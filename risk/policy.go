// Package risk 按调用者明确提供的策略给出离线风控信号；不执行交易或还原券商追保制度。
package risk

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/shopspring/decimal"
)

// Policy 是调用者显式选择的阈值，等于阈值允许，不设默认策略。
type Policy struct {
	// MaxRiskRatio 是公司保证金/权益的允许上限，必须为正。
	MaxRiskRatio decimal.Decimal
	// MinEquity 是最低权益，必须为正，避免把非正权益的未知风险度当健康。
	MinEquity decimal.Decimal
	// MinAvailable 是扣除冻结和口径排除浮盈后的最低可用，允许0。
	MinAvailable decimal.Decimal
}

// Report 是固定持仓、行情、费率和冻结假设下的只读报告，不代表券商的处置决定。
type Report struct {
	// Account 保留按完整状态重算的账户快照，风险度未知仍由HasRiskRatio明确表示。
	Account account.Snapshot
	// Policy 保留本次实际使用的策略，便于解释和复现。
	Policy Policy
	// RatioExceeded 仅在风险度有定义且超过允许上限时为true；比较不使用展示除法。
	RatioExceeded bool
	// EquityBelowMinimum 报告权益严格低于最低值。
	EquityBelowMinimum bool
	// AvailableBelowMinimum 报告可用资金严格低于最低值。
	AvailableBelowMinimum bool
	// NeedsMarginCall 汇总三个显式判据，仅为调用者策略信号。
	NeedsMarginCall bool
	// RequiredDeposit 是其他输入不变时补足三项约束所需金额，向上取整到分。
	RequiredDeposit decimal.Decimal
}

// Assess 从完整账户状态重新派生数字；未知策略、不一致状态和无界精度均拒绝，不能生成假健康报告。
func Assess(st account.State, policy Policy) (Report, error) {
	for _, field := range []struct {
		name  string
		value decimal.Decimal
	}{
		{"风险上限", policy.MaxRiskRatio}, {"最低权益", policy.MinEquity}, {"最低可用", policy.MinAvailable},
		{"上日结存", st.PreBalance}, {"入金", st.Deposit}, {"出金", st.Withdraw}, {"平仓盈亏", st.CloseProfit},
		{"手续费", st.Commission}, {"结算手续费", st.SettleCommission}, {"持仓盈亏", st.PositionProfit},
		{"公司保证金", st.CurrMargin}, {"交易所保证金", st.ExchangeMargin}, {"冻结保证金", st.FrozenMargin},
		{"冻结手续费", st.FrozenCommission}, {"冻结资金", st.FrozenCash},
	} {
		if !bounded(field.value) {
			return Report{}, fmt.Errorf("%s超出有限精度范围", field.name)
		}
	}
	if !policy.MaxRiskRatio.IsPositive() || !policy.MinEquity.IsPositive() || policy.MinAvailable.IsNegative() {
		return Report{}, fmt.Errorf("风控策略必须明确给正风险上限/最低权益及非负最低可用")
	}
	acc, err := account.Restore(st)
	if err != nil {
		return Report{}, fmt.Errorf("风控账户状态不可用：%w", err)
	}
	snapshot := acc.Snapshot()
	gap := snapshot.CurrMargin.Sub(snapshot.Balance.Mul(policy.MaxRiskRatio))
	report := Report{Account: snapshot, Policy: policy,
		RatioExceeded:         snapshot.HasRiskRatio && gap.IsPositive(),
		EquityBelowMinimum:    snapshot.Balance.LessThan(policy.MinEquity),
		AvailableBelowMinimum: snapshot.Available.LessThan(policy.MinAvailable),
	}
	report.NeedsMarginCall = report.RatioExceeded || report.EquityBelowMinimum || report.AvailableBelowMinimum
	needed := decimal.Max(decimal.Zero, policy.MinEquity.Sub(snapshot.Balance), policy.MinAvailable.Sub(snapshot.Available))
	if gap.IsPositive() {
		// 除的是待补的正缺口；QuoRem保留余数，重复小数不能向下少补一分。
		amount, remainder := gap.QuoRem(policy.MaxRiskRatio, 2)
		if remainder.IsPositive() {
			amount = amount.Add(decimal.New(1, -2))
		}
		needed = decimal.Max(needed, amount)
	}
	report.RequiredDeposit = needed.RoundCeil(2)
	if !bounded(report.RequiredDeposit) {
		return Report{}, fmt.Errorf("补资计算超出有限精度范围")
	}
	return report, nil
}

// bounded 限制表示规模，既不猜业务金额上限，也不先格式化巨大指数。
func bounded(d decimal.Decimal) bool {
	return d.Exponent() >= -64 && d.Exponent() <= 64 && d.Coefficient().BitLen() <= 512
}
