package account

import (
	"strings"
	"testing"
)

// TestSettleUsesSettleCommission：盘中结存扣原样的费，结算时按结算口径的费（§13 #5，F10）。
func TestSettleUsesSettleCommission(t *testing.T) {
	a := newAcc(t, "1000")
	if err := a.AddCommission(d1, d("12.455"), d("12.45")); err != nil {
		t.Fatal(err)
	}
	if err := a.AddCommission(d1, d("12.449"), d("12.44")); err != nil {
		t.Fatal(err)
	}
	if got := a.Balance(); !got.Equal(d("975.096")) {
		t.Errorf("盘中结存 %s，期望 1000 − 24.904 = 975.096（盘中按原样）", got)
	}
	if err := a.Settle(d1, d2); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().PreBalance; !got.Equal(d("975.11")) {
		t.Errorf("⚠️ 结算后上日结存 %s，期望 1000 − 24.89 = 975.11（按结算口径）", got)
	}
	// 次日：两个累计都清零 —— 昨天的差额不许再还一次
	if err := a.Settle(d2, d2+1); err != nil {
		t.Fatal(err)
	}
	if got := a.Snapshot().PreBalance; !got.Equal(d("975.11")) {
		t.Errorf("⚠️ 空的一天结算后上日结存 %s，期望不变 975.11 —— 结算口径的累计没清零", got)
	}
}

// TestAddCommissionRefusesSettleOutsideFee：结算口径只会把一笔费变小；越界报错且账户不动。
func TestAddCommissionRefusesSettleOutsideFee(t *testing.T) {
	for _, c := range []struct{ fee, at string }{{"1.5", "1.51"}, {"1.5", "-0.01"}} {
		a := newAcc(t, "1000")
		err := a.AddCommission(d1, d(c.fee), d(c.at))
		if err == nil || !strings.Contains(err.Error(), "结算时计入的手续费") {
			t.Errorf("费 %s、结算口径 %s：应当报错，得到 %v", c.fee, c.at, err)
		}
		if st := a.State(); !st.Commission.IsZero() || !st.SettleCommission.IsZero() {
			t.Errorf("报错之后账户动了：%s / %s", st.Commission, st.SettleCommission)
		}
	}
}

// TestRestoreRefusesSettleCommissionAboveCommission：存档是可以手改的文件（design.md §6.5）。
func TestRestoreRefusesSettleCommissionAboveCommission(t *testing.T) {
	a := newAcc(t, "1000")
	if err := a.AddCommission(d1, d("3.7743"), d("3.76")); err != nil {
		t.Fatal(err)
	}
	st := a.State()
	if !st.SettleCommission.Equal(d("3.76")) {
		t.Fatalf("State 里的结算口径手续费 %s，期望 3.76", st.SettleCommission)
	}
	if b, err := Restore(st); err != nil || !b.State().SettleCommission.Equal(d("3.76")) {
		t.Fatalf("往返：%v", err)
	}
	for _, bad := range []string{"3.78", "-0.01"} {
		tampered := st
		tampered.SettleCommission = d(bad)
		if _, err := Restore(tampered); err == nil {
			t.Errorf("⚠️ 结算口径手续费 %s（盘中 3.7743）恢复成功了 —— 手改的存档没被拦下", bad)
		}
	}
}
