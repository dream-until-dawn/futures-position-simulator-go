//go:build windows

package ctp

import (
	"fmt"
	"sync"
	"time"
	"unsafe"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// QueryGap 是两次查询之间的最小间隔。
//
// ⚠️ **这才是「查询节流」真正需要的地方** —— 而不是 probes.md §6.3 那个
// 已被推翻的诊断说的地方。CTP 的查询接口按 session 限频（约每秒一次），
// 而限频的表现是**后一次查询的应答根本不来**，不是返回一个错误。
//
// ⚠️ 于是「节流」这件事有两个完全不同的版本：
//
//	被推翻的那个   goctp 自己的登录后查询链不节流   —— **不成立**，它有 1100ms 的 ticker
//	真实存在的这个 **我们自己**连着发多个查询时要排队 —— 成立，且只需要这一行
//
// 记着这个区别是因为：同一个词指向两件事时，**证伪了一件不等于证伪了另一件**。
const QueryGap = 1200 * time.Millisecond

// queryer 把「发一个查询、等它的应答」串起来，并保证两次之间隔够。
//
// ⚠️ 串行是必须的，不是保守：并发发两个查询时，CTP 只回一个，
// 而另一个**静默消失** —— 调用方等到的是超时，看起来像「柜台没这个数据」。
type queryer struct {
	mu   sync.Mutex
	last time.Time
}

func (q *queryer) wait() {
	q.mu.Lock()
	if d := QueryGap - time.Since(q.last); d > 0 {
		time.Sleep(d)
	}
}

func (q *queryer) done() {
	q.last = time.Now()
	q.mu.Unlock()
}

// Account 查资金账户截面。
func (c *Client) Account(timeout time.Duration) (*def.CThostFtdcTradingAccountField, error) {
	c.q.wait()
	defer c.q.done()
	f := def.CThostFtdcQryTradingAccountField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	c.req("ReqQryTradingAccount", unsafe.Pointer(&f))
	select {
	case a := <-c.account:
		if a == nil {
			return nil, fmt.Errorf("查资金账户没有返回内容")
		}
		return a, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%v 内没有等到资金账户的应答 —— ⚠️ **没有结论**，不是「查不到」", timeout)
	}
}

// Positions 查持仓，按 "SHFE.rb2701" 键。
//
// ⚠️ CTP 的持仓查询是**分条回**的，最后一条带 isLast。空仓时会回一条
// **空的** isLast —— 于是「没有持仓」与「查询失败」在数据上分得开，
// 而这里必须让调用方也分得开：返回空 map + nil error，不是 error。
func (c *Client) Positions(timeout time.Duration) (map[string]*def.CThostFtdcInvestorPositionField, error) {
	c.q.wait()
	defer c.q.done()
	c.posMu.Lock()
	c.pos = map[string]*def.CThostFtdcInvestorPositionField{}
	c.posMu.Unlock()

	f := def.CThostFtdcQryInvestorPositionField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	c.req("ReqQryInvestorPosition", unsafe.Pointer(&f))
	select {
	case <-c.posDone:
		c.posMu.Lock()
		defer c.posMu.Unlock()
		out := make(map[string]*def.CThostFtdcInvestorPositionField, len(c.pos))
		for k, v := range c.pos {
			out[k] = v
		}
		return out, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%v 内没有等到持仓查询的最后一条 —— "+
			"⚠️ **没有结论**，不是「没有持仓」：空仓时柜台会回一条**空的** isLast，"+
			"两者在数据上本来是分得开的", timeout)
	}
}

// registerQueryCallbacks 注册账户与持仓的应答回调。由 Connect 调用。
// LiveOrders 查**还挂着的**委托。
//
// ⚠️ 20260911 夜盘补的，而它补的是一整类缺失的动作：此前这个包
// **能下单、却不能清理自己下出去的单**。
//
// 当晚我用 `timeout 150` 包着 `go run ctp-fee`，150 秒不够，
// 外部 kill 落在「下单之后、撤单之前」⇒ 两笔买开挂单留在账上，
// **而没有任何办法拿到它们的 `OrderRef`**，只能等 GFD 在收盘时自动撤。
//
//	⚠️ 撤单需要 ref，而 ref 只活在下单的那个进程里 ——
//	**一个只能由制造者清理的残留，在制造者死掉时就清理不了了。**
//
// ⚠️ 返回的是**当前还活着**的那些（`OST_NoTradeQueueing` / `PartTradedQueueing`），
// 不是全部委托：已成交与已撤的没有可撤性，混在一起只会让调用方再筛一次。
func (c *Client) LiveOrders(timeout time.Duration) ([]*def.CThostFtdcOrderField, error) {
	c.q.wait()
	defer c.q.done()
	c.ordMu.Lock()
	c.ord = nil
	c.ordMu.Unlock()

	f := def.CThostFtdcQryOrderField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	c.req("ReqQryOrder", unsafe.Pointer(&f))
	select {
	case <-c.ordDone:
		c.ordMu.Lock()
		defer c.ordMu.Unlock()
		out := make([]*def.CThostFtdcOrderField, len(c.ord))
		copy(out, c.ord)
		return out, nil
	case <-time.After(timeout):
		// ⚠️ 与查持仓同一条纪律：超时是**没有结论**，不是「没有挂单」。
		// 空仓/空委托时柜台会回一条**空的** isLast，两者在数据上分得开。
		return nil, fmt.Errorf("%v 内没有等到委托查询的最后一条 —— "+
			"⚠️ **没有结论**，不是「没有挂单」", timeout)
	}
}

