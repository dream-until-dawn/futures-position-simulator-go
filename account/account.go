// Package account 是资金账户：结存链条、可用资金、各类冻结。
//
// # 它不认识持仓
//
// 保证金占用与持仓盈亏由**门面**按持仓算出后告知本包。这是刻意的：
// 反过来（account 直接读 position）会让依赖成环，而本包与 position 各自
// 都要被 settle / risk / view 依赖。见 docs/design.md §3 的依赖图。
//
// # 结存链条
//
//	今结存 Balance = 上日结存 PreBalance
//	               + 入金 Deposit − 出金 Withdraw
//	               + 平仓盈亏 CloseProfit
//	               + 持仓盈亏 PositionProfit
//	               − 手续费 Commission
//
//	可用资金 Available = Balance − 保证金占用 − 全部冻结
//	风险度            = 保证金占用 / Balance
//
// ⚠️ 盘中 PositionProfit 是浮动的，于是 Balance 盘中就是**动态权益**；
// 日终结算把浮盈兑现，Balance 成为静态结存并作为明日的 PreBalance。
//
// # 报单冻结必须建模
//
// ⚠️ 挂单占用可用资金但**不占用**保证金。不建模会让回测在「挂了一堆单」的
// 状态下以为自己还有钱，从而开出真实账户开不出的仓——
// docs/silent-risks.md 第 7 条。
package account

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Account 是单币种资金账户。v1.0 只做 CNY。
type Account struct {
	Currency string
	Day      types.TradingDay

	preBalance decimal.Decimal // 上一交易日结算后的结存，今日一切计算的基线

	// 本交易日累计量，结算时清零
	deposit     decimal.Decimal
	withdraw    decimal.Decimal
	closeProfit decimal.Decimal // 逐日盯市口径
	commission  decimal.Decimal

	// 由门面按持仓算出后告知
	positionProfit decimal.Decimal
	currMargin     decimal.Decimal // 期货公司口径，风险度与可用资金用它
	exchangeMargin decimal.Decimal // 交易所口径，只为对拍留位

	frozenMargin     decimal.Decimal
	frozenCommission decimal.Decimal
	frozenCash       decimal.Decimal // 期权权利金冻结，v1.0 恒为零
}

// New 建一个账户。preBalance 是上一交易日结算后的结存；新开户传零。
func New(currency string, day types.TradingDay, preBalance decimal.Decimal) (*Account, error) {
	if currency != "CNY" {
		// ⚠️ 明确拒绝而不是默默当成 CNY：多币种在 v1.0 之外，
		// 而「默默按 CNY 算」会让一个不支持的场景看起来跑通了。
		return nil, fmt.Errorf("v1.0 只支持 CNY，得到 %q", currency)
	}
	if err := day.Validate(); err != nil {
		return nil, fmt.Errorf("开户失败: %w", err)
	}
	if preBalance.IsNegative() {
		return nil, fmt.Errorf("上日结存 %s 为负 —— 穿仓账户请显式说明来源，不要静默开户", preBalance)
	}
	return &Account{Currency: currency, Day: day, preBalance: preBalance}, nil
}

func (a *Account) checkDay(day types.TradingDay) error {
	if day == a.Day {
		return nil
	}
	if day.After(a.Day) {
		return fmt.Errorf("账户停在交易日 %d，却收到交易日 %d 的操作 —— "+
			"⚠️ 本交易日尚未结算。跨交易日前必须先 Settle，否则今日的累计量会混进明日，"+
			"而结存链条全程看不出异常", a.Day, day)
	}
	return fmt.Errorf("账户已在交易日 %d，却收到更早的交易日 %d 的操作 —— 时间倒流", a.Day, day)
}

// PreBalance 返回上日结存。
func (a *Account) PreBalance() decimal.Decimal { return a.preBalance }

