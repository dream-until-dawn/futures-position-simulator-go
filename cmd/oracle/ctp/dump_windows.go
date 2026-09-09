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
