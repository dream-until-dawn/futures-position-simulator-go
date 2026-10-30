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
	rows, err := queryRows(c, &c.accountRows, "ReqQryTradingAccount", unsafe.Pointer(&f), timeout)
	if err != nil {
		return nil, err
	}
	return uniqueQueryRow(rows, nil)
}

// Positions 查持仓，键为合约 / 方向 / 今昨 / 投保，不合并不同属性。
//
// ⚠️ CTP 的持仓查询是**分条回**的，最后一条带 isLast。空仓时会回一条
// **空的** isLast —— 于是「没有持仓」与「查询失败」在数据上分得开，
// 而这里必须让调用方也分得开：返回空 map + nil error，不是 error。
func (c *Client) Positions(timeout time.Duration) (map[string]*def.CThostFtdcInvestorPositionField, error) {
	c.q.wait()
	defer c.q.done()
	f := def.CThostFtdcQryInvestorPositionField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	rows, err := queryRows(c, &c.positionRows, "ReqQryInvestorPosition", unsafe.Pointer(&f), timeout)
	if err != nil {
		return nil, err
	}
	return positionRowsByKey(rows), nil
}

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
	rows, err := c.queryOrders(timeout)
	if err != nil {
		return nil, err
	}
	out := make([]*def.CThostFtdcOrderField, 0, len(rows))
	for i := range rows {
		if rows[i].OrderStatus == def.THOST_FTDC_OST_NoTradeQueueing || rows[i].OrderStatus == def.THOST_FTDC_OST_PartTradedQueueing {
			out = append(out, &rows[i])
		}
	}
	return out, nil
}

// UnfinishedOrders 包括未知及过渡态；用于前置检查，不把“还未确认排队”当作没有委托。
func (c *Client) UnfinishedOrders(timeout time.Duration) ([]*def.CThostFtdcOrderField, error) {
	rows, err := c.queryOrders(timeout)
	if err != nil {
		return nil, err
	}
	return unfinishedOrderRows(rows), nil
}

