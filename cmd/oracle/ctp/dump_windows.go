//go:build windows

package ctp

import (
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// Capture 拍一份 CTP 侧的截面并脱敏。
//
// ⚠️ 顺序是**先脱敏、再落盘**，与 kq 那侧同一条纪律：
// 先落原始盘再擦，原始文件已经上过磁盘、可能已经进过 git index ——
// 一次 `git add -A` 就够了。
// AttachQuote 往一份已经拍好的截面上补一条行情快照。
//
// # ⚠️ 为什么是**单独一步**，不并进 Capture
//
// 行情要指定合约，而 `Capture` 拍的是账户与持仓 —— 它不知道该问哪个合约，
// 也不该猜。⚠️ 更要紧的是：把它并进去意味着**每一次拍截面都会多发一次查询**，
// 而 CTP 的查询是**限流**的（见 QueryGap），一次多余的查询会让下一次该查的排队。
//
// # ⚠️ 它补的是一个**已经声明过的盲区**
//
// probes.md §6.9 写着：`PositionProfit` 与今结算价严格对上，
// 但夹具里没有最新价字段，所以「基准是今结算价」与「基准是最新价而此刻两者相等」
// **分不开**。⚠️ **而声明一个盲区不等于关掉它** —— 20260910 评审指出，
// 次日那份实验清单里没有一件抓行情，于是那个盲区会原样留到后天。
//
// ⚠️ 最该抓的一刻是**开盘那一瞬**：停盘期间最新价与结算价大概率相等（分不开），
// 而重开之后行情在动，两者最可能不等（分得开）。
func (c *Client) AttachQuote(f *Fixture, symbol string, timeout time.Duration) error {
	md, err := c.MarketData(symbol, timeout)
	if err != nil {
		return fmt.Errorf("查 %s 的行情：%w", symbol, err)
	}
	m, dropped, err := sanitizeStruct(*md, quoteFields)
	if err != nil {
		return fmt.Errorf("脱敏 %s 的行情：%w", symbol, err)
	}
	if f.Quotes == nil {
		f.Quotes = map[string]map[string]any{}
	}
	f.Quotes[symbol] = m
	f.Dropped = append(f.Dropped, dropped...)
	return nil
}

func (c *Client) Capture(timeout time.Duration, note string) (*Fixture, error) {
	f := &Fixture{
		Source:     Source,
		TradingDay: c.TradingDay(),
		CapturedAt: time.Now().Format(time.RFC3339),
		Note:       note,
		Positions:  map[string]map[string]any{},
	}
	if f.TradingDay == "" {
		// ⚠️ 与 kq 那侧同理：一份不知道属于哪一天的截面，
		// 与一份没落盘的截面在证据上是同一个位置。
		return nil, fmt.Errorf("这份截面没有 trading_day —— 不落盘")
	}

	p, err := c.BrokerParams(timeout)
	if err != nil {
		return nil, fmt.Errorf("查经纪商参数：%w", err)
	}
	f.BrokerParams = map[string]any{
		"MarginPriceType":         string(p.MarginPriceType),
		"Algorithm":               string(p.Algorithm),
		"AvailIncludeCloseProfit": string(p.AvailIncludeCloseProfit),
		"CurrencyID":              text(p.CurrencyID[:]),
		"OptionRoyaltyPriceType":  string(p.OptionRoyaltyPriceType),
	}

	acc, err := c.Account(timeout)
	if err != nil {
		return nil, fmt.Errorf("查资金账户：%w", err)
	}
	accMap, accDropped, err := sanitizeStruct(*acc, accountFields)
	if err != nil {
		return nil, err
	}
	f.Account = accMap
	f.Dropped = append(f.Dropped, accDropped...)

	pos, err := c.Positions(timeout)
	if err != nil {
		return nil, fmt.Errorf("查持仓：%w", err)
	}
	seenDropped := map[string]bool{}
	for _, d := range f.Dropped {
		seenDropped[d] = true
	}
	for key, one := range pos {
		m, dropped, err := sanitizeStruct(*one, positionFields)
		if err != nil {
			return nil, err
		}
		f.Positions[key] = m
		for _, d := range dropped {
			if !seenDropped[d] {
				seenDropped[d] = true
				f.Dropped = append(f.Dropped, d)
			}
		}
	}
	return f, nil
}

// 让编译器确认 def 被用到（BrokerParams 的字段类型）。
var _ = def.CThostFtdcBrokerTradingParamsField{}

// AttachTrades 把**当日成交明细**附进一份截面。
//
// # ⚠️ 它与 AttachQuote 同一条纪律：补不上就让整份不落盘
//
// 调用方拿到 error 必须**不写盘** —— 一份缺了 `trades` 的截面与一份
// 本来就不要 trades 的截面**在磁盘上长得一模一样**，而后者正常、前者是事故。
// 守卫 `TestAttachTradesHasOneCallSite`。
//
// ⚠️ `symbol` 用来**筛**，而筛掉的那些**只记数不记内容**：
// 「今天这个合约成交了 N 笔、账上另有 M 笔别的合约」这件事本身要看得见 ——
// 一份只剩本合约的 trades，与一个只在本合约上交易过的账户，在夹具里同形。
func (c *Client) AttachTrades(f *Fixture, symbol string, timeout time.Duration) error {
	all, err := c.Trades(timeout)
	if err != nil {
		return fmt.Errorf("查成交明细：%w", err)
	}
	_, want := SplitSymbol(symbol)
	other := 0
	for _, t := range all {
		if want != "" && text(t.InstrumentID[:]) != want {
			other++
			continue
		}
		m, dropped, err := sanitizeStruct(*t, tradeFields)
		if err != nil {
			return fmt.Errorf("脱敏成交明细：%w", err)
		}
		f.Trades = append(f.Trades, m)
		f.Dropped = append(f.Dropped, dropped...)
	}
	if len(f.Trades) == 0 {
		// ⚠️ 一笔都没筛出来时**报错**，不是默默附一个空数组：
		// 本函数只在「要按片次序」的实验里被调用，而那种实验
		// 一定已经成交过 —— 空数组意味着查询或筛选出了问题。
		return fmt.Errorf("⚠️ %s 上一笔成交都没筛出来（当日共 %d 笔，别的合约 %d 笔）—— "+
			"要按片次序的实验不该看到这个，多半是查询或筛选出了问题",
			symbol, len(all), other)
	}
	c.logf("[ctp] 成交明细 %s %d 笔（当日共 %d 笔，别的合约 %d 笔）",
		symbol, len(f.Trades), len(all), other)
	return nil
}
