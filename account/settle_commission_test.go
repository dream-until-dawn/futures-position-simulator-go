package account

import (
	"strings"
	"testing"
)

// TestSettleUsesSettleCommission：盘中结存扣原样的费，结算时按结算口径的费（§13 #5，F10：结算时按笔重算，可以进位）。
func TestSettleUsesSettleCommission(t *testing.T) {
	a := newAcc(t, "1000")
	if err := a.AddCommission(d1, d("23.568"), d("23.57")); err != nil { // 进位：结算费大于盘中费
		t.Fatal(err)
	}
	if err := a.AddCommission(d1, d("23.556"), d("23.56")); err != nil {
		t.Fatal(err)
	}
	if got := a.Balance(); !got.Equal(d("952.876")) {
		t.Errorf("盘中结存 %s，期望 1000 − 47.124 = 952.876（盘中按原样）", got)
	}
	if err := a.Settle(d1, d2); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().PreBalance; !got.Equal(d("952.87")) {
		t.Errorf("⚠️ 结算后上日结存 %s，期望 1000 − 47.13 = 952.87（按结算口径）", got)
	}
	// 次日：累计清零 —— 昨天的差额不许再算一次
	if err := a.Settle(d2, d2+1); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().PreBalance; !got.Equal(d("952.87")) {
		t.Errorf("⚠️ 空的一天结算后上日结存 %s，期望不变 952.87 —— 结算口径的累计没清零", got)
	}
}

// TestAddCommissionTolerance：每笔 |结算费 − 盘中费| ≤ 0.01、结算费 ≥ 0；恰好 0.01 放行（截断到分口径能取到），超过报错且账户不动。
func TestAddCommissionTolerance(t *testing.T) {
	a := newAcc(t, "1000")
	if err := a.AddCommission(d1, d("35.68"), d("35.69")); err != nil {
		t.Errorf("⚠️ 差恰好 0.01（截断到分口径下按额 35.685 那种）应当放行：%v", err)
	}
	for _, c := range []struct{ fee, at string }{{"1.5", "1.52"}, {"1.5", "1.48"}, {"0.004", "-0.001"}} {
		b := newAcc(t, "1000")
		err := b.AddCommission(d1, d(c.fee), d(c.at))
		if err == nil || !strings.Contains(err.Error(), "结算时计入的手续费") {
			t.Errorf("盘中 %s、结算 %s：应当报错，得到 %v", c.fee, c.at, err)
		}
		if st := b.State(); !st.Commission.IsZero() || !st.SettleCommission.IsZero() || st.CommissionTrades != 0 {
			t.Errorf("报错之后账户动了：%+v", st)
		}
	}
}

// TestRestoreRefusesSettleCommissionOutsideTolerance：存档是可以手改的文件（design.md §6.5）——累计差不许超过「笔数 × 0.01」。
func TestRestoreRefusesSettleCommissionOutsideTolerance(t *testing.T) {
	a := newAcc(t, "1000")
	for _, p := range [][2]string{{"23.568", "23.57"}, {"23.556", "23.56"}} {
		if err := a.AddCommission(d1, d(p[0]), d(p[1])); err != nil {
			t.Fatal(err)
		}
	}
	st := a.State()
	if !st.SettleCommission.Equal(d("47.13")) || st.CommissionTrades != 2 {
		t.Fatalf("State 里的结算口径手续费 %s / 笔数 %d，期望 47.13 / 2", st.SettleCommission, st.CommissionTrades)
	}
	if b, err := Restore(st); err != nil || !b.State().SettleCommission.Equal(d("47.13")) || b.State().CommissionTrades != 2 {
		t.Fatalf("往返：%v", err)
	}
	for _, c := range []struct {
		name string
		edit func(*State)
	}{
		{"结算口径手续费改大到超出 2 笔 × 0.01", func(s *State) { s.SettleCommission = d("47.15") }},
		{"结算口径手续费为负", func(s *State) { s.SettleCommission = d("-0.01") }},
		{"笔数改成 0（界收窄到 0）", func(s *State) { s.CommissionTrades = 0 }},
	} {
		tampered := st
		c.edit(&tampered)
		if _, err := Restore(tampered); err == nil {
			t.Errorf("⚠️ %s：恢复成功了 —— 手改的存档没被拦下", c.name)
		}
	}
}
