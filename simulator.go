package futsim

import (
	"fmt"
	"sort"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Config 是开一个模拟账户所需的全部输入。没有默认值。
type Config struct {
	Day        types.TradingDay
	PreBalance decimal.Decimal
	Rules      refdata.Provider
	Choices    Choices

	// —— 报单路径（Submit）才用的事实；不给则对应那一项「没查成」，不成交 ——

	// Calendar 查交易时段。
	Calendar *refdata.Calendar
	// TickRounding 是涨跌停对齐最小变动价位的方向，按交易所。实测过的见 MeasuredTickRounding。
	TickRounding map[types.Exchange]refdata.TickRounding
	// PositionLimits 是限仓，按合约。⚠️ 本库没有这份数据：回测调用方自己给（给一个足够大的数也是一种声明）。
	PositionLimits map[types.InstrumentID]int
}

// Quote 是一个合约此刻的计价输入。
//
// ⚠️ 每个价都配 Has*：「价是零」与「没有这个价」必须分开（§6.5）。
type Quote struct {
	Instrument       types.InstrumentID
	Last             decimal.Decimal
	HasLast          bool
	PreSettlement    decimal.Decimal
	HasPreSettlement bool
}

type posKey struct {
	inst  types.InstrumentID
	hedge types.HedgeFlag
}

type priceState struct {
	last, pre       decimal.Decimal
	hasLast, hasPre bool
}

// Simulator 是一个资金账户及其全部持仓。不并发安全。
type Simulator struct {
	rules   refdata.Provider
	choices Choices

	acc       *account.Account
	positions map[posKey]*position.Position
	prices    map[types.InstrumentID]priceState

	book           *order.Book // 挂着的委托（F4）
	calendar       *refdata.Calendar
	tickRounding   map[types.Exchange]refdata.TickRounding
	positionLimits map[types.InstrumentID]int

	// groups 是最近一次计价的保证金分组分解（margin.Compute 给的），由 MarginGroups 只读给出。
	groups []margin.GroupResult

	// broken 非空时模拟器处于**失效态**：写账户中途失败，状态不再可信。
	broken error
}

// New 开一个模拟账户。
func New(cfg Config) (*Simulator, error) {
	if cfg.Rules == nil {
		return nil, fmt.Errorf("没有规则数据（Rules 为 nil）")
	}
	if err := cfg.Choices.validate(); err != nil {
		return nil, fmt.Errorf("口径没选齐：\n%w", err)
	}
	acc, err := account.New("CNY", cfg.Day, cfg.PreBalance, cfg.Choices.Algorithm)
	if err != nil {
		return nil, err
	}
	return &Simulator{
		rules: cfg.Rules, choices: cfg.Choices, acc: acc,
		positions: map[posKey]*position.Position{},
		prices:    map[types.InstrumentID]priceState{},
		calendar:  cfg.Calendar, tickRounding: cfg.TickRounding, positionLimits: cfg.PositionLimits,
		book: order.NewBook(),
	}, nil
}

func (s *Simulator) usable(day types.TradingDay) error {
	if s.broken != nil {
		return fmt.Errorf("模拟器已失效：%w", s.broken)
	}
	if day != s.acc.Day {
		return fmt.Errorf("模拟器停在交易日 %d，收到交易日 %d 的操作", s.acc.Day, day)
	}
	return nil
}

// Deposit 入金。
func (s *Simulator) Deposit(day types.TradingDay, amount decimal.Decimal) error {
	if err := s.usable(day); err != nil {
		return err
	}
	return s.acc.Deposit(day, amount)
}

// Withdraw 出金。上限是可用资金（按盈亏算法扣掉不计入的浮盈）。
func (s *Simulator) Withdraw(day types.TradingDay, amount decimal.Decimal) error {
	if err := s.usable(day); err != nil {
		return err
	}
	return s.acc.Withdraw(day, amount)
}

// Account 返回账户快照。
func (s *Simulator) Account() account.Snapshot { return s.acc.Snapshot() }

// MarginGroups 返回**最近一次计价**算出的保证金分组分解（`margin.Compute` 的 Groups），逐组带多空两边。
//
// ⚠️ 它随每一次 Mark / ApplyTrade / Settle 重算，不是缓存的独立状态；空仓时为空。
// ⚠️ **逐方向的数只有在组键是合约时**（SideScope = ByInstrument，或该组未启用大边）才对应柜台的 margin_long / margin_short：
// ByProduct 下一组跨多个合约，「这个合约的多头占用」没有定义 —— 调用方要自己核组键，本方法不替它挑（design.md「门面的形状」§12）。
func (s *Simulator) MarginGroups() []margin.GroupResult {
	out := make([]margin.GroupResult, len(s.groups))
	copy(out, s.groups)
	return out
}

// Position 返回一个合约（投机）持仓的**副本**。改它不影响模拟器。
func (s *Simulator) Position(inst types.InstrumentID, hedge types.HedgeFlag) (*position.Position, bool) {
	p, ok := s.positions[posKey{inst, hedge}]
	if !ok {
		return nil, false
	}
	return p.Clone(), true
}

// sortedKeys 让遍历顺序确定：map 的随机序会让报错点名的合约每次不同。
func sortedKeys(m map[posKey]*position.Position) []posKey {
	keys := make([]posKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.inst.Canonical() != b.inst.Canonical() {
			return a.inst.Canonical() < b.inst.Canonical()
		}
		return a.hedge < b.hedge
	})
	return keys
}
