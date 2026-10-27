// Package lifecycle 按显式给定的规则处理日历覆盖、保证金台阶和持仓期限，不推测真实制度或执行交割。
package lifecycle

import (
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// InvestorKind 标识调用者声明的资格；不是本库对真实账户的认证。
type InvestorKind uint8

const (
	// InvestorUnknown 是未知资格，构造规则时拒绝。
	InvestorUnknown InvestorKind = iota
	// Individual 表示本次使用自然人规则集合。
	Individual
	// Institution 表示本次使用机构规则集合。
	Institution
)

// MarginStep 是From含端点生效的完整投机保证金率，0费率必须由显式条目提供。
type MarginStep struct {
	From  types.TradingDay
	Rates refdata.MarginRates
}

// Contract 为一个合约明确给出最后交易、持仓日期及保证金台阶。
type Contract struct {
	Instrument     types.InstrumentID
	LastTradingDay types.TradingDay
	LastHoldingDay types.TradingDay
	MarginSteps    []MarginStep
}

// Schedule 是一份明确适用范围和资格的规则声明，不能用空来源或零日期代替未知。
type Schedule struct {
	First, Last types.TradingDay
	Investor    InvestorKind
	Source      string
	Contracts   []Contract
}

// Holding 是一个合约全部投机持仓的合计；Long/Short都必须非负。
type Holding struct {
	Instrument  types.InstrumentID
	Long, Short int
}

// Holdings 的Known区分未知仓位与明确的空仓快照。
type Holdings struct {
	Known     bool
	Positions []Holding
}

// Status 是一个合约在绑定日的规则与敞口状态，未清仓不会被静默丢弃。
type Status struct {
	Instrument                                        types.InstrumentID
	Long, Short                                       int
	MarginFrom                                        types.TradingDay
	LastTradingDay, LastHoldingDay                    types.TradingDay
	Expired, CloseOnly, ExitByClose, HoldingViolation bool
	// Tradable是基准声明与最后交易日共同决定的交易状态，不代表实时连接/行情可用。
	Tradable bool
}

// Report 明确列出规则日期、资格和来源；HasBlockingHoldings表示不能继续带仓进入该日。
type Report struct {
	Day                 types.TradingDay
	Investor            InvestorKind
	Source              string
	Contracts           []Status
	HasBlockingHoldings bool
}

// DailyRules 是不可变的当日规则视图，持有独立基准数据与Schedule副本。
type DailyRules struct {
	base, data *refdata.Snapshot
	day        types.TradingDay
	schedule   Schedule
	contracts  map[types.InstrumentID]Status
	ids        []types.InstrumentID
}
