package account

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

const (
	d1 = types.TradingDay(20260907)
	d2 = types.TradingDay(20260908)
)

func newAcc(t *testing.T, pre string) *Account {
	t.Helper()
	a, err := New("CNY", d1, d(pre))
	if err != nil {
		t.Fatalf("开户失败: %v", err)
	}
	return a
}

// mustCheck 在每次状态变更后断言不变量。
func mustCheck(t *testing.T, a *Account, where string) {
	t.Helper()
	if err := a.Check(); err != nil {
		t.Fatalf("%s 之后不变量破裂: %v", where, err)
	}
}

// TestBalanceChain 覆盖结存链条的每一项。
func TestBalanceChain(t *testing.T) {
	a := newAcc(t, "1000000")
	steps := []struct {
		name        string
		do          func() error
		wantBalance string
	}{
		{"起点", func() error { return nil }, "1000000"},
		{"入金 50000", func() error { return a.Deposit(d1, d("50000")) }, "1050000"},
		{"出金 20000", func() error { return a.Withdraw(d1, d("20000")) }, "1030000"},
		{"平仓盈亏 +3000", func() error { return a.AddCloseProfit(d1, d("3000")) }, "1033000"},
		{"平仓盈亏 −800", func() error { return a.AddCloseProfit(d1, d("-800")) }, "1032200"},
		{"手续费 120", func() error { return a.AddCommission(d1, d("120")) }, "1032080"},
		{"持仓盈亏 +5000", func() error { return a.SetPositionProfit(d1, d("5000")) }, "1037080"},
		{"持仓盈亏改为 −2000", func() error { return a.SetPositionProfit(d1, d("-2000")) }, "1030080"},
	}
	if len(steps) != 8 {
		t.Fatalf("步骤数应为 8，实际 %d", len(steps))
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("%s 失败: %v", s.name, err)
		}
		if got := a.Balance(); !got.Equal(d(s.wantBalance)) {
			t.Errorf("%s 之后结存 = %s，期望 %s", s.name, got, s.wantBalance)
		}
		mustCheck(t, a, s.name)
	}
	// 静态权益不含任何盈亏。
	if got := a.StaticBalance(); !got.Equal(d("1030000")) {
		t.Errorf("静态权益 = %s，期望 1030000（只含出入金）", got)
	}
}

// TestPositionProfitOverwritesNotAccumulates 断言持仓盈亏是覆盖不是累加。
//
// ⚠️ 写成累加会让它随每次刷新无界增长，**而增长得很像盈利**。
func TestPositionProfitOverwritesNotAccumulates(t *testing.T) {
	a := newAcc(t, "1000000")
	for i := 0; i < 5; i++ {
		if err := a.SetPositionProfit(d1, d("3000")); err != nil {
			t.Fatal(err)
		}
	}
	if got := a.Balance(); !got.Equal(d("1003000")) {
		t.Errorf("⚠️ 刷新五次后结存 = %s，期望 1003000 —— 累加会得到 1015000", got)
	}
}

// TestFrozenCountsAgainstAvailableNotMargin 是 silent-risks 第 7 条的守卫。
//
// ⚠️ 挂单占用可用资金但**不占用**保证金。不建模会让回测在「挂了一堆单」的
// 状态下以为自己还有钱，从而开出真实账户开不出的仓。
func TestFrozenCountsAgainstAvailableNotMargin(t *testing.T) {
	a := newAcc(t, "1000000")
	if err := a.SetMargin(d1, d("30000"), d("25000")); err != nil {
		t.Fatal(err)
	}
	availBefore := a.Available()
	if err := a.Freeze(d1, d("8000"), d("50")); err != nil {
		t.Fatal(err)
	}
	mustCheck(t, a, "冻结")

	if got := a.Available(); !got.Equal(availBefore.Sub(d("8050"))) {
		t.Errorf("⚠️ 冻结应减少可用资金：%s → %s，期望减少 8050", availBefore, got)
	}
	// 但保证金占用不变，风险度也不因冻结而变。
	snap := a.Snapshot()
	if !snap.CurrMargin.Equal(d("30000")) {
		t.Errorf("⚠️ 冻结不该改变保证金占用，实为 %s", snap.CurrMargin)
	}
	rr, ok := a.RiskRatio()
	if !ok {
		t.Fatal("有结存时应有风险度")
	}
	if !rr.Equal(d("30000").Div(a.Balance())) {
		t.Errorf("风险度应为 占用/结存，实为 %s", rr)
	}

	// 解冻后可用资金回到原处。
	if err := a.Unfreeze(d1, d("8000"), d("50")); err != nil {
		t.Fatal(err)
	}
	mustCheck(t, a, "解冻")
	if got := a.Available(); !got.Equal(availBefore) {
		t.Errorf("解冻后可用资金应回到 %s，实为 %s", availBefore, got)
	}
}

