package fixture

import (
	"os"
	"path/filepath"
	"strings"
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

// TestCarrySplitsTheTwoBaselines 是本项目最核心那条区分的**第一次**可观测样本。
//
// 在此之前，全部实测样本上 open_price 与 position_price **恒等** ——
// 今仓下两条基线本来就相同，所以那条区分从未被验证过。
// 结算把它们分开：
//
//	OpenPrice  停在原始成交价的加权均价 —— 逐笔对冲，永不改变
//	Basis      被重置成结算价           —— 逐日盯市
//
// ⚠️ 本测试用的是**日盘那份真实夹具**（3 手 @{3151,3151,3171}），
// 结算价用一个与均价明显不同的数 —— 今晚换成交易所给的真值。
func TestCarrySplitsTheTwoBaselines(t *testing.T) {
	f := loadOne(t, "avg-price-20260908.json")
	const sym = "SHFE.rb2701"
	next := types.NewTradingDay(2026, 9, 9)
	settle := decimal.RequireFromString("3160")

	p, err := Carry(f, sym, types.Speculation, positionDateOf(t, sym), settle, next)
	if err != nil {
		t.Fatal(err)
	}
	if p.Day != next {
		t.Errorf("结转之后应停在交易日 %s，实为 %s", next, p.Day)
	}
	s, err := p.Side(types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	// 全部变昨仓。
	if got := s.VolumeHistory(); got != 3 {
		t.Errorf("⚠️ 结算之后应有 3 手昨仓，得到 %d", got)
	}
	if got := s.VolumeToday(); got != 0 {
		t.Errorf("⚠️ 结算之后不该还有今仓，得到 %d 手 —— "+
			"「今昨仓不滚」是 silent-risks.md 第 1 条，全程不报错", got)
	}

	openAvg, basisAvg, differs, err := Split(p, types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("逐笔对冲基线 %s，逐日盯市基线 %s", openAvg, basisAvg)

	// 逐笔对冲基线 = (3151×2 + 3171)/3 = 3157.666…，**不受结算影响**。
	wantOpen := decimal.RequireFromString("3157.6666666666666667")
	if !openAvg.Equal(wantOpen) {
		t.Errorf("⚠️ 逐笔对冲基线应为 %s（不受结算影响），得到 %s —— "+
			"结算动了 OpenPrice 的话就再也算不回逐笔对冲口径了", wantOpen, openAvg)
	}
	if !basisAvg.Equal(settle) {
		t.Errorf("⚠️ 逐日盯市基线应被重置成结算价 %s，得到 %s", settle, basisAvg)
	}
	if !differs {
		t.Fatalf("⚠️ 两条基线仍然相等（%s）—— 本样本对「逐日盯市 vs 逐笔对冲」"+
			"这条区分**没有判别力**，而对拍会照样全绿", openAvg)
	}
}

// TestCarryRefusesWhenBaselinesWouldCoincide 是上一条的判别力守卫。
//
// ⚠️ 结算价恰好等于开仓均价时两条基线仍然相等，那时的样本什么都验证不了。
// Split 必须把这件事说出来，而不是让它悄悄通过。
func TestCarryRefusesWhenBaselinesWouldCoincide(t *testing.T) {
	f := loadOne(t, "avg-price-20260908.json")
	// 结算价取成开仓均价本身。
	same := decimal.RequireFromString("3157.6666666666666667")
	p, err := Carry(f, "SHFE.rb2701", types.Speculation, refdata.UseHistory, same, types.NewTradingDay(2026, 9, 9))
	if err != nil {
		t.Fatal(err)
	}
	_, _, differs, err := Split(p, types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	if differs {
		t.Error("⚠️ 结算价等于开仓均价时两条基线应当相同，Split 却说不同 —— " +
			"那说明判据在看别的东西")
	}
}

// TestCarryRefusesFlatAndMissing 断言两种「结转了个寂寞」的情形都报错。
func TestCarryRefusesFlatAndMissing(t *testing.T) {
	f := loadOne(t, "avg-price-20260908.json")
	next := types.NewTradingDay(2026, 9, 9)
	settle := decimal.RequireFromString("3160")

	// ① 夹具里没有这个合约的成交。
	_, err := Carry(f, "SHFE.zzz2701", types.Speculation, refdata.UseHistory, settle, next)
	if err == nil || !strings.Contains(err.Error(), "一笔成交都没有") {
		t.Errorf("⚠️ 结转一个没有成交记录的合约应当报错，得到 %v —— "+
			"空仓与「有仓但没记录」在结果上长得一样", err)
	}

	// ② 有成交但收盘是空仓（当天开了又平光）。
	flat := ""
	for _, sym := range f.Symbols() {
		if len(f.TradesOf(sym)) == 0 {
			continue
		}
		v := f.Positions[sym]["volume_long"]
		w := f.Positions[sym]["volume_short"]
		if !v.Absent && v.Number.IsZero() && !w.Absent && w.Number.IsZero() {
			flat = sym
			break
		}
	}
	if flat == "" {
		t.Fatal("⚠️ 夹具里找不到「有成交但收盘空仓」的合约 —— " +
			"这一分支没被考验过，而它是最像正常结果的那种失败")
	}
	_, err = Carry(f, flat, types.Speculation, refdata.UseHistory, settle, next)
	if err == nil || !strings.Contains(err.Error(), "空仓") {
		t.Errorf("⚠️ 结转一个空仓合约（%s）应当报错，得到 %v", flat, err)
	}
}

// TestCarryRefusesZeroSettlement 断言结算价为零被拒。
//
// ⚠️ 「没有任何品种的结算价会是零，0 只可能是缺失的伪装」——
// 免费日线源上有 7.7%～98.6% 的结算价字段是 0（design.md §6.5）。
func TestCarryRefusesZeroSettlement(t *testing.T) {
	f := loadOne(t, "avg-price-20260908.json")
	_, err := Carry(f, "SHFE.rb2701", types.Speculation, refdata.UseHistory,
		decimal.Zero, types.NewTradingDay(2026, 9, 9))
	if err == nil {
		t.Fatal("⚠️ 结算价为 0 应当报错 —— 0 只可能是缺失的伪装")
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
