//go:build windows

package ctp

import (
	"fmt"
	"time"
	"unsafe"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// MarketData 查一个合约的行情快照（**查询，不是订阅**）。
//
// ⚠️ 它给的字段里**包含盘口**（`BidPrice1` / `AskPrice1`）——
// 而快期那一侧 `match` 当初卡住的理由正是「成交价要盘口，而 `quoteKeep`
// 刻意不留 bid/ask」。**那个理由在这一侧不成立。**
//
// ⚠️ 但「不做盘口」是使用者 2026-09-09 的裁决（roadmap 的 match 一节），
// 这里**不重开它** —— 只把「这一侧拿得到」这个事实记下来，
// 因为将来若要重议，需要知道的正是这一点。
//
// 本函数当下只为一件事服务：拿到**涨跌停价**，好构造一笔**挂得上但成不了**的委托。
func (c *Client) MarketData(symbol string, timeout time.Duration) (*def.CThostFtdcDepthMarketDataField, error) {
	c.q.wait()
	defer c.q.done()
	ex, inst := SplitSymbol(symbol)
	f := def.CThostFtdcQryDepthMarketDataField{}
	copy(f.ExchangeID[:], ex)
	copy(f.InstrumentID[:], inst)
	// ⚠️ **必须精确匹配**：CTP 的行情查询是**前缀匹配** ——
	// 问 rb2701 会连 rb2701C3000 / rb2701P3100 这些**期权**一起回来，
	// 而它们的涨跌停与期货差一个数量级（实测期权跌停 33.5，期货在 3000 附近）。
	// ⚠️ 20260910 夜盘第一次真发单就栽在这里：拿期权的跌停价给期货报单，被拒且**无声**。
	c.wantInst = inst
	c.req("ReqQryDepthMarketData", unsafe.Pointer(&f))
	select {
	case m := <-c.md:
		if m == nil {
			return nil, fmt.Errorf("%s 的行情查询没有返回内容 —— "+
				"⚠️ 夜盘刚开或该合约不在交易时段时会这样，那与「合约不存在」不同", symbol)
		}
		return m, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("%v 内没有等到 %s 的行情应答 —— ⚠️ **没有结论**", timeout, symbol)
	}
}

// registerMarketDataCallback 注册行情查询应答。由 Connect 调用。
func (c *Client) registerMarketDataCallback() {
	c.on("SetOnRspQryDepthMarketData", func(m *def.CThostFtdcDepthMarketDataField,
		info *def.CThostFtdcRspInfoField, _ int, _ bool) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ 行情查询失败 %v", err)
			c.md <- nil
			return 0
		}
		if m == nil || text(m.InstrumentID[:]) == "" {
			return 0
		}
		if got := text(m.InstrumentID[:]); got != c.wantInst {
			// ⚠️ 前缀匹配带回来的别的合约：记一笔就丢，**不要当成答案**。
			c.logf("[ctp] （略过前缀命中的 %s，要的是 %s）", got, c.wantInst)
			return 0
		}
		cp := *m
		// ⚠️ 打出**回来的是哪个合约** —— 「查到了」与「查到的是我问的那个」是两件事。
		c.logf("[ctp] 行情应答 %s.%s 最新=%.2f 涨停=%.2f 跌停=%.2f 昨结=%.2f",
			text(cp.ExchangeID[:]), text(cp.InstrumentID[:]), float64(cp.LastPrice),
			float64(cp.UpperLimitPrice), float64(cp.LowerLimitPrice), float64(cp.PreSettlementPrice))
		c.md <- &cp
		return 0
	})
}