// StaticBalance 返回静态权益：上日结存 + 今日出入金，不含任何盈亏。
func (a *Account) StaticBalance() decimal.Decimal {
	return a.preBalance.Add(a.deposit).Sub(a.withdraw)
}

// Balance 返回结存。
//
// ⚠️ 盘中它是**动态权益**（含浮动的持仓盈亏）；日终结算后才是静态结存。
// CTP 的同名字段也是这个语义，见 cn-futures-rules.md §2 的字段语义陷阱。
func (a *Account) Balance() decimal.Decimal {
	return a.StaticBalance().
		Add(a.closeProfit).
		Add(a.positionProfit).
		Sub(a.commission)
}

// Available 返回可用资金。
func (a *Account) Available() decimal.Decimal {
	return a.Balance().
		Sub(a.currMargin).
		Sub(a.frozenMargin).
		Sub(a.frozenCash).
		Sub(a.frozenCommission)
}

// RiskRatio 返回风险度 = 保证金占用 / 结存。
//
// ⚠️ 第二个返回值区分「风险度是零」与「**没有**风险度」。
// 结存为零或为负时风险度没有意义——空账户不是「风险度 0%」，
// 穿仓账户更不是。把它们都渲染成 0 会让最危险的状态看起来最安全。
func (a *Account) RiskRatio() (decimal.Decimal, bool) {
	bal := a.Balance()
	if !bal.IsPositive() {
		return decimal.Zero, false
	}
	return a.currMargin.Div(bal), true
}

// Deposit 入金。
func (a *Account) Deposit(day types.TradingDay, amount decimal.Decimal) error {
	if err := a.checkDay(day); err != nil {
		return err
	}
	if !amount.IsPositive() {
		return fmt.Errorf("入金额必须为正，得到 %s", amount)
	}
	a.deposit = a.deposit.Add(amount)
	return nil
}

// Withdraw 出金。
//
// ⚠️ 超过可用资金时**报错**。真实账户不会让你取走保证金，
// 而静默允许会让回测里的资金曲线凭空多出一笔钱。
func (a *Account) Withdraw(day types.TradingDay, amount decimal.Decimal) error {
	if err := a.checkDay(day); err != nil {
		return err
	}
	if !amount.IsPositive() {
		return fmt.Errorf("出金额必须为正，得到 %s", amount)
	}
	if avail := a.Available(); amount.GreaterThan(avail) {
		return fmt.Errorf("出金 %s 超过可用资金 %s", amount, avail)
	}
	a.withdraw = a.withdraw.Add(amount)
	return nil
}

// AddCloseProfit 累加平仓盈亏（逐日盯市口径），可正可负。
func (a *Account) AddCloseProfit(day types.TradingDay, profit decimal.Decimal) error {
	if err := a.checkDay(day); err != nil {
		return err
	}
	a.closeProfit = a.closeProfit.Add(profit)
	return nil
}

// AddCommission 累加手续费。手续费只增不减，传负数报错。
func (a *Account) AddCommission(day types.TradingDay, fee decimal.Decimal) error {
	if err := a.checkDay(day); err != nil {
		return err
	}
	if fee.IsNegative() {
		return fmt.Errorf("手续费 %s 为负 —— 返佣不在本库范围内，请在外部处理", fee)
	}
	a.commission = a.commission.Add(fee)
	return nil
}

// SetPositionProfit 由门面按持仓算出后告知。
//
// ⚠️ 它是**覆盖**不是累加：持仓盈亏是一个随行情重算的量，不是流水。
// 写成累加会让它随每次刷新无界增长，而增长得很像盈利。
func (a *Account) SetPositionProfit(day types.TradingDay, profit decimal.Decimal) error {
	if err := a.checkDay(day); err != nil {
		return err
	}
	a.positionProfit = profit
	return nil
}