// TestUnfreezeBeyondFrozenRejected 断言超额解冻**报错**而不是静默截断。
//
// ⚠️ 静默截断会让冻结额悄悄归零，于是可用资金凭空变多。
func TestUnfreezeBeyondFrozenRejected(t *testing.T) {
	a := newAcc(t, "1000000")
	if err := a.Freeze(d1, d("5000"), d("30")); err != nil {
		t.Fatal(err)
	}
	if err := a.Unfreeze(d1, d("6000"), d("0")); err == nil {
		t.Error("⚠️ 解冻 6000 超过已冻结的 5000，本该报错")
	}
	if err := a.Unfreeze(d1, d("0"), d("31")); err == nil {
		t.Error("⚠️ 解冻手续费超额，本该报错")
	}
	// 被拒后冻结额必须原封不动。
	if snap := a.Snapshot(); !snap.FrozenMargin.Equal(d("5000")) || !snap.FrozenCommission.Equal(d("30")) {
		t.Errorf("被拒的解冻不得改动冻结额，实为 %s / %s", snap.FrozenMargin, snap.FrozenCommission)
	}
	mustCheck(t, a, "被拒的解冻")
}

// TestRiskRatioDistinguishesZeroFromAbsent 是「零值不是安全的默认」的守卫。
//
// ⚠️ 空账户不是「风险度 0%」，穿仓账户更不是。
// 把它们都渲染成 0，最危险的状态会看起来最安全。
func TestRiskRatioDistinguishesZeroFromAbsent(t *testing.T) {
	cases := []struct {
		name    string
		build   func() *Account
		wantHas bool
	}{
		{"有结存有持仓", func() *Account {
			a := newAcc(t, "1000000")
			_ = a.SetMargin(d1, d("30000"), d("30000"))
			return a
		}, true},
		{"有结存无持仓（风险度确实是零）", func() *Account { return newAcc(t, "1000000") }, true},
		{"结存恰为零", func() *Account { return newAcc(t, "0") }, false},
		{"穿仓：结存为负", func() *Account {
			a := newAcc(t, "1000")
			_ = a.AddCloseProfit(d1, d("-5000"))
			return a
		}, false},
	}
	if len(cases) != 4 {
		t.Fatalf("用例数应为 4，实际 %d", len(cases))
	}
	for _, c := range cases {
		a := c.build()
		rr, ok := a.RiskRatio()
		if ok != c.wantHas {
			t.Errorf("%s：HasRiskRatio = %v，期望 %v（结存 %s）", c.name, ok, c.wantHas, a.Balance())
		}
		if !ok && !rr.IsZero() {
			t.Errorf("%s：没有风险度时应返回零值占位，实为 %s", c.name, rr)
		}
	}
	// 「风险度是零」与「没有风险度」必须能被调用方分开。
	withBalance, _ := newAcc(t, "1000000").RiskRatio()
	zeroBalance, hasZero := newAcc(t, "0").RiskRatio()
	if withBalance.Equal(zeroBalance) && hasZero {
		t.Error("⚠️ 两种情形都返回 0 且都标为有值 —— 调用方分不开")
	}
}

// TestSettleCrystallizesAndResets 覆盖结算的兑现与清零。
func TestSettleCrystallizesAndResets(t *testing.T) {
	a := newAcc(t, "1000000")
	_ = a.Deposit(d1, d("10000"))
	_ = a.AddCloseProfit(d1, d("2000"))
	_ = a.AddCommission(d1, d("100"))
	_ = a.SetPositionProfit(d1, d("5000"))
	_ = a.SetMargin(d1, d("30000"), d("30000"))

	before := a.Balance() // 1000000 + 10000 + 2000 - 100 + 5000 = 1016900
	if !before.Equal(d("1016900")) {
		t.Fatalf("结算前结存 = %s，期望 1016900", before)
	}

	if err := a.Settle(d1, d2); err != nil {
		t.Fatalf("结算失败: %v", err)
	}
	if a.Day != d2 {
		t.Errorf("结算后应推进到 %d，实为 %d", d2, a.Day)
	}
	if got := a.PreBalance(); !got.Equal(before) {
		t.Errorf("上日结存应等于结算前的结存 %s，实为 %s", before, got)
	}

	// ⚠️ 持仓盈亏必须归零：基线已推进到今结算价，相对新基线的持仓盈亏就是零。
	// 忘了归零会让今天的浮盈在明天被【重复计入】一次。
	snap := a.Snapshot()
	if !snap.PositionProfit.IsZero() {
		t.Errorf("⚠️ 结算后持仓盈亏应归零，实为 %s —— 否则今日浮盈明日被重复计入", snap.PositionProfit)
	}
	for _, f := range []struct {
		name string
		v    decimal.Decimal
	}{
		{"入金", snap.Deposit}, {"出金", snap.Withdraw},
		{"平仓盈亏", snap.CloseProfit}, {"手续费", snap.Commission},
	} {
		if !f.v.IsZero() {
			t.Errorf("结算后%s应清零，实为 %s", f.name, f.v)
		}
	}
	// 结存不变（浮盈已兑现进 preBalance）。
	if got := a.Balance(); !got.Equal(before) {
		t.Errorf("⚠️ 结算不该改变结存的数值，%s → %s", before, got)
	}
	// 保证金占用不由结算清零 —— 它是持仓的函数，由门面重算后告知。
	if !snap.CurrMargin.Equal(d("30000")) {
		t.Errorf("结算不该清零保证金占用，实为 %s", snap.CurrMargin)
	}
	mustCheck(t, a, "结算")
}

