//go:build windows

package ctp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// Write 把一份夹具落到 dir 下，并在落盘**之前**做独立的凭据复查。
//
// ⚠️ 复查放在这里而不是调用方：一个「记得先查一下」的约定，
// 与没有这道检查在出事那天是一样的。
func (fx *Fixture) Write(dir, name string, secrets map[string]string,
	logf func(string, ...any)) (string, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	b, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return "", err
	}
	if bs := BlindSpots(secrets); len(bs) > 0 {
		// ⚠️ 明说查不了什么，免得「没报错」被读成「都查过了」。
		logf("  ⓘ 独立复查的盲区：%v —— 这几个值太短，在夹具里搜它们只会撞上数字", bs)
	}
	if err := Scrubbed(string(b), secrets); err != nil {
		return "", fmt.Errorf("脱敏自检失败，**不落盘**：%w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.json", name, fx.TradingDay))
	if _, err := os.Stat(path); err == nil {
		// ⚠️ 同名不覆盖：两份都是证据，谁也不该把谁擦掉。
		for i := 2; ; i++ {
			alt := filepath.Join(dir, fmt.Sprintf("%s-%s-%d.json", name, fx.TradingDay, i))
			if _, err := os.Stat(alt); err != nil {
				path = alt
				break
			}
		}
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	logf("CTP 夹具落盘 %s（%d 字节，去掉 %d 个键）", abs, len(b), len(fx.Dropped))
	return path, nil
}

// 让编译器确认 def 被用到（BrokerParams 的字段类型）。
var _ = def.CThostFtdcBrokerTradingParamsField{}
