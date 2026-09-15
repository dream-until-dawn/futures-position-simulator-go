package ctpfixture

import (
	"math"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// ctpAlgorithm 把柜台声明的 `Algorithm` 映射到本库的取值。
//
// ⚠️ 只映射 '1' / '2'：'3' / '4' 本库拒绝开户（无观测），这里也不替它们找个近似。
func ctpAlgorithm(s string) (account.Algorithm, bool) {
	switch s {
	case "1":
		return account.AlgorithmAll, true
	case "2":
		return account.AlgorithmOnlyLost, true
	}
	return account.AlgorithmUnset, false
}

// TestProductionAccountAvailableAgainstCTP 用**生产的 `account`** 按 CTP 截面的各分量重建账户，
// 拿 `Available()` 比柜台。
//
// ⚠️ 它与 `TestAccountIdentityAgainstCTP` / `TestIdentityAgreesWithDeclaredAlgorithm` 不重复：
// 那两条是在 float64 里**独立重写**的恒等式，**不经过 account 包** ——
// 20260915 的影响实验里，把 `account.Available` 在两种算法之间来回改，那两条一次都没红。
// ⇒ 柜台量到的规则此前只活在测试里，生产代码可以与它背道而驰而没有任何东西报出来。
//
// ⚠️ 容差 1e-6：本库是 decimal、柜台是 float64，量级 2e7 上 float64 的舍入在 1e-8 附近；
// 而本条要分开的差是**浮盈本身**（最小的正样本 +30），差七个数量级以上。
func TestProductionAccountAvailableAgainstCTP(t *testing.T) {
	const tol = 1e-6
	n, positives, mixed := 0, 0, 0
	for name, f := range loadCTP(t) {
		if len(f.Account) == 0 {
			continue
		}
		decl, ok := f.BrokerParams["Algorithm"].(string)
		if !ok {
			continue // 早期夹具没拍到声明 —— 没有声明就选不了算法，不猜
		}
		alg, ok := ctpAlgorithm(decl)
		if !ok {
			t.Errorf("⚠️ %s 声明 Algorithm=%q，本库没有观测过的取值 —— 这份截面是量它的机会", name, decl)
			continue
		}
		day, err := types.ParseTradingDay(f.TradingDay)
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		if fc := flt(t, f.Account, "FrozenCash"); fc != 0 {
			t.Fatalf("⚠️ %s 有权利金冻结 %v —— account 没有这一项的入口，重建出来的可用必然错", name, fc)
		}
		a, err := account.New("CNY", day, num(t, f.Account, "PreBalance"), alg)
		if err != nil {
			t.Fatalf("%s：%v", name, err)
		}
		must := func(step string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("%s %s：%v", name, step, err)
			}
		}
		if v := num(t, f.Account, "Deposit"); v.IsPositive() {
			must("入金", a.Deposit(day, v))
		}
		must("平仓盈亏", a.AddCloseProfit(day, num(t, f.Account, "CloseProfit")))
		must("手续费", a.AddCommission(day, num(t, f.Account, "Commission")))
		must("持仓盈亏", a.SetPositionProfit(day, num(t, f.Account, "PositionProfit")))
		must("保证金", a.SetMargin(day, num(t, f.Account, "CurrMargin"), num(t, f.Account, "ExchangeMargin")))
		must("冻结", a.Freeze(day, num(t, f.Account, "FrozenMargin"), num(t, f.Account, "FrozenCommission")))
		// ⚠️ 出金放最后：它经 Available 判上限，而柜台那笔出金发生时的可用未知
		if v := num(t, f.Account, "Withdraw"); v.IsPositive() {
			must("出金", a.Withdraw(day, v))
		}
		must("自查", a.Check())

		bal, _ := a.Balance().Float64()
		if want := flt(t, f.Account, "Balance"); math.Abs(bal-want) > tol {
			t.Errorf("%s：结存本库 %v、柜台 %v —— 分量读错了，下面的可用比较不可信", name, bal, want)
			continue
		}
		got, _ := a.Available().Float64()
		pp := flt(t, f.Account, "PositionProfit")
		if want := flt(t, f.Account, "Available"); math.Abs(got-want) > tol {
			t.Errorf("⚠️ %s：可用本库 %v、柜台 %v（差 %v；持仓盈亏 %v、声明 %q → %s）",
				name, got, want, want-got, pp, decl, alg)
		}
		n++
		if pp > 0 {
			positives++
		}
		if hasGainAndLoss(f.Positions) {
			mixed++
		}
	}
	if n < 5 {
		t.Fatalf("⚠️ 只核了 %d 份（下界 5）—— 本条在空转", n)
	}
	// ⚠️ 判别力：浮盈为正的截面上两种算法才给不同的数。
	if positives == 0 {
		t.Fatalf("⚠️ 核了 %d 份，没有一份持仓盈亏 > 0 —— 「全部计算」与「只计浮亏」在这批上给同一个数，本条分不开它们", n)
	}
	// ⚠️ 判别力第二层（评审 20260915）：「按账户净额扣」与「逐持仓扣正数」只在**有赚有赔**的截面上分得开。
	// 全是单腿的截面上两种读法同值 —— 那时本条守不住「净额」这一半。
	if mixed == 0 {
		t.Fatalf("⚠️ %d 份截面里没有一份同时有赚有赔的持仓 —— 净额与逐持仓分不开（原先靠 ctp-status-20260914-2）", n)
	}
	t.Logf("ⓘ 生产 account 核了 %d 份 CTP 截面，其中浮盈为正 %d 份、有赚有赔 %d 份", n, positives, mixed)
}

// hasGainAndLoss 报告一份截面里是否同时有持仓盈亏为正与为负的持仓记录。
func hasGainAndLoss(positions map[string]map[string]any) bool {
	gain, loss := false, false
	for _, r := range positions {
		pp, ok := r["PositionProfit"].(float64)
		if !ok {
			continue
		}
		gain = gain || pp > 0
		loss = loss || pp < 0
	}
	return gain && loss
}
