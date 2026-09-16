package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func loadOne(t *testing.T, name string) *Fixture {
	t.Helper()
	f, err := os.Open(filepath.Join(probesDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fx, err := Load(f, name)
	if err != nil {
		t.Fatal(err)
	}
	return fx
}

// TestSplitSaysSameWhenBaselinesCoincide 是 TestReconstructOnFacadeSplitsTheTwoBaselines 的判别力守卫。
//
// ⚠️ 结算价恰好等于开仓均价时两条基线仍然相等，那时的样本什么都验证不了。
// Split 必须把这件事说出来，而不是让它悄悄通过。F7b 之前它跑在 Carry 的结果上，Carry 删了，改在门面结转的结果上跑。
func TestSplitSaysSameWhenBaselinesCoincide(t *testing.T) {
	f := loadOne(t, "status-20260908-8.json")
	const sym = "SHFE.rb2701"
	spec, ok := specForSymbol(t, f, sym)
	if !ok {
		t.Fatal("rb2701 没有登记规格")
	}
	next := types.NewTradingDay(2026, 9, 9)
	// 先随便结一次，取出逐笔对冲基线；再拿它本身当结算价
	r, err := ReconstructOnFacade(f, nil, sym, spec, refdata.UseHistory, decimal.RequireFromString("3160"), next)
	if err != nil {
		t.Fatal(err)
	}
	openAvg, _, _, err := Split(r.Position, types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	r, err = ReconstructOnFacade(f, nil, sym, spec, refdata.UseHistory, openAvg, next)
	if err != nil {
		t.Fatal(err)
	}
	_, basisAvg, differs, err := Split(r.Position, types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	if differs {
		t.Errorf("⚠️ 结算价等于开仓均价（%s）时两条基线应当相同，Split 却说不同（逐日盯市基线 %s）—— "+
			"那说明判据在看别的东西", openAvg, basisAvg)
	}
}

// TestTonightSeedDiscriminatingPower 把今晚两个种子的**判别力**钉在测试里。
//
// ⚠️ 这不是「测代码」，是把一个已经**观测到**的事实固定下来，
// 免得今晚看到 m2701 的两条基线相等时以为是本库算错了。
//
// 交易日 20260908 的今结算价（15:00:02 与 15:03:47 分别到达，probes.md §7.5）：
//
//	SHFE.rb2701  3 手 @{3151,3151,3171}  均价 3157.666…  今结算 3163   → 两条基线**不同** ✓
//	DCE.m2701    3 手 @{3411,3411,3423}  均价 3415       今结算 3415   → 两条基线**相同** ⚠️
//
// ⚠️ m2701 那条是巧合：`(3411+3411+3423)/3` 恰好等于 3415。
// 于是它今晚**对「逐日盯市 vs 逐笔对冲」这条区分没有判别力** ——
// 而这正是 Split 那条守卫写下来时假设的情形，它现在成真了。
//
// 幸好种子有两个：**rb2701 保住了这条判别力**。
// 若当初只播一个 m2701，今晚整批会在一个什么都验证不了的样本上跑完，
// 而且**全绿**。
func TestTonightSeedDiscriminatingPower(t *testing.T) {
	cases := []struct {
		sym        string
		lots       []string
		settlement string
		wantSplit  bool // 结算后两条基线是否不同
	}{
		{"SHFE.rb2701", []string{"3151", "3151", "3171"}, "3163", true},
		{"DCE.m2701", []string{"3411", "3411", "3423"}, "3415", false},
	}
	inst := map[string]types.InstrumentID{
		"SHFE.rb2701": {Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1},
		"DCE.m2701":   {Exchange: types.DCE, Product: "m", Year: 2027, Month: 1},
	}
	next := types.NewTradingDay(2026, 9, 9)
	day := types.NewTradingDay(2026, 9, 8)

	split := 0
	for _, c := range cases {
		p, err := position.New(inst[c.sym], types.Speculation, day, refdata.UseHistory)
		if err != nil {
			t.Fatal(err)
		}
		for _, px := range c.lots {
			if err := p.Open(types.Buy, day, decimal.RequireFromString(px), 1); err != nil {
				t.Fatal(err)
			}
		}
		if err := p.Settle(day, decimal.RequireFromString(c.settlement), next); err != nil {
			t.Fatal(err)
		}
		openAvg, basisAvg, differs, err := Split(p, types.Buy)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%-12s 逐笔对冲基线 %-26s 逐日盯市基线 %-8s 分开了：%v",
			c.sym, openAvg, basisAvg, differs)
		if differs != c.wantSplit {
			t.Errorf("⚠️ %s：两条基线是否分开 = %v，实测应为 %v —— "+
				"⚠️ 若这条红了，说明结算价或种子明细与记录不符，**先查那个**，"+
				"不要改这里的期望值", c.sym, differs, c.wantSplit)
		}
		if differs {
			split++
		}
	}
	// ⚠️ 至少要有一个种子保住判别力，否则今晚整批什么都验证不了 —— 而且会全绿。
	if split == 0 {
		t.Fatal("⚠️ **两个种子的两条基线都相等** —— " +
			"今晚对「逐日盯市 vs 逐笔对冲」这条区分完全没有判别力。" +
			"这不是本库的问题，是样本的问题：要在结算前补一手把均价挪开")
	}
	t.Logf("ⓘ %d/%d 个种子保住了两条基线的区分 —— "+
		"⚠️ m2701 那个是巧合失效（均价恰好等于结算价），不是本库算错", split, len(cases))
}
