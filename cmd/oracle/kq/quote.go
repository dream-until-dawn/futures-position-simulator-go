package kq

import (
	"fmt"
	"strings"
	"time"
)

// Quote 是行情截面里与本项目相关的字段。
//
// 只取用得上的：判别实验要的是**规则数据**（乘数、最小变动价位、每手保证金与手续费）
// 与**结算价基线**（昨结算、今结算、涨跌停），不是盘口。
type Quote struct {
	Symbol         string
	InstrumentID   string
	VolumeMultiple float64
	PriceTick      float64
	PriceDecs      float64

	// ⚠️ 天勤给的 margin / commission 是「每手」的单一数值，
	// 不是 CTP 的 6 个手续费率 + 4 个保证金率。用它做规则数据会**丢掉平今费率**
	// 这个维度——而日内策略的手续费几乎全落在那上面。
	MarginPerLot     float64
	CommissionPerLot float64

	LastPrice     float64
	UpperLimit    float64
	LowerLimit    float64
	PreSettlement float64
	Settlement    float64
	PreClose      float64
	AskPrice1     float64
	BidPrice1     float64
	DateTime      string

	Raw map[string]any
}

// Valid 报告这条行情是否**可用于下单定价**。
//
// ⚠️ 只要涨跌停价，不要 price_tick —— 实测免费行情网关**不下发静态合约字段**
// （volume_multiple / price_tick / margin / commission 全为 null），
// 它们来自另一条链路（见 InstrumentInfo）。
// 早先把 price_tick > 0 写进这里，结果是行情明明到齐了却报「未就绪」，
// 而提示语还写着「可能非交易时段」—— 一个把自己的 bug 说成环境问题的判据。
//
// 分开判「有没有」和「是不是零」：非交易时段 last_price 可能缺失或为 0，
// 拿 0 去下单会被拒，而拒单原因看起来像别的问题。
func (q Quote) Valid() bool {
	return q.UpperLimit > 0 && q.LowerLimit > 0
}

// HasSpec 报告静态合约字段是否可用（乘数与最小变动价位）。
func (q Quote) HasSpec() bool { return q.VolumeMultiple > 0 && q.PriceTick > 0 }

// SubscribeQuotes 订阅一组合约的行情。symbols 形如 SHFE.rb2601。
func (c *Client) SubscribeQuotes(symbols ...string) error {
	if c.mdConn == nil {
		return fmt.Errorf("行情连接未建立")
	}
	c.logf("[md] 订阅 %s", strings.Join(symbols, ","))
	return c.send(c.mdConn, map[string]any{
		"aid": "subscribe_quote", "ins_list": strings.Join(symbols, ","),
	})
}

// QuoteOf 取一条行情。第二个返回值报告它是否已出现在截面里。
func (c *Client) QuoteOf(symbol string) (Quote, bool) {
	raw, ok := Dig(c.QuoteSnapshot(), "quotes", symbol).(map[string]any)
	if !ok {
		return Quote{}, false
	}
	q := Quote{Symbol: symbol, Raw: raw}
	q.InstrumentID, _ = raw["instrument_id"].(string)
	q.DateTime, _ = raw["datetime"].(string)
	q.VolumeMultiple = MustNum(raw, "volume_multiple")
	q.PriceTick = MustNum(raw, "price_tick")
	q.PriceDecs = MustNum(raw, "price_decs")
	q.MarginPerLot = MustNum(raw, "margin")
	q.CommissionPerLot = MustNum(raw, "commission")
	q.LastPrice = MustNum(raw, "last_price")
	q.UpperLimit = MustNum(raw, "upper_limit")
	q.LowerLimit = MustNum(raw, "lower_limit")
	q.PreSettlement = MustNum(raw, "pre_settlement")
	q.Settlement = MustNum(raw, "settlement")
	q.PreClose = MustNum(raw, "pre_close")
	q.AskPrice1 = MustNum(raw, "ask_price1")
	q.BidPrice1 = MustNum(raw, "bid_price1")
	return q, true
}

// WaitQuoteReady 等一条行情出现且可用于定价。
func (c *Client) WaitQuoteReady(symbol string, timeout time.Duration) (Quote, bool) {
	var last Quote
	deadline := time.Now().Add(timeout)
	for {
		if q, ok := c.QuoteOf(symbol); ok {
			last = q
			if q.Valid() {
				return q, true
			}
		}
		if time.Now().After(deadline) {
			return last, false
		}
		c.WaitQuote(500 * time.Millisecond)
	}
}

// AggressivePrice 给出一个「几乎必成」的限价：买用涨停价，卖用跌停价。
//
// 这不是市价单。**本客户端只发限价单**——市价单在各交易所的支持程度不一，
// 且成交价不可控，而判别实验要求输入可复现。用涨跌停价下限价单，
// 效果是尽快成交，同时成交价仍受涨跌停约束、不会离谱。
func (q Quote) AggressivePrice(dir Direction) float64 {
	if dir == Buy {
		return q.UpperLimit
	}
	return q.LowerLimit
}

// FarPrice 给出一个「几乎不可能成交」的限价：买用跌停价，卖用涨停价。
//
// 实验 6（报单被拒的错误码）与「挂单冻结」的观测都要它——需要一笔挂得住、
// 不会立刻成交的委托。
func (q Quote) FarPrice(dir Direction) float64 {
	if dir == Buy {
		return q.LowerLimit
	}
	return q.UpperLimit
}
