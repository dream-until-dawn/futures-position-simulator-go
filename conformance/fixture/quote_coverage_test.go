package fixture

import (
	"sort"
	"testing"
)

// quotelessRatchet 是**当前**缺行情的夹具份数。这个数只许减少。
//
// ⚠️ 它不是「可以接受 68 份」，是「已经有 69 份，别再多」。
const quotelessRatchet = 69

// TestFixtureQuotesCoverOrderedSymbols 断言夹具里出现过的合约都带着行情。
//
// # 它抓的是一种**静默跳过**
//
// 缺了行情，下游对拍不会报错，只会「本份跳过」——
// ⚠️ 而「跳过了一份」与「比过了一份且一致」在汇总行里长得一模一样。
//
// # 这条守卫是被一次真实的丢失逼出来的
//
// 20260909 的 reject 系列实验形状是「只下单、全被拒、没有持仓也没有成交」，
// 而落盘时挑行情的判据（observedSymbols）只看**持仓与成交**，不看委托 ——
// 于是 exp-reject-tick-vs-limit-20260909-3.json 的行情段整个是空的，
// 它因此**永远**比不了金额侧冻结（缺 DCE.i2701 的昨结算价）。
// 判据已经补上委托那一支，同一实验的 -4 是修好之后重跑的，那一份有行情。
//
// # 为什么是棘轮而不是硬断言
//
// 20260907–08 的夹具**根本没有行情段**（行情是 09-08 下午才进夹具的），
// 09-09 修好之前落的那批也缺。这些都不重制 —— 它们记的是那一刻的账户，
// 重制出来的是另一个时刻。于是这里钉住份数：只许减少。
//
// ⚠️ 这个棘轮有个洞，写出来：**删掉一份旧的、加进一份新的坏的，数不变。**
// 补不上的原因是「哪些算旧」没有机械判据（修复发生在 09-09 当天，
// 同一天里有好有坏）。真要堵，得在夹具里记下落盘工具的版本 ——
// 那是另一件事。
func TestFixtureQuotesCoverOrderedSymbols(t *testing.T) {
	checked, lacking := 0, 0
	for _, f := range loadAll(t) {
		want := map[string]bool{}
		for sym := range f.Positions {
			want[sym] = true
		}
		for _, tr := range f.Trades {
			want[tr.Instrument.Native()] = true
		}
		for _, o := range f.Orders {
			if sym, ok := textOf(o, "exchange_id", "instrument_id"); ok {
				want[sym] = true
			}
		}
		var lack []string
		for sym := range want {
			if _, ok := f.Quotes[sym]; !ok {
				lack = append(lack, sym)
			}
		}
		checked++
		if len(lack) == 0 {
			continue
		}
		lacking++
		sort.Strings(lack)
		t.Logf("ⓘ %s 缺 %v 的行情", f.Path, lack)
	}
	t.Logf("查了 %d 份夹具，缺行情的 %d 份（棘轮 %d）", checked, lacking, quotelessRatchet)
	switch {
	case lacking > quotelessRatchet:
		t.Errorf("⚠️ 缺行情的夹具从 %d 份涨到了 %d 份 —— "+
			"新落的夹具又丢了行情。下游对拍会**静默跳过**它，"+
			"而跳过与「比过且一致」长得一模一样。先查 observedSymbols "+
			"是不是又漏了一类合约来源", quotelessRatchet, lacking)
	case lacking < quotelessRatchet:
		t.Errorf("ⓘ 缺行情的夹具降到了 %d 份（棘轮 %d）—— 好消息，"+
			"把 quotelessRatchet 改成 %d 钉住它，否则退化不会红",
			lacking, quotelessRatchet, lacking)
	}
	// ⚠️ 判别力：一份都不缺时，上面两支都不走，而这条测试会绿得毫无内容。
	if checked == 0 {
		t.Fatal("⚠️ 一份夹具都没读到 —— 这条守卫什么都没查")
	}
}
