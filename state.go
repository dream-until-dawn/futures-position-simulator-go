package futsim

import (
	"fmt"
	"sort"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// StateFormat 是存档格式的版本。对不上就报错，不迁移（v0.9.0 之前格式可以变）。
//
// ⚠️ State 及其嵌套结构没有 json tag，JSON 键就是 Go 字段名：**改字段名、加删字段都要手动把这个数加一**，
// 它不会自己跟着变（评审 20260915 要求写在这里，不只写在 design 里）。
//
//	1  F5 起
//	2  State 加 Quotas（F11，裸平按当日开仓额度收档；使用者 20260918 确认抬一格）
//	3  account.State 加 SettleCommission / CommissionTrades（F10，结算时按笔重算手续费；使用者确认「抬一格」）
const StateFormat = 3

// State 是模拟器的全部状态，全是数据。小数在 JSON 里是字符串（decimal.Decimal 的默认）。
//
// ⚠️ 限仓、取整方向、日历、规则数据不在里面：它们是规则输入，由 Restore 的 Config 再给一次。
type State struct {
	Format       int
	Day          types.TradingDay
	RulesVersion int64
	Choices      Choices

	Account   account.State
	Positions []PositionState
	Prices    []PriceState
	Orders    []OrderState
	// Quotas 是裸平的当日开仓额度计数（§13 #23 的 (f)，design.md 门面形状 §15）。⚠️ 丢了它，恢复后的裸平会按「今天没开过仓」收平昨档
	Quotas []QuotaState
}

// QuotaState 是一个（合约, 投保, 持仓方向）的当日额度计数。
type QuotaState struct {
	Instrument    types.InstrumentID
	Hedge         types.HedgeFlag
	Direction     types.Direction // 持仓方向：多头 = Buy
	OpenedToday   int             // 当日开仓手数
	ChargedToday  int             // 当日已按平今档收过的手数
	ExplicitToday int             // ChargedToday 里有几手来自显式平今
}

// PositionState 是一条持仓的明细。
type PositionState struct {
	Instrument types.InstrumentID
	Hedge      types.HedgeFlag
	Day        types.TradingDay
	DateType   refdata.PositionDateType
	Long       []position.Lot
	Short      []position.Lot
}

// PriceState 是一个合约的计价输入。
type PriceState struct {
	Instrument       types.InstrumentID
	Last             decimal.Decimal
	HasLast          bool
	PreSettlement    decimal.Decimal
	HasPreSettlement bool
}

// OrderState 是一笔挂单。
type OrderState struct {
	ID      string
	Request order.Request
	Frozen  order.Frozen
}

// State 导出模拟器的全部状态。失效态报错：半截状态存下来再恢复，等于把失效洗掉了。
func (s *Simulator) State() (State, error) {
	if s.broken != nil {
		return State{}, fmt.Errorf("模拟器已失效，不许导出：%w", s.broken)
	}
	st := State{Format: StateFormat, Day: s.acc.Day, RulesVersion: s.rules.Version(), Choices: s.choices, Account: s.acc.State()}
	for _, k := range sortedKeys(s.positions) {
		p := s.positions[k]
		long, _ := p.Side(types.Buy)
		short, _ := p.Side(types.Sell)
		st.Positions = append(st.Positions, PositionState{Instrument: p.Instrument, Hedge: p.Hedge, Day: p.Day,
			DateType: p.DateType(), Long: long.Lots(), Short: short.Lots()})
	}
	insts := make(map[types.InstrumentID]bool)
	for id := range s.prices {
		insts[id] = true
	}
	for _, id := range sortedInstruments(insts) {
		px := s.prices[id]
		st.Prices = append(st.Prices, PriceState{Instrument: id, Last: px.last, HasLast: px.hasLast, PreSettlement: px.pre, HasPreSettlement: px.hasPre})
	}
	for _, id := range s.book.Live() {
		req, fr, _ := s.book.Get(id)
		st.Orders = append(st.Orders, OrderState{ID: id, Request: req, Frozen: fr})
	}
	for k, q := range s.quotas {
		if q == (quotaCount{}) {
			continue
		}
		st.Quotas = append(st.Quotas, QuotaState{Instrument: k.inst, Hedge: k.hedge, Direction: k.side,
			OpenedToday: q.Opened, ChargedToday: q.Charged, ExplicitToday: q.Explicit})
	}
	sort.Slice(st.Quotas, func(i, j int) bool {
		a, b := st.Quotas[i], st.Quotas[j]
		if a.Instrument != b.Instrument {
			return fmt.Sprint(a.Instrument) < fmt.Sprint(b.Instrument)
		}
		if a.Hedge != b.Hedge {
			return a.Hedge < b.Hedge
		}
		return a.Direction < b.Direction
	})
	return st, nil
}

// Restore 从存档还原一个模拟器。cfg 提供规则输入（规则数据、口径、日历、取整方向、限仓），必须与存档一致。
//
// 查什么（design.md「门面的形状」§9）：格式版本；口径逐项相等、规则版本相等、交易日相等；
// 持仓与挂单的合约在规则里、PositionDateType 一致；明细逐片（position.Restore，昨仓片先于今仓片）；
// 账户（account.Restore）；挂单冻结合计 = 账户冻结、持仓侧冻住不超过持仓；
// 最后**重算核对**：恢复出来的持仓与计价重算的占用与持仓盈亏，必须等于存档里的账户数。
func Restore(cfg Config, st State) (*Simulator, error) {
	if st.Format != StateFormat {
		return nil, fmt.Errorf("存档格式 %d，本库只认 %d —— 不迁移", st.Format, StateFormat)
	}
	s, err := New(Config{Day: st.Day, PreBalance: decimal.Zero, Rules: cfg.Rules, Choices: cfg.Choices,
		Calendar: cfg.Calendar, TickRounding: cfg.TickRounding, PositionLimits: cfg.PositionLimits})
	if err != nil {
		return nil, err
	}
	if cfg.Choices != st.Choices {
		return nil, fmt.Errorf("口径与存档不同：配置 %+v，存档 %+v", cfg.Choices, st.Choices)
	}
	if v := cfg.Rules.Version(); v != st.RulesVersion {
		return nil, fmt.Errorf("规则数据版本 %d 与存档的 %d 不同 —— 隐式基线跟规则数据走，跨版本恢复算出来的数不可信", v, st.RulesVersion)
	}
	if cfg.Day != st.Day {
		return nil, fmt.Errorf("配置的交易日 %d 与存档的 %d 不同", cfg.Day, st.Day)
	}
	if st.Account.Day != st.Day {
		return nil, fmt.Errorf("存档里账户交易日 %d 与存档交易日 %d 不同", st.Account.Day, st.Day)
	}
	if st.Account.Algorithm != st.Choices.Algorithm {
		return nil, fmt.Errorf("存档里账户的盈亏算法 %v 与口径 %v 不同", st.Account.Algorithm, st.Choices.Algorithm)
	}

	acc, err := account.Restore(st.Account)
	if err != nil {
		return nil, err
	}
	s.acc = acc

	for _, ps := range st.Positions {
		inst, err := cfg.Rules.Instrument(ps.Instrument)
		if err != nil {
			return nil, fmt.Errorf("存档里的持仓 %s：%w", ps.Instrument, err)
		}
		if inst.PositionDateType != ps.DateType {
			return nil, fmt.Errorf("存档里 %s 的 PositionDateType %v 与规则数据 %v 不同", ps.Instrument, ps.DateType, inst.PositionDateType)
		}
		if ps.Day != st.Day {
			return nil, fmt.Errorf("存档里 %s 的持仓交易日 %d 与存档交易日 %d 不同", ps.Instrument, ps.Day, st.Day)
		}
		key := posKey{ps.Instrument, ps.Hedge}
		if _, dup := s.positions[key]; dup {
			return nil, fmt.Errorf("存档里 %s 的持仓出现两次", ps.Instrument)
		}
		p, err := position.Restore(ps.Instrument, ps.Hedge, ps.Day, ps.DateType, ps.Long, ps.Short)
		if err != nil {
			return nil, err
		}
		s.positions[key] = p
	}

	// 额度计数（§15）：非负、显式 ≤ 已收平今 ≤ 当日开仓；该方向今仓不多于当日开仓（今仓都来自今天的开仓）
	for _, q := range st.Quotas {
		if _, err := cfg.Rules.Instrument(q.Instrument); err != nil {
			return nil, fmt.Errorf("存档里的额度计数 %s：%w", q.Instrument, err)
		}
		if q.Direction != types.Buy && q.Direction != types.Sell {
			return nil, fmt.Errorf("存档里 %s 的额度计数方向 %v 认不得", q.Instrument, q.Direction)
		}
		if q.ExplicitToday < 0 || q.ExplicitToday > q.ChargedToday || q.ChargedToday > q.OpenedToday {
			return nil, fmt.Errorf("存档里 %s %v 的额度计数不成立：要 0 ≤ 显式平今 %d ≤ 已收平今 %d ≤ 当日开仓 %d",
				q.Instrument, q.Direction, q.ExplicitToday, q.ChargedToday, q.OpenedToday)
		}
		k := quotaKey{q.Instrument, q.Hedge, q.Direction}
		if _, dup := s.quotas[k]; dup {
			return nil, fmt.Errorf("存档里 %s %v 的额度计数出现两次", q.Instrument, q.Direction)
		}
		s.quotas[k] = quotaCount{Opened: q.OpenedToday, Charged: q.ChargedToday, Explicit: q.ExplicitToday}
	}
	for key, p := range s.positions {
		for _, dir := range []types.Direction{types.Buy, types.Sell} {
			if today, opened := p.VolumeToday(dir), s.quotaOf(key.inst, key.hedge, dir).Opened; today > opened {
				return nil, fmt.Errorf("存档里 %s %v 有今仓 %d 手而当日开仓计数只有 %d —— 额度计数丢了或被改过（今仓都来自今天的开仓）",
					key.inst, dir, today, opened)
			}
		}
	}

	for _, px := range st.Prices {
		if _, dup := s.prices[px.Instrument]; dup {
			return nil, fmt.Errorf("存档里 %s 的计价输入出现两次", px.Instrument)
		}
		if (px.HasLast && !px.Last.IsPositive()) || (px.HasPreSettlement && !px.PreSettlement.IsPositive()) {
			return nil, fmt.Errorf("存档里 %s 的计价输入不为正", px.Instrument)
		}
		s.prices[px.Instrument] = priceState{last: px.Last, hasLast: px.HasLast, pre: px.PreSettlement, hasPre: px.HasPreSettlement}
	}

	for _, o := range st.Orders {
		if _, err := cfg.Rules.Instrument(o.Request.Instrument); err != nil {
			return nil, fmt.Errorf("存档里的挂单 %s：%w", o.ID, err)
		}
		if err := s.book.Insert(o.ID, o.Request, freezeInputOf(o.Frozen)); err != nil {
			return nil, fmt.Errorf("存档里的挂单：%w", err)
		}
		if _, got, _ := s.book.Get(o.ID); fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", o.Frozen) {
			return nil, fmt.Errorf("存档里挂单 %s 的冻结手数 %+v 与按开平标志重算的 %+v 不同", o.ID, o.Frozen, got)
		}
	}
	// 挂单冻结的**金额**逐笔重算（评审 20260915：上一版只按开平标志重算了手数，金额取的是存档自己的数，
	// 挂单与账户的冻结手续费一起改就查不出来）。放在计价恢复之后：昨结算价基准要它。
	for _, o := range st.Orders {
		if err := s.checkOrderFreeze(st.Day, o); err != nil {
			return nil, err
		}
	}
	total := s.book.Total()
	if !total.Margin.Equal(st.Account.FrozenMargin) || !total.Commission.Equal(st.Account.FrozenCommission) {
		return nil, fmt.Errorf("挂单冻结合计（保证金 %s / 手续费 %s）与账户冻结（%s / %s）不同",
			total.Margin, total.Commission, st.Account.FrozenMargin, st.Account.FrozenCommission)
	}
	// ⚠️ 与 PlaceAccepted 的可平量守卫是同一个条件（一个在恢复、一个在入口），没合并 —— 改一处的判据或文案，另一处跟上
	for key, p := range s.positions {
		for _, dir := range []types.Direction{types.Buy, types.Sell} {
			fz := s.book.TotalOf(key.inst, dir)
			if p.VolumeToday(dir) < fz.VolumeToday || p.VolumeHistory(dir) < fz.VolumeHistory {
				return nil, fmt.Errorf("存档里 %s 挂单冻住的手数超过持仓", key.inst)
			}
		}
	}

	// 重算核对：挡「只手改了账户里的一个数」
	v, err := s.value(s.positions, s.prices)
	if err != nil {
		return nil, fmt.Errorf("按存档的持仓与计价重算：%w", err)
	}
	s.groups = v.groups // 恢复出来的模拟器也要能答 MarginGroups（它不进存档，按存档的持仓与计价重算）
	for _, c := range []struct {
		name      string
		got, want decimal.Decimal
	}{
		{"公司占用", v.marginCompany, st.Account.CurrMargin},
		{"交易所占用", v.marginExchange, st.Account.ExchangeMargin},
		{"持仓盈亏", v.positionProfit, st.Account.PositionProfit},
	} {
		if !c.got.Equal(c.want) {
			return nil, fmt.Errorf("存档里的%s %s 与按持仓重算的 %s 不同 —— 存档被改过，或者导出时状态不一致", c.name, c.want, c.got)
		}
	}
	return s, nil
}

