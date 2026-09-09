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
func (c *Client) registerQueryCallbacks() {
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
	c.on("SetOnRspQryInvestorPosition", func(p *def.CThostFtdcInvestorPositionField,
		info *def.CThostFtdcRspInfoField, _ int, isLast bool) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ 查持仓失败 %v", err)
		}
		if p != nil && text(p.InstrumentID[:]) != "" {
			cp := *p
			key := text(cp.ExchangeID[:]) + "." + text(cp.InstrumentID[:])
			c.posMu.Lock()
			// ⚠️ 同一个合约会回**多条**（今仓一条、昨仓一条，由 PositionDate 区分）。
			// 按合约键会互相覆盖，所以键里带上 PositionDate。
			c.pos[key+"/"+string(rune(cp.PositionDate))] = &cp
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
