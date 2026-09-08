// Package view 把本库的内部状态渲染成与柜台业务截面**字段级同构**的视图。
//
// 它存在的理由只有一个：让对拍变成机械的。
// 手挑几个字段比对，比的是「我记得该看哪几个」；逐字段比对，比的是**全部**。
//
// ⚠️ 本包最要紧的设计不是字段映射，是**值的三种状态**：
//
//	有值        本库算得出，可以拿去比
//	明确不建模  带到期版本，对拍时单独成档
//	还没实现    ⚠️ **绝不渲染成 0**
//
// 第三种是这个包存在的核心。一个还没实现的字段若渲染成 0，
// 而柜台那边恰好也是 0（空仓、无期权、无冻结时大量字段都是 0），
// 对拍会判「一致」——**一个缺失的实现伪装成了一致**。
// 那比算错更坏：算错会在某个样本上露出来，缺失永远不会。
package view

import (
	"fmt"
	"sort"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/shopspring/decimal"
)

// Presence 是一个字段在本库这边的状态。
type Presence uint8

const (
	// PresenceUnset 是零值：没声明。使用即报错。
	//
	// ⚠️ 一个默认落进「有值」的零值，会让忘记声明的字段全部变成 0，
	// 而 0 正是最容易与柜台「碰巧一致」的那个数。
	PresenceUnset Presence = iota
	// Present 本库算得出这个字段。
	Present
	// NotModeled 明确不建模，带到期版本。
	NotModeled
	// NotImplemented 还没实现。⚠️ 对拍时必须判失败，不许当成 0 去比。
	NotImplemented
)

func (p Presence) String() string {
	switch p {
	case Present:
		return "有值"
	case NotModeled:
		return "明确不建模"
	case NotImplemented:
		return "还没实现"
	}
	return "未声明"
}

// Value 是视图里的一个字段。
type Value struct {
	Presence Presence
	Number   decimal.Decimal

	// Until 是 NotModeled 的到期版本，如 "v0.6.0"。
	//
	// ⚠️ 不带到期版本的「不建模」是一句永久豁免。
	// design.md §5 的第二档要求它带到期版本，正是为此。
	Until string

	// Why 是 NotModeled / NotImplemented 的说明，必须非空。
	Why string
}

// Num 构造一个有值的字段。
func Num(d decimal.Decimal) Value { return Value{Presence: Present, Number: d} }

// Skip 构造一个「明确不建模」的字段。
func Skip(until, why string) Value {
	return Value{Presence: NotModeled, Until: until, Why: why}
}

// Todo 构造一个「还没实现」的字段。
func Todo(why string) Value { return Value{Presence: NotImplemented, Why: why} }

// Validate 检查一个字段的声明是否完整。
func (v Value) Validate(name string) error {
	switch v.Presence {
	case Present:
		return nil
	case NotModeled:
		if v.Until == "" {
			return fmt.Errorf("字段 %s 声明为不建模，但没有到期版本 —— "+
				"不带到期版本的「不建模」是一句永久豁免", name)
		}
		if v.Why == "" {
			return fmt.Errorf("字段 %s 声明为不建模，但没写理由", name)
		}
		return nil
	case NotImplemented:
		if v.Why == "" {
			return fmt.Errorf("字段 %s 声明为还没实现，但没写是什么没实现", name)
		}
		return nil
	}
	return fmt.Errorf("字段 %s 的状态未声明 —— "+
		"⚠️ 零值不许当成「有值」：那会让忘记声明的字段全部变成 0，"+
		"而 0 正是最容易与柜台碰巧一致的那个数", name)
}

// Account 是账户截面视图，键用柜台（DIFF）的字段名。
//
// ⚠️ 键用柜台的叫法而不是本库的叫法，是为了让对拍不需要一层「翻译表」——
// 翻译表是又一个可能出错、且出错时不会有动静的地方。
type Account map[string]Value

// AccountInput 是渲染账户视图需要的、`account.Snapshot` 里**没有**的东西。
//
// ⚠️ 这个结构存在本身就是一条记录：本库的账户快照里缺 `float_profit`，
// 而它正是本项目核心那对区分（逐日盯市 / 逐笔对冲）的另一半。
// 不给就渲染成「还没实现」，**不是 0**。
type AccountInput struct {
	FloatProfit    decimal.Decimal
	HasFloatProfit bool
}

