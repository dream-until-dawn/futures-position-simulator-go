package fixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	p, err := Carry(f, sym, types.Speculation, settle, next)
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
	p, err := Carry(f, "SHFE.rb2701", types.Speculation, same, types.NewTradingDay(2026, 9, 9))
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
	_, err := Carry(f, "SHFE.zzz2701", types.Speculation, settle, next)
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
	_, err = Carry(f, flat, types.Speculation, settle, next)
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
	_, err := Carry(f, "SHFE.rb2701", types.Speculation,
		decimal.Zero, types.NewTradingDay(2026, 9, 9))
	if err == nil {
		t.Fatal("⚠️ 结算价为 0 应当报错 —— 0 只可能是缺失的伪装")
	}
}