// SetMargin 由门面按持仓算出后告知保证金占用。
//
// company 是期货公司口径（风险度与可用资金用它），exchange 是交易所口径
// （只为对拍留位）。⚠️ 两者一般不等：期货公司在交易所标准上加收。
func (a *Account) SetMargin(day types.TradingDay, company, exchange decimal.Decimal) error {
	if err := a.checkDay(day); err != nil {
		return err
	}
	if company.IsNegative() || exchange.IsNegative() {
		return fmt.Errorf("保证金占用不能为负：公司 %s / 交易所 %s", company, exchange)
	}
	if company.LessThan(exchange) {
		// ⚠️ 公司口径低于交易所口径意味着加收为负，那不成立。
		// 静默接受会让风险度偏低，而偏低的方向正是最危险的那个。
		return fmt.Errorf("公司保证金 %s 低于交易所保证金 %s —— 加收比例不能为负",
			company, exchange)
	}
	a.currMargin, a.exchangeMargin = company, exchange
	return nil
}

// Freeze 冻结报单占用的保证金与手续费。
func (a *Account) Freeze(day types.TradingDay, margin, commission decimal.Decimal) error {
	if err := a.checkDay(day); err != nil {
		return err
	}
	if margin.IsNegative() || commission.IsNegative() {
		return fmt.Errorf("冻结额不能为负：保证金 %s / 手续费 %s", margin, commission)
	}
	if total := margin.Add(commission); total.GreaterThan(a.Available()) {
		return fmt.Errorf("冻结 %s 超过可用资金 %s", total, a.Available())
	}
	a.frozenMargin = a.frozenMargin.Add(margin)
	a.frozenCommission = a.frozenCommission.Add(commission)
	return nil
}

// Unfreeze 解冻。
//
// ⚠️ 解冻额超过已冻结额时**报错**。静默截断会让冻结额悄悄归零，
// 于是可用资金凭空变多——而这正是「挂单撤了但钱没回来」的反面，同样不报错。
func (a *Account) Unfreeze(day types.TradingDay, margin, commission decimal.Decimal) error {
	if err := a.checkDay(day); err != nil {
		return err
	}
	if margin.IsNegative() || commission.IsNegative() {
		return fmt.Errorf("解冻额不能为负：保证金 %s / 手续费 %s", margin, commission)
	}
	if margin.GreaterThan(a.frozenMargin) {
		return fmt.Errorf("解冻保证金 %s 超过已冻结的 %s", margin, a.frozenMargin)
	}
	if commission.GreaterThan(a.frozenCommission) {
		return fmt.Errorf("解冻手续费 %s 超过已冻结的 %s", commission, a.frozenCommission)
	}
	a.frozenMargin = a.frozenMargin.Sub(margin)
	a.frozenCommission = a.frozenCommission.Sub(commission)
	return nil
}

// Settle 执行日终结算：把浮盈兑现成结存，清零本日累计量。
//
// ⚠️ 结算后 positionProfit **归零**，这不是清空数据，是逐日盯市的定义：
// 基线已推进到今结算价，相对于新基线的持仓盈亏就是零。
// 忘了归零会让今天的浮盈在明天被**重复计入**一次结存。
//
// ⚠️ 结算时仍有冻结额意味着还有未成交的挂单跨日存活。本库**报错**：
// 挂单的跨日存活规则（GFD 当日有效、是否自动撤销）属于报单范畴，
// 由 order 包在 v0.4.0 处理；在那之前，带着冻结额结算是一个未定义的状态，
// 不该被静默通过。
func (a *Account) Settle(day, nextDay types.TradingDay) error {
	if day != a.Day {
		return fmt.Errorf("结算的是交易日 %d，而账户停在 %d —— 结算不能跳过交易日，也不能重复执行", day, a.Day)
	}
	if !nextDay.After(day) {
		return fmt.Errorf("下一交易日 %d 不晚于当前交易日 %d", nextDay, day)
	}
	if err := nextDay.Validate(); err != nil {
		return fmt.Errorf("下一交易日不合法: %w", err)
	}
	if !a.frozenMargin.IsZero() || !a.frozenCommission.IsZero() || !a.frozenCash.IsZero() {
		return fmt.Errorf("结算时仍有冻结额（保证金 %s / 手续费 %s / 权利金 %s）—— "+
			"挂单的跨日存活规则属于 order 包（v0.4.0），在那之前这是未定义状态",
			a.frozenMargin, a.frozenCommission, a.frozenCash)
	}

	a.preBalance = a.Balance()
	a.deposit = decimal.Zero
	a.withdraw = decimal.Zero
	a.closeProfit = decimal.Zero
	a.commission = decimal.Zero
	a.positionProfit = decimal.Zero // 基线已推进，相对新基线的持仓盈亏为零
	a.Day = nextDay
	return nil
}