// TestForgotToSettleIsLoud 断言跨交易日未结算的操作**报错**。
func TestForgotToSettleIsLoud(t *testing.T) {
	a := newAcc(t, "1000000")
	ops := []struct {
		name string
		err  error
	}{
		{"入金", a.Deposit(d2, d("100"))},
		{"出金", a.Withdraw(d2, d("100"))},
		{"平仓盈亏", a.AddCloseProfit(d2, d("100"))},
		{"手续费", a.AddCommission(d2, d("1"))},
		{"持仓盈亏", a.SetPositionProfit(d2, d("100"))},
		{"保证金", a.SetMargin(d2, d("1"), d("1"))},
		{"冻结", a.Freeze(d2, d("1"), d("0"))},
		{"解冻", a.Unfreeze(d2, d("0"), d("0"))},
	}
	if len(ops) != 8 {
		t.Fatalf("操作数应为 8，实际 %d", len(ops))
	}
	for _, o := range ops {
		if o.err == nil {
			t.Errorf("⚠️ %s 在未结算的下一交易日本该报错", o.name)
			continue
		}
		if !strings.Contains(o.err.Error(), "尚未结算") {
			t.Errorf("%s 的报错应指出未结算，实为 %v", o.name, o.err)
		}
	}
	// 重复结算必须报错。
	if err := a.Settle(d1, d2); err != nil {
		t.Fatalf("首次结算应成功: %v", err)
	}
	if err := a.Settle(d1, d2); err == nil {
		t.Error("⚠️ 重复结算同一交易日本该报错")
	}
}

// TestSettleRefusesWithFrozen 断言带着冻结额结算会**报错**。
func TestSettleRefusesWithFrozen(t *testing.T) {
	a := newAcc(t, "1000000")
	if err := a.Freeze(d1, d("5000"), d("30")); err != nil {
		t.Fatal(err)
	}
	err := a.Settle(d1, d2)
	if err == nil {
		t.Fatal("⚠️ 带着冻结额结算本该报错 —— 挂单的跨日存活规则尚未定义")
	}
	if !strings.Contains(err.Error(), "order 包") {
		t.Errorf("报错应指出这属于 order 包的范畴，实为 %v", err)
	}
	// 解冻后放行。
	if err := a.Unfreeze(d1, d("5000"), d("30")); err != nil {
		t.Fatal(err)
	}
	if err := a.Settle(d1, d2); err != nil {
		t.Errorf("解冻后应放行，实为 %v", err)
	}
}

// TestRejects 覆盖各类非法输入。
func TestRejects(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"非 CNY 币种", mustErr(New("USD", d1, d("0")))},
		{"上日结存为负", mustErr(New("CNY", d1, d("-1")))},
		{"交易日不合法", mustErr(New("CNY", types.TradingDay(20260230), d("0")))},
		{"入金为负", newAcc(t, "1000").Deposit(d1, d("-1"))},
		{"入金为零", newAcc(t, "1000").Deposit(d1, d("0"))},
		{"出金超过可用", newAcc(t, "1000").Withdraw(d1, d("1001"))},
		{"手续费为负", newAcc(t, "1000").AddCommission(d1, d("-1"))},
		{"保证金为负", newAcc(t, "1000").SetMargin(d1, d("-1"), d("0"))},
		{"公司保证金低于交易所", newAcc(t, "1000").SetMargin(d1, d("10"), d("20"))},
		{"冻结超过可用", newAcc(t, "1000").Freeze(d1, d("2000"), d("0"))},
	}
	if len(cases) != 10 {
		t.Fatalf("反例数应为 10，实际 %d", len(cases))
	}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("%s 本该报错", c.name)
		}
	}
}

func mustErr(_ *Account, err error) error { return err }

// TestCheckCatchesBrokenInvariant 断言不变量检查真的会失败。
//
// ⚠️ 直接把内部字段改坏，看 Check 抓不抓得到。一条永远返回 nil 的不变量检查
// 和没有检查，在正常状态下长得一模一样。
func TestCheckCatchesBrokenInvariant(t *testing.T) {
	a := newAcc(t, "1000000")
	if err := a.Check(); err != nil {
		t.Fatalf("正常状态下不该报错: %v", err)
	}
	broken := []struct {
		name string
		mut  func(*Account)
	}{
		{"冻结保证金为负", func(x *Account) { x.frozenMargin = d("-1") }},
		{"冻结手续费为负", func(x *Account) { x.frozenCommission = d("-1") }},
		{"权利金冻结为负", func(x *Account) { x.frozenCash = d("-1") }},
		{"累计手续费为负", func(x *Account) { x.commission = d("-1") }},
	}
	if len(broken) != 4 {
		t.Fatalf("破坏用例数应为 4，实际 %d", len(broken))
	}
	for _, b := range broken {
		x := newAcc(t, "1000000")
		b.mut(x)
		if err := x.Check(); err == nil {
			t.Errorf("⚠️ 不变量检查放过了「%s」", b.name)
		}
	}
}
