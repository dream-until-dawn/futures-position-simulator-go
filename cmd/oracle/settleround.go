package main

import (
	"sort"

	"github.com/shopspring/decimal"
)

// settleRoundingCandidates 是 §13 #5「结算时点是否重新取整手续费」的五个候选（事前登记见 state.md 与提交 309922a）。
//
// ⚠️ 顺序与名字是登记的一部分，TestSettleRoundingCandidatesMatchPreRegistration 钉住。
var settleRoundingCandidates = []string{"iii", "i-t", "i-r", "i-T", "i-R"}

// settleRoundingResiduals 按五个候选各自预言「次日 PreBalance − 不重新取整时的推算」这个残差。纯函数。
//
// 残差 = 当日累计手续费 − 结算时实收手续费（实收少了，结存就多出这么多）。
//
//	iii  不重新取整          0
//	i-t  逐笔截断到分        Σ(费_i − 截断到分(费_i))
//	i-r  逐笔四舍五入到分    Σ(费_i − 四舍五入到分(费_i))
//	i-T  合计截断到分        合计 − 截断到分(合计)
//	i-R  合计四舍五入到分    合计 − 四舍五入到分(合计)
//
// ⚠️ 截断指「朝零」：手续费恒非负，朝零与朝负无穷相同。
// ⚠️ 四舍五入指「逢五进一」（decimal.Round 的半远离零）；银行家舍入没有列为候选 —— 列出来它会与 i-r 在
// 「第三位恰好是 5 且第二位是偶数」之外处处同值，登记时没有写它，事后不补。
func settleRoundingResiduals(fees []decimal.Decimal) map[string]decimal.Decimal {
	total := decimal.Zero
	perTrunc, perRound := decimal.Zero, decimal.Zero
	for _, f := range fees {
		total = total.Add(f)
		perTrunc = perTrunc.Add(f.Sub(f.Truncate(2)))
		perRound = perRound.Add(f.Sub(f.Round(2)))
	}
	return map[string]decimal.Decimal{
		"iii": decimal.Zero,
		"i-t": perTrunc,
		"i-r": perRound,
		"i-T": total.Sub(total.Truncate(2)),
		"i-R": total.Sub(total.Round(2)),
	}
}

// indistinguishable 列出预言值相同、今晚分不开的候选组（每组至少两个）。纯函数。
//
// ⚠️ 登记里写了「两个同值就在提交时说出来，不等看到结果」—— 这个函数就是说出来的那一步。
func indistinguishable(res map[string]decimal.Decimal) [][]string {
	groups := map[string][]string{}
	for _, name := range settleRoundingCandidates {
		k := res[name].String()
		groups[k] = append(groups[k], name)
	}
	var out [][]string
	for _, g := range groups {
		if len(g) > 1 {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}
