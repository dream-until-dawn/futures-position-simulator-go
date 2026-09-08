package fixture

import (
	"strings"
	"testing"
)

// TestCloseOrderIsStructurallyUnmeasurable 断言**平仓消耗顺序在这个口子上测不出来**，
// 而且给出的是**结构性理由**，不是「这批样本恰好没撞上」。
//
// # 两条腿，缺一条结论就不成立
//
//	NoUseHistory（DCE）  持仓**从来没有昨仓** —— 没有今昨可选
//	UseHistory（SHFE）   CLOSETODAY 冻/平今、CLOSE 冻/平昨，**开平标志已经指定了哪一边**
//	                     （kq_facts 32，20260909 在 今1/昨3 的同一截面上量出来）
//
// 两条腿合起来：柜台**从来不需要在今昨之间做选择**。
// 于是 `position.CloseOrder` 那三个取值在这个口子上永远给出同一个结果 ——
// 不是「测了发现一样」，是「根本没有可测的场合」。
//
// ⚠️ 这与 kq_facts 5（单向大边在此口子上未启用）同形：
// 结论是**要换口子**，不是「再多跑几次实验」。
//
// # ⚠️ 一条不能拿来当证据的东西
//
// `TestReplayIsUnambiguous` 报「有歧义 0 个」，看起来像独立佐证 —— **它不是**。
// 那个 0 的成因是重放里根本没有昨仓（夹具只含当日成交，昨仓那几手的开仓单
// 在前一交易日的夹具里），与本条的结构性理由无关。
// 拿它来加强本条，等于把一个「样本缺失」读成「性质如此」。
func TestCloseOrderIsStructurallyUnmeasurable(t *testing.T) {
	all := loadAll(t)
	sections, dceSections, dceWithHistory := 0, 0, 0
	var offenders []string

	for _, f := range all {
		for sym, pos := range f.Positions {
			sections++
			if !strings.HasPrefix(sym, "DCE.") {
				continue
			}
			dceSections++
			for _, side := range []string{"long", "short"} {
				h, ok := numberOf(pos, "volume_"+side+"_his")
				if !ok {
					continue // ⚠️ 读不到不当成零
				}
				if h.IsPositive() {
					dceWithHistory++
					offenders = append(offenders,
						f.Path+" "+sym+"."+side+"="+h.String())
				}
			}
		}
	}

	// ⚠️ 判别力：大商所的样本得够多，否则「从来没有昨仓」是因为压根没样本。
	if dceSections < 50 {
		t.Fatalf("⚠️ 只有 %d 个大商所持仓截面 —— 「从来没有昨仓」此时说明不了什么", dceSections)
	}

	if dceWithHistory > 0 {
		// ⚠️ 这是**好消息**：大商所出现昨仓了，消耗顺序第一次有可测的场合。
		// 但好消息同样需要有人被通知到，而且清单要跟着改。
		t.Errorf("⚠️ 大商所出现了 %d 个**有昨仓**的方向 —— "+
			"本条此前的结论是「NoUseHistory 上从来没有昨仓，因此没有今昨可选」。"+
			"⚠️ 这是好消息：消耗顺序第一次有了可测的场合。"+
			"去跑 close-order 实验，并把 rules_pending 里那条从「测不出来」改回「待测」。"+
			"样本：%v", dceWithHistory, offenders)
	}

	t.Logf("%d 个持仓截面，其中大商所 %d 个，**有昨仓的 %d 个**",
		sections, dceSections, dceWithHistory)
	t.Log("ⓘ 结论：平仓消耗顺序在这个口子上**结构性地测不出来** —— " +
		"NoUseHistory 没有昨仓可选，UseHistory 的开平标志已经指定了哪一边（kq_facts 32）。" +
		"与 kq_facts 5（单向大边未启用）同形：要换口子，不是再多跑几次实验。")
}
