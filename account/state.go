package account

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// State 是账户的全部内部状态，原样可存可取。
//
// ⚠️ 与 Snapshot 不同：Snapshot 带着推出来的数（结存、可用、风险度），State 只带**存储**的那几项 ——
// 推出来的数在 Restore 之后由同一套公式重新推，不从存档里读，免得存档里的「可用」与公式各说各的。
type State struct {
	Currency  string
	Day       types.TradingDay
	Algorithm Algorithm

	PreBalance  decimal.Decimal
	Deposit     decimal.Decimal
	Withdraw    decimal.Decimal
	CloseProfit decimal.Decimal
	Commission  decimal.Decimal
	// SettleCommission 是结算时计入结存的手续费（每笔按结算口径重算后的和）；CommissionTrades 是计入了几笔。
	// ⚠️ 丢了前者，盘中存档 → 恢复 → 结算会按「整笔都不计」算。两项是 StateFormat 3 新增的（F10）。
	SettleCommission decimal.Decimal
	CommissionTrades int
	PositionProfit   decimal.Decimal
	CurrMargin       decimal.Decimal
	ExchangeMargin   decimal.Decimal

	FrozenMargin     decimal.Decimal
	FrozenCommission decimal.Decimal
	FrozenCash       decimal.Decimal
}

// State 导出账户的内部状态。
func (a *Account) State() State {
	return State{
		Currency: a.Currency, Day: a.Day, Algorithm: a.Algorithm,
		PreBalance: a.preBalance, Deposit: a.deposit, Withdraw: a.withdraw,
		CloseProfit: a.closeProfit, Commission: a.commission, SettleCommission: a.settleCommission, CommissionTrades: a.commissionTrades, PositionProfit: a.positionProfit,
		CurrMargin: a.currMargin, ExchangeMargin: a.exchangeMargin,
		FrozenMargin: a.frozenMargin, FrozenCommission: a.frozenCommission, FrozenCash: a.frozenCash,
	}
}

// Restore 从 State 还原一个账户。
//
// ⚠️ 走与 New 相同的检查（币种、交易日、上日结存非负、盈亏算法有观测），再加上各分量自己的约束
// （出入金、手续费、占用、冻结非负，公司占用不低于交易所占用），最后 Check。
// 存档是可以手改的文件 —— 「反正是自己写的」不是跳过检查的理由（design.md §6.5）。
func Restore(st State) (*Account, error) {
	a, err := New(st.Currency, st.Day, st.PreBalance, st.Algorithm)
	if err != nil {
		return nil, fmt.Errorf("恢复账户：%w", err)
	}
	for _, f := range []struct {
		name string
		v    decimal.Decimal
	}{
		{"入金", st.Deposit}, {"出金", st.Withdraw}, {"手续费", st.Commission}, {"结算口径手续费", st.SettleCommission},
		{"公司占用", st.CurrMargin}, {"交易所占用", st.ExchangeMargin},
		{"冻结保证金", st.FrozenMargin}, {"冻结手续费", st.FrozenCommission}, {"冻结权利金", st.FrozenCash},
	} {
		if f.v.IsNegative() {
			return nil, fmt.Errorf("恢复账户：%s %s 为负", f.name, f.v)
		}
	}
	if st.CurrMargin.LessThan(st.ExchangeMargin) {
		return nil, fmt.Errorf("恢复账户：公司占用 %s 低于交易所占用 %s", st.CurrMargin, st.ExchangeMargin)
	}
	a.deposit, a.withdraw = st.Deposit, st.Withdraw
	a.closeProfit, a.commission, a.positionProfit = st.CloseProfit, st.Commission, st.PositionProfit
	a.settleCommission, a.commissionTrades = st.SettleCommission, st.CommissionTrades
	a.currMargin, a.exchangeMargin = st.CurrMargin, st.ExchangeMargin
	a.frozenMargin, a.frozenCommission, a.frozenCash = st.FrozenMargin, st.FrozenCommission, st.FrozenCash
	if err := a.Check(); err != nil {
		return nil, fmt.Errorf("恢复账户：%w", err)
	}
	return a, nil
}