// checkOrderFreeze 核一笔存档挂单的冻结金额（以及指定了今昨的平仓单的手数）是否就是这笔委托该冻的。
//
// ⚠️ 只核**与挂单那一刻的持仓无关**的部分。裸 CLOSE 的今昨拆分与手续费档位依赖挂单时的持仓，而持仓在挂单之后可能变了：
// 挂一笔裸平（今1昨1 时冻昨 1、按平今档收）之后再成交一笔平今，账上只剩昨 1 —— 这是合法状态，
// 可在恢复时的状态上重算那笔挂单会得到**另一个数**（当日开仓额度已被那笔平今用掉，按 §13 #23 的 (f) 那笔要改收平昨档；
// 收敛到 (f) 之前这里按的是 (a)「平仓前今仓」—— 两种规则下「重算会得到另一个数」都成立），
// 与存档的冻结对不上。⚠️ 收敛之前这里写的是「会撞 #21 报错」—— 20260917 起不报错，但数仍然不同，所以这层放宽仍然需要。
// ⇒ 裸 CLOSE：手续费只核「今仓部分走平今档、其余走平昨档」的某种拆法
// （commission(tr, k)，k = 0…手数）能算出存档的数；拆分只核合计与持仓（上面已核）。
func (s *Simulator) checkOrderFreeze(day types.TradingDay, o OrderState) error {
	req := o.Request
	inst, err := s.rules.Instrument(req.Instrument)
	if err != nil {
		return fmt.Errorf("存档里的挂单 %s：%w", o.ID, err)
	}
	// UseHistory 上按口径记作平昨的裸 CLOSE 与持仓无关（整份冻昨），走下面的整份重算
	if off := s.datedOffset(inst.PositionDateType, req.Offset); off.IsClose() && !off.SpecifiesPositionDate() {
		if !o.Frozen.Margin.IsZero() {
			return fmt.Errorf("存档里挂单 %s 是裸 CLOSE，却冻了保证金 %s", o.ID, o.Frozen.Margin)
		}
		tr := match.Trade{Instrument: req.Instrument, Direction: req.Direction, Offset: req.Offset, Hedge: req.Hedge, Price: req.Price, Volume: req.Volume}
		for k := 0; k <= req.Volume; k++ {
			k := k
			if c, _, _, err := s.commission(tr, func() (int, error) { return k, nil }); err == nil && c.Equal(o.Frozen.Commission) {
				return nil
			}
		}
		return fmt.Errorf("存档里挂单 %s（裸 CLOSE %d 手）的冻结手续费 %s 不是任何一种今昨拆法能算出来的", o.ID, req.Volume, o.Frozen.Commission)
	}
	// 开仓与指定了今昨的平仓：FreezeOf 与持仓无关（保证金按挂单价或昨结算价、手续费按开平标志），整份重算
	// ⚠️ 此刻这笔挂单已在簿上：FreezeOf 对这两类单不看簿，所以不用先拿下
	want, err := s.FreezeOf(day, req)
	if err != nil {
		return fmt.Errorf("存档里挂单 %s 重算冻结：%w", o.ID, err)
	}
	if !want.Margin.Equal(o.Frozen.Margin) || !want.Commission.Equal(o.Frozen.Commission) {
		return fmt.Errorf("存档里挂单 %s 的冻结（保证金 %s / 手续费 %s）与按委托重算的（%s / %s）不同",
			o.ID, o.Frozen.Margin, o.Frozen.Commission, want.Margin, want.Commission)
	}
	// ⚠️ 手数也核：簿按开平标志重算手数，而记作平昨的裸 CLOSE 在簿上仍是 Close，手数取的是存档自己的今 / 昨拆分
	if want.VolumeToday != o.Frozen.VolumeToday || want.VolumeHistory != o.Frozen.VolumeHistory {
		return fmt.Errorf("存档里挂单 %s 的冻结手数（今 %d / 昨 %d）与按委托重算的（今 %d / 昨 %d）不同",
			o.ID, o.Frozen.VolumeToday, o.Frozen.VolumeHistory, want.VolumeToday, want.VolumeHistory)
	}
	return nil
}

func sortedInstruments(set map[types.InstrumentID]bool) []types.InstrumentID {
	keys := make(map[posKey]*position.Position, len(set))
	for id := range set {
		keys[posKey{inst: id}] = nil
	}
	out := make([]types.InstrumentID, 0, len(set))
	for _, k := range sortedKeys(keys) {
		out = append(out, k.inst)
	}
	return out
}
