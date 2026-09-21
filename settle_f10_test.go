package futsim

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// agPair 在 SHFE.ag2702（按额 1e-05 × 乘数 15，按手 0）上开一手、平今一手，价由调用方给。
func agPair(t *testing.T, s *Simulator, open, close string) {
	t.Helper()
	mark(t, s, "SHFE.ag2702", "12580", "12570")
	if err := s.ApplyTrade(simDay, trade(t, "SHFE.ag2702", types.Buy, types.Open, open, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyTrade(simDay, trade(t, "SHFE.ag2702", types.Sell, types.CloseToday, close, 1)); err != nil {
		t.Fatal(err)
	}
}

func simWithRounding(t *testing.T, r fee.Rounding) *Simulator {
	t.Helper()
	c := ctpChoices()
	c.FeeRounding = r
	s, err := New(Config{Day: simDay, PreBalance: dec("100000"), Rules: simRules(t), Choices: c})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSettleRecomputesFeesPerTrade：盘中按 FeeRounding，结算时每笔按「四舍五入到分(按额部分) + 按手部分」重算（§13 #5，design.md 门面形状 §14）。
//
//	开 @12579：按额 1.88685    平今 @12583：按额 1.88745
//	不取整：盘中 3.7743，结算 1.89 + 1.89 = 3.78（进位：结算费大于盘中费）
func TestSettleRecomputesFeesPerTrade(t *testing.T) {
	s := simWithRounding(t, fee.NoRounding)
	agPair(t, s, "12579", "12583")
	a := s.Account()
	if !a.Commission.Equal(dec("3.7743")) || !a.Balance.Equal(dec("100056.2257")) {
		t.Fatalf("盘中 手续费 %s / 结存 %s，期望 3.7743 / 100056.2257（盘中按 FeeRounding = 不取整）", a.Commission, a.Balance)
	}
	if err := s.Settle(simDay, nil, simNext); err != nil {
		t.Fatal(err)
	}
	switch got := s.Account().PreBalance; {
	case got.Equal(dec("100056.22")):
	case got.Equal(dec("100056.24")):
		t.Errorf("⚠️ 上日结存 %s = 逐笔截断到分（旧的 (i-t)，被 0918 / 0921 两张结算单否掉）", got)
	case got.Equal(dec("100056.2257")):
		t.Errorf("⚠️ 上日结存 %s = 结算不重算（快期那一侧的口径）", got)
	default:
		t.Errorf("上日结存 %s，期望 100000 + 60 − 1.89 − 1.89 = 100056.22", got)
	}
}

// TestSettleToleranceTruncateExactlyOneCent：FeeRounding = 截断到分、按额部分第三位是 5 时，结算比盘中**恰好**多 0.01 —— 界是 ≤ 0.01，不许误拒。
//
//	开 @12570：按额 1.8855 ⇒ 盘中截断 1.88，结算四舍五入 1.89
func TestSettleToleranceTruncateExactlyOneCent(t *testing.T) {
	s := simWithRounding(t, fee.TruncateToCent)
	agPair(t, s, "12570", "12570")
	if got := s.Account().Commission; !got.Equal(dec("3.76")) {
		t.Fatalf("盘中截断到分：1.88 + 1.88 = 3.76，得到 %s", got)
	}
	if err := s.Settle(simDay, nil, simNext); err != nil {
		t.Fatal(err)
	}
	if got := s.Account().PreBalance; !got.Equal(dec("99996.22")) {
		t.Errorf("⚠️ 上日结存 %s，期望 100000 − 1.89 − 1.89 = 99996.22（每笔结算比盘中多恰好 0.01）", got)
	}
}

// TestSettleRecomputeIsIdentityUnderHalfUp：FeeRounding = 四舍五入到分时，结算费与盘中费逐笔相同。
func TestSettleRecomputeIsIdentityUnderHalfUp(t *testing.T) {
	s := simWithRounding(t, fee.HalfUpToCent)
	agPair(t, s, "12579", "12583")
	before := s.Account()
	if err := s.Settle(simDay, nil, simNext); err != nil {
		t.Fatal(err)
	}
	if got := s.Account().PreBalance; !got.Equal(before.Balance) {
		t.Errorf("⚠️ 四舍五入到分下结算后上日结存 %s ≠ 结算前结存 %s", got, before.Balance)
	}
}

// TestSettleKBlindSpotPinnedAtFive 钉住 k ∈ {1…4} 这个盲区里本库的选择（使用者 20260921 定 k = 5，双方各自确认）：
// 按额部分第三位是 4 时按 k = 5 **舍去**。将来实测到 k ≤ 4，这一格要红 —— 那时改口径，不是改这条测试。
//
//	开 @12560：按额 1.884 ⇒ k = 5 给 1.88；k ≤ 4 会给 1.89
func TestSettleKBlindSpotPinnedAtFive(t *testing.T) {
	s := simWithRounding(t, fee.NoRounding)
	agPair(t, s, "12560", "12560")
	if err := s.Settle(simDay, nil, simNext); err != nil {
		t.Fatal(err)
	}
	if got := s.Account().PreBalance; !got.Equal(dec("99996.24")) {
		t.Errorf("⚠️ 上日结存 %s，期望 100000 − 1.88 − 1.88 = 99996.24（k = 5：第三位 4 舍去）", got)
	}
}

// TestSettleCommissionSurvivesStateRestoreF10：盘中存档 → 恢复 → 结算，与不经存档的结算得同一个上日结存。
func TestSettleCommissionSurvivesStateRestoreF10(t *testing.T) {
	build := func() *Simulator {
		s, err := New(submitCfg(t, simDay))
		if err != nil {
			t.Fatal(err)
		}
		agPair(t, s, "12579", "12583")
		return s
	}
	direct := build()
	st, err := build().State()
	if err != nil {
		t.Fatal(err)
	}
	if !st.Account.SettleCommission.Equal(dec("3.78")) || st.Account.CommissionTrades != 2 {
		t.Fatalf("存档里的结算口径手续费 %s / 笔数 %d，期望 3.78 / 2", st.Account.SettleCommission, st.Account.CommissionTrades)
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
	if a, b := direct.Account().PreBalance, restored.Account().PreBalance; !a.Equal(b) || !a.Equal(decimal.RequireFromString("1000056.22")) {
		t.Errorf("⚠️ 直接结算 %s，经存档恢复后结算 %s，期望都是 1000056.22", a, b)
	}
}
