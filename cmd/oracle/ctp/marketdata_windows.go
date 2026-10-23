//go:build windows

package ctp

import (
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
	rows, err := queryRows(c, &c.marketRows, "ReqQryDepthMarketData", unsafe.Pointer(&f), timeout)
	if err != nil {
		return nil, err
	}
	// 行情查询可能前缀匹配期权；不在回调中使用共享的“当前合约”字段。
	return uniqueQueryRow(rows, func(r def.CThostFtdcDepthMarketDataField) bool {
		return text(r.InstrumentID[:]) == inst && text(r.ExchangeID[:]) == ex
	})
}

// registerMarketDataCallback 注册行情查询应答。由 Connect 调用。
func (c *Client) registerMarketDataCallback() {
	c.on("SetOnRspQryDepthMarketData", func(row *def.CThostFtdcDepthMarketDataField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		c.marketRows.receive(id, row, errOf(info), last)
		return 0
	})
}