// Snapshot 是账户的只读快照，字段与 CTP / DIFF 的资金截面同名对应。
type Snapshot struct {
	Currency         string
	TradingDay       types.TradingDay
	PreBalance       decimal.Decimal
	StaticBalance    decimal.Decimal
	Balance          decimal.Decimal
	Available        decimal.Decimal
	Deposit          decimal.Decimal
	Withdraw         decimal.Decimal
	CloseProfit      decimal.Decimal
	PositionProfit   decimal.Decimal
	Commission       decimal.Decimal
	CurrMargin       decimal.Decimal
	ExchangeMargin   decimal.Decimal
	FrozenMargin     decimal.Decimal
	FrozenCommission decimal.Decimal
	FrozenCash       decimal.Decimal

	// RiskRatio 与 HasRiskRatio 成对出现。
	//
	// ⚠️ 结存非正时没有风险度。空账户不是「风险度 0%」，穿仓账户更不是——
	// 把它们都渲染成 0，最危险的状态会看起来最安全。
	RiskRatio    decimal.Decimal
	HasRiskRatio bool
}

// Snapshot 取当前快照。
func (a *Account) Snapshot() Snapshot {
	rr, ok := a.RiskRatio()
	return Snapshot{
		Currency: a.Currency, TradingDay: a.Day,
		PreBalance: a.preBalance, StaticBalance: a.StaticBalance(),
		Balance: a.Balance(), Available: a.Available(),
		Deposit: a.deposit, Withdraw: a.withdraw,
		CloseProfit: a.closeProfit, PositionProfit: a.positionProfit,
		Commission: a.commission,
		CurrMargin: a.currMargin, ExchangeMargin: a.exchangeMargin,
		FrozenMargin: a.frozenMargin, FrozenCommission: a.frozenCommission,
		FrozenCash: a.frozenCash,
		RiskRatio:  rr, HasRiskRatio: ok,
	}
}

// Check 断言账户的内部不变量。
//
// ⚠️ 它是「每次状态变更后都应成立」的那条恒等式的机械形式：
//
//	可用 = 结存 − 保证金占用 − 全部冻结
//
// 它抓不到「结存本身算错了」，但能抓到「某处忘了把冻结算进可用」——
// 而后者正是 silent-risks 第 7 条（报单冻结不建模）的形态。
func (a *Account) Check() error {
	want := a.Balance().
		Sub(a.currMargin).Sub(a.frozenMargin).
		Sub(a.frozenCash).Sub(a.frozenCommission)
	if !a.Available().Equal(want) {
		return fmt.Errorf("不变量破裂：可用 %s ≠ 结存 %s − 占用 %s − 冻结 %s/%s/%s",
			a.Available(), a.Balance(), a.currMargin,
			a.frozenMargin, a.frozenCash, a.frozenCommission)
	}
	if a.frozenMargin.IsNegative() || a.frozenCommission.IsNegative() || a.frozenCash.IsNegative() {
		return fmt.Errorf("冻结额为负：%s / %s / %s",
			a.frozenMargin, a.frozenCommission, a.frozenCash)
	}
	if a.commission.IsNegative() {
		return fmt.Errorf("累计手续费为负：%s", a.commission)
	}
	return nil
}