// CommissionRate 查柜台**声明**的手续费率。

// ⚠️ 它补的是一整类此前只能反解的东西：本项目的手续费公式
// （`名义金额 × ByMoney + 手数 × ByVolume`）一直是**从冻结额解出来的** ——
// 三个点解两个参数，余量当证据。而柜台一直能直接把这六个数说出来：
//
//	开仓   OpenRatioByMoney / OpenRatioByVolume
//	平昨   CloseRatioByMoney / CloseRatioByVolume
//	平今   CloseTodayRatioByMoney / CloseTodayRatioByVolume
//
// ⚠️ **而这不是第二个独立来源。**它与冻结额来自同一个柜台，
// 只是换了条查询路径 —— 声明与行为。§13 #1 的教训正在这里：
// `MarginPriceType "4" 开仓价` 是一句真的声明，而它**只管今仓**，
// 把它读成「对所有持仓都成立」就错了。
//
//	⇒ 声明对得上行为时，它涨的是**这条公式的可读性**，不是证据等级。
//	   对不上时才是新东西 —— 那说明我把某句声明读宽了。
func (c *Client) CommissionRate(symbol string, timeout time.Duration) (
	*def.CThostFtdcInstrumentCommissionRateField, error) {
	c.q.wait()
	defer c.q.done()
	ex, inst := SplitSymbol(symbol)
	f := def.CThostFtdcQryInstrumentCommissionRateField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	copy(f.ExchangeID[:], ex)
	copy(f.InstrumentID[:], inst)
	// ⚠️ 排空上一次可能残留的应答：这个通道深度 1，
	// 一条陈旧的费率与一条刚查到的**在类型上一模一样**。
	select {
	case <-c.comm:
	default:
	}
	c.req("ReqQryInstrumentCommissionRate", unsafe.Pointer(&f))
	select {
	case r := <-c.comm:
		if r == nil {
			return nil, fmt.Errorf("查 %s 的手续费率：柜台回了空", symbol)
		}
		return r, nil
	case <-time.After(timeout):
		// ⚠️ 与查持仓同一条纪律：超时是**没有结论**，不是「这个合约没有费率」。
		return nil, fmt.Errorf("%v 内没有等到 %s 的手续费率 —— "+
			"⚠️ **没有结论**，不是「它不收费」", timeout, symbol)
	}
}

// Trades 查**当日成交明细**。
//
// # ⚠️ 它补的是 #13 两次都栽在的那个洞
//
// 持仓记录里**没有按片的开仓时刻**：`OpenAmount` 是当日累计，
// 它只贡献那些片的**和** ⇒ 从持仓截面能算出「被消耗的那片值多少」，
// 却算不出**哪一片先开** ⇒ FIFO 与 LIFO 分不开。
//
//	⚠️ 20260912 评审打回的正是这一点：那次「修好了」的落盘（平仓前后各一份）
//	只给出集合 `{被消耗的, 存活的} = {p1, p2}`，**次序不在里面**。
//
// ⇒ 成交明细直接给出 `Price` + `TradeTime` + `SequenceNo`：
// **逐笔价与次序由柜台自己说出来**，不用从持仓反解，
// 也不依赖任何运行开关 —— `-restfirst` 那条路把判别性的事实放回了运行配置里，
// 而那正是 #13 栽过的地方。
//
// ⚠️ 返回**全部**当日成交，不按合约过滤：过滤要用 `InstrumentID`，
// 而漏掉同品种别月份的成交会让「这一片是哪来的」少一条线索。调用方自己筛。
func (c *Client) Trades(timeout time.Duration) ([]*def.CThostFtdcTradeField, error) {
	c.q.wait()
	defer c.q.done()
	c.trdMu.Lock()
	c.trd = nil
	c.trdMu.Unlock()

	f := def.CThostFtdcQryTradeField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	c.req("ReqQryTrade", unsafe.Pointer(&f))
	select {
	case <-c.trdDone:
		c.trdMu.Lock()
		defer c.trdMu.Unlock()
		out := make([]*def.CThostFtdcTradeField, len(c.trd))
		copy(out, c.trd)
		return out, nil
	case <-time.After(timeout):
		// ⚠️ 与查持仓同一条纪律：超时是**没有结论**，不是「今天没有成交」。
		// 空成交时柜台回一条**空的** isLast，两者在数据上分得开。
		return nil, fmt.Errorf("%v 内没有等到成交查询的最后一条 —— "+
			"⚠️ **没有结论**，不是「今天没成交」", timeout)
	}
}

