package main

import (
	"fmt"
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
	return indistinguishableAmong(settleRoundingCandidates, res)
}

// indistinguishableAmong 同 indistinguishable，候选名单由调用方给（F10 补测用 settleGranularityCandidates）。
func indistinguishableAmong(names []string, res map[string]decimal.Decimal) [][]string {
	groups := map[string][]string{}
	for _, name := range names {
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

// settleOrder 是 F10 补测里一张单的柜台手续费增量（订单前后账户 Commission 之差）。
type settleOrder struct {
	Fee    decimal.Decimal
	Volume int // 手数
	Trades int // 成交笔数（从落盘的成交明细数出来）
}

// settleGranularityCandidates 是 F10 补测的候选（事前登记见 design.md 门面形状 §14「前置条件：补测」，提交 520be1b）。
//
// ⚠️ 顺序与名字是登记的一部分，TestSettleGranularityCandidatesMatchRegistration 钉住。
var settleGranularityCandidates = []string{"iii", "i-t", "i-o", "i-l", "i-r", "i-T", "i-R"}

// settleGranularityResiduals 按 F10 补测的七个候选各自预言残差。纯函数。
//
//	i-t  每笔成交截断到分    Σ_成交 (费 − 截断(费))
//	i-o  每张单截断到分      Σ_单 (费 − 截断(费))
//	i-l  每手截断到分        Σ (费 − 手数 × 截断(费 ÷ 手数))
//
// ⚠️ 柜台增量是**按单**读的：一张单拆成多笔成交时逐笔的费观测不到，(i-t) 算不出 —— 报错，不猜怎么拆。
// 于是只要能算，(i-t) 与 (i-o) 必然同值；它们今天分不分得开，取决于有没有单拆成了多笔。
// ⚠️ (i-l) 的「每手费」定义为 费 ÷ 手数（0.005 常数项按笔还是按手收未定，§13 #19）—— 这个定义本身是一个假设。
func settleGranularityResiduals(orders []settleOrder) (map[string]decimal.Decimal, error) {
	var fees []decimal.Decimal
	perLot := decimal.Zero
	for i, o := range orders {
		if o.Volume <= 0 || o.Trades <= 0 {
			return nil, fmt.Errorf("第 %d 张单：手数 %d、成交 %d 笔 —— 缺数不判", i+1, o.Volume, o.Trades)
		}
		if o.Trades > 1 {
			return nil, fmt.Errorf("第 %d 张单拆成了 %d 笔成交 —— 逐笔的费观测不到，(i-t) 算不出；不猜怎么拆", i+1, o.Trades)
		}
		fees = append(fees, o.Fee)
		v := decimal.NewFromInt(int64(o.Volume))
		lot := o.Fee.Div(v).Truncate(2).Mul(v)
		perLot = perLot.Add(o.Fee.Sub(lot))
	}
	old := settleRoundingResiduals(fees)
	return map[string]decimal.Decimal{
		"iii": old["iii"], "i-t": old["i-t"], "i-o": old["i-t"], "i-l": perLot,
		"i-r": old["i-r"], "i-T": old["i-T"], "i-R": old["i-R"],
	}, nil
}
