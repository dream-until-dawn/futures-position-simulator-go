package futsim

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// agRoundTrip 在 SHFE.ag2702 上开一手、平今一手：按额 1e-05 × 乘数 15，两笔费 1.88685 / 1.88745 各带五位小数。
//
//	逐笔截断 1.88 + 1.88 = 3.76    合计截断 trunc(3.7743) = 3.77    不取整 3.7743
//
// ⇒ 三种结算口径在这一对成交上两两不同（design.md 门面形状 §14，F10）。
func agRoundTrip(t *testing.T, s *Simulator) {
	t.Helper()
	mark(t, s, "SHFE.ag2702", "12580", "12570")
	if err := s.ApplyTrade(simDay, trade(t, "SHFE.ag2702", types.Buy, types.Open, "12579", 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyTrade(simDay, trade(t, "SHFE.ag2702", types.Sell, types.CloseToday, "12583", 1)); err != nil {
		t.Fatal(err)
	}
}

// TestSettleTruncatesFeesPerTrade：盘中按原样累加，结算时逐笔截断到分（§13 #5，CTP 实测 (i-t)）。
func TestSettleTruncatesFeesPerTrade(t *testing.T) {
	s := newSim(t)
	agRoundTrip(t, s)
	// 盘中：平仓盈亏 (12583−12579)×15 = 60；手续费原样 3.7743 ⇒ 结存 100056.2257
	a := s.Account()
	if !a.Commission.Equal(dec("3.7743")) || !a.Balance.Equal(dec("100056.2257")) {
		t.Fatalf("盘中 手续费 %s / 结存 %s，期望 3.7743 / 100056.2257（盘中不取整）", a.Commission, a.Balance)
	}
	if err := s.Settle(simDay, nil, simNext); err != nil {
		t.Fatal(err)
	}
	got := s.Account().PreBalance
	switch {
	case got.Equal(dec("100056.24")):
	case got.Equal(dec("100056.23")):
		t.Errorf("⚠️ 上日结存 %s = 合计截断（3.77）—— 要逐笔截断（3.76）：两者在 20260917 的十笔上差 0.020", got)
	case got.Equal(dec("100056.2257")):
		t.Errorf("⚠️ 上日结存 %s = 不取整 —— 结算没有按 CTP 逐笔截断到分（§13 #5）", got)
	default:
		t.Errorf("上日结存 %s，期望 100056.24（100000 + 60 − 1.88 − 1.88）", got)
	}
}

// TestSettleCommissionSurvivesStateRestore：盘中存档 → 恢复 → 结算，与不经存档的结算得同一个上日结存。
// ⚠️ 结算口径的累计只在这里被存档路径碰到：它丢了，恢复出的账户会按「整笔都不计」结算，而盘中一切读数都对。
func TestSettleCommissionSurvivesStateRestore(t *testing.T) {
	direct, err := New(submitCfg(t, simDay))
	if err != nil {
		t.Fatal(err)
	}
	agRoundTrip(t, direct)
	via, err := New(submitCfg(t, simDay))
	if err != nil {
		t.Fatal(err)
	}
	agRoundTrip(t, via)
	st, err := via.State()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(submitCfg(t, simDay), roundTrip(t, st))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Simulator{direct, restored} {
		if err := s.Settle(simDay, nil, simNext); err != nil {
			t.Fatal(err)
		}
	}
	if a, b := direct.Account().PreBalance, restored.Account().PreBalance; !a.Equal(b) {
		t.Errorf("⚠️ 经存档恢复后结算得 %s，直接结算得 %s —— 结算口径的手续费没进存档", b, a)
	}
	if !direct.Account().PreBalance.Equal(dec("1000056.24")) {
		t.Errorf("直接结算得 %s，期望 1000056.24", direct.Account().PreBalance)
	}
}

// TestSettleTruncationIsIdentityOnCentFees：盘中已取整到分（FeeRounding = 四舍五入到分）时，结算截断不改变任何数 ——
// 与 Choices.FeeRounding 组合不出矛盾（design.md 门面形状 §14）。
func TestSettleTruncationIsIdentityOnCentFees(t *testing.T) {
	c := ctpChoices()
	c.FeeRounding = fee.HalfUpToCent
	s, err := New(Config{Day: simDay, PreBalance: dec("100000"), Rules: simRules(t), Choices: c})
	if err != nil {
		t.Fatal(err)
	}
	agRoundTrip(t, s)
	before := s.Account()
	if !before.Commission.Equal(dec("3.78")) {
		t.Fatalf("盘中四舍五入到分：1.89 + 1.89 = 3.78，得到 %s", before.Commission)
	}
	if err := s.Settle(simDay, nil, simNext); err != nil {
		t.Fatal(err)
	}
	if got := s.Account().PreBalance; !got.Equal(before.Balance) {
		t.Errorf("⚠️ 盘中费已是整分，结算后上日结存 %s ≠ 结算前结存 %s —— 截断不该再动它", got, before.Balance)
	}
}