func (c *Client) registerQueryCallbacks() {
	c.on("SetOnRspQryInstrumentCommissionRate", func(r *def.CThostFtdcInstrumentCommissionRateField,
		info *def.CThostFtdcRspInfoField, _ int, _ bool) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ 查手续费率失败 %v", err)
			c.comm <- nil
			return 0
		}
		if r == nil {
			c.comm <- nil
			return 0
		}
		cp := *r
		c.comm <- &cp
		return 0
	})
	c.on("SetOnRspQryTrade", func(t *def.CThostFtdcTradeField,
		info *def.CThostFtdcRspInfoField, _ int, isLast bool) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ 查成交失败 %v", err)
		}
		if t != nil && text(t.TradeID[:]) != "" {
			ct := *t
			c.trdMu.Lock()
			c.trd = append(c.trd, &ct)
			c.trdMu.Unlock()
		}
		if isLast {
			select {
			case c.trdDone <- struct{}{}:
			default:
			}
		}
		return 0
	})
	c.on("SetOnRspQryTradingAccount", func(a *def.CThostFtdcTradingAccountField,
		info *def.CThostFtdcRspInfoField, _ int, _ bool) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ 查资金账户失败 %v", err)
			c.account <- nil
			return 0
		}
		if a == nil {
			c.account <- nil
			return 0
		}
		cp := *a
		c.account <- &cp
		return 0
	})
	c.on("SetOnRspQryOrder", func(o *def.CThostFtdcOrderField,
		info *def.CThostFtdcRspInfoField, _ int, isLast bool) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ 查委托失败 %v", err)
		}
		// ⚠️ 只收**还挂着**的：已成交/已撤的没有可撤性。
		if o != nil && (o.OrderStatus == def.THOST_FTDC_OST_NoTradeQueueing ||
			o.OrderStatus == def.THOST_FTDC_OST_PartTradedQueueing) {
			co := *o
			c.ordMu.Lock()
			c.ord = append(c.ord, &co)
			c.ordMu.Unlock()
		}
		if isLast {
			select {
			case c.ordDone <- struct{}{}:
			default:
			}
		}
		return 0
	})
	c.on("SetOnRspQryInvestorPosition", func(p *def.CThostFtdcInvestorPositionField,
		info *def.CThostFtdcRspInfoField, _ int, isLast bool) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ 查持仓失败 %v", err)
		}
		if p != nil && text(p.InstrumentID[:]) != "" {
			cp := *p
			key := text(cp.ExchangeID[:]) + "." + text(cp.InstrumentID[:])
			c.posMu.Lock()
			// ⚠️ 同一个合约会回**多条**，而它们由**两个**维度区分：
			//
			//	PositionDate    今仓一条、昨仓一条
			//	PosiDirection   多头一条、空头一条   ← ⚠️ 20260910 夜盘补上的
			//
			// ⚠️ 原先键里只有 PositionDate ⇒ **多头与空头互相覆盖，后到的赢**，
			// 而丢失是**静默**的：查询正常返回，只是少了一条。
			//
			// 撞到它的经过：`ctp-profit` 开空之后连查五次都「读不到开仓价」——
			// 因为账上还留着一条已归零的**多头**记录，键相同，把空头盖掉了。
			//
			//	⚠️ 上一版的注释写着「同一个合约会回多条（今仓一条、昨仓一条）」——
			//	**它想到了一个维度会撞键，就停在了那里。**
			//	而「还有没有别的维度」这个问题，没有被问出来。
			//
			// ⚠️ 键的格式因此变了（`SHFE.rb2701/1` → `SHFE.rb2701/2/1`，
			// 先方向后今昨），落进夹具的键也跟着变 —— 读夹具的那一侧要兼容两种。
			c.pos[key+"/"+string(rune(cp.PosiDirection))+"/"+string(rune(cp.PositionDate))] = &cp
			c.posMu.Unlock()
		}
		if isLast {
			select {
			case c.posDone <- struct{}{}:
			default:
			}
		}
		return 0
	})
}