// AccountOf 渲染账户视图。
func AccountOf(s account.Snapshot, in AccountInput) (Account, error) {
	a := Account{
		// —— 本库算得出的 ——
		"pre_balance":       Num(s.PreBalance),
		"static_balance":    Num(s.StaticBalance),
		"balance":           Num(s.Balance),
		"available":         Num(s.Available),
		"deposit":           Num(s.Deposit),
		"withdraw":          Num(s.Withdraw),
		"close_profit":      Num(s.CloseProfit),
		"position_profit":   Num(s.PositionProfit),
		"commission":        Num(s.Commission),
		"margin":            Num(s.CurrMargin),
		"frozen_margin":     Num(s.FrozenMargin),
		"frozen_commission": Num(s.FrozenCommission),
		"frozen_premium":    Num(s.FrozenCash),

		// —— 期权相关，v1.0 明确不建模 ——
		"premium": Skip("v1.0.0", "期权权利金；v1.0 不含期权"),
		"market_value": Skip("v1.0.0",
			"期权市值；⚠️ 期货持仓上柜台返回的是字符串 \"-\" 而不是 0，见 probes.md"),
		"pre_option_market_value": Skip("v1.0.0", "上日期权市值；v1.0 不含期权"),

		// —— 快期特有的第二口径 ——
		//
		// ⚠️ 不建模是因为它是**另一个口子的口径**，不是一个规则。
		// 实测：有持仓、有浮亏时 balance == ctp_balance，差 0.000000；
		// 但样本里没有期权、没有昨仓、没有冻结，而那三样正是可能分岔的地方。
		"ctp_balance": Skip("v0.8.0",
			"快期自己的第二权益口径；本库只建模一套。差异本身要在 v0.8.0 测量并记录"),
		"ctp_available": Skip("v0.8.0", "同 ctp_balance"),

		// —— 还没实现 ——
		"currency": Todo("币种是字符串，本视图目前只承载数值字段"),
	}

	// ⚠️ risk_ratio 与它的「有没有」成对：结存非正时**没有**风险度。
	// 空账户不是「风险度 0%」，穿仓账户更不是 —— 都渲染成 0 的话，
	// **最危险的状态会看起来最安全**。
	if s.HasRiskRatio {
		a["risk_ratio"] = Num(s.RiskRatio)
	} else {
		a["risk_ratio"] = Todo("结存非正时没有风险度；本库以「无此值」表达，" +
			"而柜台在该状态下返回什么尚未实测")
	}

	// ⚠️ float_profit：本库的账户快照里没有它。
	if in.HasFloatProfit {
		a["float_profit"] = Num(in.FloatProfit)
	} else {
		a["float_profit"] = Todo("本库的 account.Snapshot 不承载浮动盈亏（逐笔对冲口径），" +
			"调用方需从 pnl 侧提供；⚠️ 它与 position_profit 正是本项目核心的那对区分")
	}

	for name, v := range a {
		if err := v.Validate(name); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// Fields 返回视图声明的全部字段名，升序。
func (a Account) Fields() []string {
	out := make([]string, 0, len(a))
	for k := range a {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CoverExactly 断言视图的字段集与给定集合**完全一致**：不多不少。
//
// ⚠️ 两个方向都要查，而且是两种不同的病：
//
//	少了  本库不知道柜台有这个字段 —— 对拍会漏掉它，且不会有动静
//	多了  本库渲染了一个柜台没有的字段 —— 说明字段名写错了，
//	      而写错的那个会永远「对不上」，看起来像一个真实的差异
func (a Account) CoverExactly(want []string) error {
	have := map[string]bool{}
	for _, k := range a.Fields() {
		have[k] = true
	}
	wantSet := map[string]bool{}
	for _, k := range want {
		wantSet[k] = true
	}
	var missing, extra []string
	for k := range wantSet {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	for k := range have {
		if !wantSet[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	return fmt.Errorf("字段集不一致：本库缺 %v；本库多出 %v —— "+
		"⚠️ 缺的那些对拍会整个漏掉且不会有动静；多的那些说明字段名写错了，"+
		"而写错的名字会永远「对不上」，看起来像一个真实的差异", missing, extra)
}
