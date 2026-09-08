package fixture

import (
	"fmt"
	"sort"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/view"
	"github.com/shopspring/decimal"
)

// ComparePosition 把本库的持仓视图与柜台的持仓截面变成一批可判定的字段。
//
// 映射规则，四种 presence 各有去处：
//
//	有值        → 数值比较
//	明确无值    → LibraryAbsent，与柜台的 "-" 相比
//	明确不建模  → NotModeledUntil，单独成档
//	还没实现    → ⚠️ **不给任何豁免**，值上必然对不上，判失败
//
// ⚠️ 最后一条是刻意的。把「还没实现」渲染成 0 再去比，
// 会在柜台那边恰好也是 0 时判成一致 —— 一个缺失的实现伪装成了一致。
// 那比算错更坏：算错会在某个样本上露出来，缺失永远不会。
//
// triggered 报告某个字段在本次样本里是否被真正触发过。
// ⚠️ 它必须由**造样本的人**给，不能由「值是不是零」推 ——
// 一个非零值也可能没被触发，而一个零值也可能是被触发之后的正确结果。
func ComparePosition(lib view.Position, oracle map[string]Value,
	triggered func(field string) bool) ([]conformance.Field, []error) {

	if triggered == nil {
		return nil, []error{fmt.Errorf("必须给出「这个字段被触发过没有」的判定 —— " +
			"由值推触发是本仓库明确禁止的：非零可能没触发，零可能是触发后的正确结果")}
	}
	var fields []conformance.Field
	var errs []error

	names := make([]string, 0, len(lib))
	for k := range lib {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		v := lib[name]
		o, ok := oracle[name]
		if !ok {
			// ⚠️ 本库渲染了柜台没有的字段 —— 字段名写错了。
			// 写错的名字会永远「对不上」，看起来像一个真实的差异。
			errs = append(errs, fmt.Errorf("本库渲染了字段 %s，而柜台截面里没有它 —— "+
				"字段名写错的话会永远对不上，看起来像一个真实的差异", name))
			continue
		}
		if o.IsText {
			// 字符串字段本视图不承载，跳过；完整性由 CoverExactly 另行保证。
			continue
		}
		f := conformance.Field{Name: name, Triggered: triggered(name)}
		f.Oracle, f.OracleAbsent = o.Number, o.Absent
		if o.Absent {
			f.Oracle = decimal.Zero
		}
		switch v.Presence {
		case view.Present:
			f.Library = v.Number
		case view.Absent:
			f.LibraryAbsent = true
		case view.NotModeled:
			f.NotModeledUntil = v.Until
		case view.NotImplemented:
			// ⚠️ 走专门的一档，**不比值**。
			//
			// 上一版这里写的是 `f.Library = decimal.Zero`，注释还说「两种情形都红」——
			// 那是错的：柜台那边恰好也是 0 时（空仓、无期权、无冻结时大量字段都是 0）
			// 它会判成一致。`volume_long_frozen_*` 当场就是这么假通过的。
			// **一个缺失的实现伪装成了一致**，而这正是 view 包整个存在的理由所要防的。
			f.LibraryUnimplemented = true
		default:
			errs = append(errs, fmt.Errorf("字段 %s 的状态未声明", name))
			continue
		}
		fields = append(fields, f)
	}

	// 反向：柜台有而本库没渲染的数值字段。
	for name, o := range oracle {
		if o.IsText {
			continue
		}
		if _, ok := lib[name]; !ok {
			errs = append(errs, fmt.Errorf("柜台有字段 %s 而本库没渲染 —— "+
				"⚠️ 对拍会整个漏掉它，且不会有任何动静", name))
		}
	}
	return fields, errs
}

// TriggeredByVolume 是一个**保守**的触发判定：该合约有持仓就算触发。
//
// ⚠️ 它是判定的**下界**而不是正解，用它必须清楚它承认什么：
// 有持仓时该合约的持仓字段确实被算过一遍，
// 但「被算过」不等于「这个字段的那条分支被走到过」——
// 例如昨仓为零时，`volume_long_his` 被算了，可它的非零分支没被考验过。
//
// 真正的触发判定要由造样本的人按实验目的给。本函数只用于
// 「先把整批字段跑起来看看形状」，**不作为验收依据**。
func TriggeredByVolume(oracle map[string]Value) func(string) bool {
	long := oracle["volume_long"]
	short := oracle["volume_short"]
	has := (!long.Absent && long.Number.IsPositive()) ||
		(!short.Absent && short.Number.IsPositive())
	return func(string) bool { return has }
}