func (c *Client) queryOrders(timeout time.Duration) ([]def.CThostFtdcOrderField, error) {
	c.q.wait()
	defer c.q.done()
	f := def.CThostFtdcQryOrderField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	rows, err := queryRows(c, &c.orderRows, "ReqQryOrder", unsafe.Pointer(&f), timeout)
	if err != nil {
		return nil, err
	}
	return rows, nil
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
func (c *Client) CommissionRate(symbol string, timeout time.Duration) (*def.CThostFtdcInstrumentCommissionRateField, error) {
	c.q.wait()
	defer c.q.done()
	ex, inst := SplitSymbol(symbol)
	f := def.CThostFtdcQryInstrumentCommissionRateField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	copy(f.ExchangeID[:], ex)
	copy(f.InstrumentID[:], inst)
	rows, err := queryRows(c, &c.commissionRows, "ReqQryInstrumentCommissionRate", unsafe.Pointer(&f), timeout)
	if err != nil {
		return nil, err
	}
	// SimNow 本次只读实测的费率 ExchangeID 为空；合约仍须精确相等，非空交易所不能冲突。
	return uniqueQueryRow(rows, func(r def.CThostFtdcInstrumentCommissionRateField) bool {
		return text(r.InstrumentID[:]) == inst && (text(r.ExchangeID[:]) == "" || text(r.ExchangeID[:]) == ex)
	})
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
	f := def.CThostFtdcQryTradeField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	rows, err := queryRows(c, &c.tradeRows, "ReqQryTrade", unsafe.Pointer(&f), timeout)
	if err != nil {
		return nil, err
	}
	out := make([]*def.CThostFtdcTradeField, 0, len(rows))
	for i := range rows {
		if text(rows[i].TradeID[:]) != "" {
			out = append(out, &rows[i])
		}
	}
	return out, nil
}

// SettlementText 查某个交易日的**结算单正文**（GBK 原始字节）。
//
// ⚠️ 结算单抬头带投资者代码与姓名：本方法只把字节交给调用方，调用方只许取白名单里的那一节（成交记录），
// 不许原样打印、不许落盘（InvestorID 不许打印 / 持久化）。
// ⚠️ 超时是**没有结论**，不是「那天没有结算单」。
func (c *Client) SettlementText(day string, timeout time.Duration) ([]byte, error) {
	c.q.wait()
	defer c.q.done()
	f := def.CThostFtdcQrySettlementInfoField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	copy(f.TradingDay[:], day)
	rows, err := queryRows(c, &c.settlementRows, "ReqQrySettlementInfo", unsafe.Pointer(&f), timeout)
	if err != nil {
		return nil, err
	}
	var out []byte
	for i := range rows {
		part := rows[i].Content[:]
		if n := indexZero(part); n >= 0 {
			part = part[:n]
		}
		out = append(out, part...)
	}
	return out, nil
}

// indexZero 返回第一个 0 字节的位置（没有就是 -1）。
func indexZero(b []byte) int {
	for i, x := range b {
		if x == 0 {
			return i
		}
	}
	return -1
}

// Instrument 查柜台**声明**的合约参数 —— 本项目要的是 `PriceTick` 与 `VolumeMultiple`。
//
// # ⚠️ 它消掉的是一处「猜错了不会报错」
//
// `ctp-reject` 的 `-tick` 一直要人手填，而填错的表现**不是报错**：
// 四条用例照样跑完、照样拿到四个码，只是其中两条测到的是**另一种拒因**，
// 而输出上仍标着原来那个名字（20260914 夜盘就这样错过一次：`bc` 的 tick 是 10，
// 命令里写着 1，「低于跌停」那一笔同时违反了步长，柜台报的是 `48`）。
//
// ⇒ 让**柜台自己说**。这不是第二个独立来源（与行情、与拒因同一个柜台），
// 但它把「人手转录」这一环整个去掉了 —— 而那一环正是唯一出过错的那一环。
//
// ⚠️ 郑商所 / 广期所此前拍不了拒因语料，理由写的是「仓库里没有它们最小变动价位的出处」。
// 这个查询就是那个出处。
func (c *Client) Instrument(symbol string, timeout time.Duration) (*def.CThostFtdcInstrumentField, error) {
	c.q.wait()
	defer c.q.done()
	ex, inst := SplitSymbol(symbol)
	f := def.CThostFtdcQryInstrumentField{}
	copy(f.ExchangeID[:], ex)
	copy(f.InstrumentID[:], inst)
	rows, err := queryRows(c, &c.instrumentRows, "ReqQryInstrument", unsafe.Pointer(&f), timeout)
	if err != nil {
		return nil, err
	}
	// 前缀匹配可能带回期权或相似合约，完整结束后才挑唯一精确匹配。
	return uniqueQueryRow(rows, func(r def.CThostFtdcInstrumentField) bool {
		return text(r.InstrumentID[:]) == inst && text(r.ExchangeID[:]) == ex
	})
}

func (c *Client) registerQueryCallbacks() {
	c.on("SetOnRspQryInstrument", func(row *def.CThostFtdcInstrumentField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		c.instrumentRows.receive(id, row, errOf(info), last)
		return 0
	})
	c.on("SetOnRspQryInstrumentCommissionRate", func(row *def.CThostFtdcInstrumentCommissionRateField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		c.commissionRows.receive(id, row, errOf(info), last)
		return 0
	})
	c.on("SetOnRspQrySettlementInfo", func(row *def.CThostFtdcSettlementInfoField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		c.settlementRows.receive(id, row, errOf(info), last)
		return 0
	})
	c.on("SetOnRspQryTrade", func(row *def.CThostFtdcTradeField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		c.tradeRows.receive(id, row, errOf(info), last)
		return 0
	})
	c.on("SetOnRspQryTradingAccount", func(row *def.CThostFtdcTradingAccountField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		c.accountRows.receive(id, row, errOf(info), last)
		return 0
	})
	c.on("SetOnRspQryOrder", func(row *def.CThostFtdcOrderField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		c.orderRows.receive(id, row, errOf(info), last)
		return 0
	})
	c.on("SetOnRspQryInvestorPosition", func(row *def.CThostFtdcInvestorPositionField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		c.positionRows.receive(id, row, errOf(info), last)
		return 0
	})
	c.on("SetOnRspQryBrokerTradingParams", func(row *def.CThostFtdcBrokerTradingParamsField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		c.brokerRows.receive(id, row, errOf(info), last)
		return 0
	})
}

// queryRows 在回调之前绑定请求编号，并将即时拒绝与分批回报错误统一交给收集器。
func queryRows[T any](c *Client, b *queryBatch[T], name string, p unsafe.Pointer, timeout time.Duration) ([]T, error) {
	id := c.nextRequestID()
	rows, err := runQuery(b, id, timeout, func(requestID int) int32 {
		c.setupMu.Lock()
		defer c.setupMu.Unlock()
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			return nativeHandshakeBindingError
		}
		if c.querySend != nil {
			return c.querySend(name, p, requestID)
		}
		result, err := invokeNativeQuery(c.api, name, p, requestID, c.nativeSDKModule)
		if err != nil {
			c.logf("[ctp.query.native] event=binding-error")
			return nativeHandshakeBindingError
		}
		return result
	})
	if err != nil {
		return nil, fmt.Errorf("%s 没有完整成功：%w", name, err)
	}
	return rows, nil
}

// abortQueries 在释放API前唤醒全部活跃查询；关闭不能把部分回报包装为成功。
func (c *Client) abortQueries() {
	c.positionRows.abort(errQueryClosed)
	c.orderRows.abort(errQueryClosed)
	c.tradeRows.abort(errQueryClosed)
	c.settlementRows.abort(errQueryClosed)
	c.accountRows.abort(errQueryClosed)
	c.commissionRows.abort(errQueryClosed)
	c.instrumentRows.abort(errQueryClosed)
	c.marketRows.abort(errQueryClosed)
	c.brokerRows.abort(errQueryClosed)
}
