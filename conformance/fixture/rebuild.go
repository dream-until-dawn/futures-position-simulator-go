package fixture

import (
	"fmt"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/pnl"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Spec 是重建一个合约所需的规则数据。
//
// ⚠️ 全部字段都必须由调用方给，**没有默认值**：
// 一个「差不多能用」的规格会在平今平昨和大边上静默算错
// （refdata.Builder 的零值报错也是这个理由）。
type Spec struct {
	Multiplier    decimal.Decimal
	Commission    refdata.CommissionRates
	Margin        refdata.MarginRates
	MaxMarginSide bool
}

// Rebuilt 是重建的结果。
//
// ⚠️ FloatProfit 单独放在这里而不是塞进 account.Snapshot，
// 是因为**本库的账户快照里根本没有它** —— 那不是疏漏，是 view.AccountInput
// 的注释里写着的事实：浮动盈亏是逐笔对冲口径，账户侧不承载它，
// 由调用方从 pnl 侧提供。
//
// ⚠️ 而它正是本项目核心那对区分的另一半：
// account.Snapshot.PositionProfit 是逐日盯市，这里的 FloatProfit 是逐笔对冲。
type Rebuilt struct {
	Account account.Snapshot
	// FloatProfit 是全部持仓的浮动盈亏合计（逐笔对冲口径）。
	// HasFloatProfit 为假表示**算不出**（缺最新价），那是「没有」不是「零」。
	FloatProfit    decimal.Decimal
	HasFloatProfit bool
}

// Rebuild 从一份夹具**重建整个账户**，然后与柜台的账户截面逐字段比。
//
// ⚠️ 2026-09-15 起它**经过门面**（`futsim.Simulator`）：记账链条只在门面里有一份。
// 此前这里自己串了一遍「手续费 → 重放 → 平仓盈亏 → 占用 / 持仓盈亏 → 账户」，
// 而门面若照着另写一份，两份一起退化时这条对拍全绿（design.md「门面的形状」开头）。
//
// 链条（由门面执行）：
//
//	pre_balance（夹具给的起点）
//	  → 出入金
//	  → 每个成交过的合约 Mark（持仓截面的最新价 + 昨结算价）
//	  → 逐笔 ApplyTrade：持仓、手续费、平仓盈亏、全部持仓重算占用与持仓盈亏
//	  → account 自己推出 balance / available / risk_ratio
//
// ⚠️ 哪些字段有判别力、哪些是同义反复，必须分清：
//
//	同义反复  pre_balance / static_balance / deposit / withdraw
//	          —— 它们是**输入**，或由输入平凡推出；比它们等于比自己抄的数
//	有判别力  commission / close_profit / position_profit / margin
//	          —— 本库自己算的
//	真正的验收 balance / available / risk_ratio
//	          —— 由上面那几个**再推一层**，错一处就全错
//
// ⚠️ 本函数**不处理昨仓**：有昨仓的合约要先走 Carry。
// 调用方用 Fixture.HasHistoryPosition 判，这里只在撞上时报错。
func Rebuild(f *Fixture, specs map[string]Spec) (Rebuilt, error) {
	var zero Rebuilt
	pre, ok := numberOf(f.Account, "pre_balance")
	if !ok {
		return zero, fmt.Errorf("夹具 %s 的账户截面里没有 pre_balance —— "+
			"⚠️ 那是整条资金链的起点，没有它重建出来的每一个数都是错的", f.Path)
	}
	// —— 挂单冻结 ——
	//
	// ⚠️ 原先这里有一支按夹具委托冻结的代码，**在现有语料上是死的**：记了委托的夹具（20260909 起）全都带昨仓，
	// 而本函数拒绝昨仓 —— 所以改成明说「做不了」，而不是悄悄不冻。冻结的对拍走 FrozenBook（F6b）。
	if f.HasOrders {
		return zero, fmt.Errorf("夹具 %s 记了委托 —— 本函数不接挂单冻结，重建出来的可用必然错，不重建", f.Path)
	}

	var syms []string
	rules := specRules{version: 1, byID: map[types.InstrumentID]Spec{}}
	for _, sym := range f.Symbols() {
		trades := f.TradesOf(sym)
		if len(trades) == 0 {
			continue
		}
		if f.HasHistoryPosition(sym) {
			return zero, fmt.Errorf("⚠️ %s 有昨仓，而本函数只重放当日成交 —— "+
				"昨仓那几手今天的成交里没有一笔能解释它，先走 Carry", sym)
		}
		spec, ok := specs[sym]
		if !ok {
			return zero, fmt.Errorf("合约 %s 没有规格 —— "+
				"⚠️ 不填默认值：一个「差不多能用」的规格会静默算错", sym)
		}
		rules.byID[trades[0].Instrument] = spec
		syms = append(syms, sym)
	}

	sim, err := futsim.New(futsim.Config{Day: f.TradingDay, PreBalance: pre, Rules: rules, Choices: fixtureChoices()})
	if err != nil {
		return zero, err
	}

	// —— 入金出金 ——
	//
	// ⚠️ 照实取，不假设为零：本批夹具里它们确实都是 0，
	// 而「恒为 0 的减项不会暴露自己被漏掉」正是 §13 第 12 条。
	if d, ok := numberOf(f.Account, "deposit"); ok && d.IsPositive() {
		if err := sim.Deposit(f.TradingDay, d); err != nil {
			return zero, err
		}
	}
	if w, ok := numberOf(f.Account, "withdraw"); ok && w.IsPositive() {
		if err := sim.Withdraw(f.TradingDay, w); err != nil {
			return zero, err
		}
	}

	// —— 计价输入：先给齐，再灌成交 ——
	//
	// ⚠️ 夹具只有**截面那一刻**的最新价。中间每一步的持仓盈亏因此是按最终价算的 ——
	// 它们在下一步被覆盖，最后一次重算才是账户里留下的那个数，所以不影响对拍。
	for _, sym := range syms {
		preSettle, ok := f.PreSettlement(sym)
		if !ok {
			return zero, fmt.Errorf("合约 %s 没有昨结算价 —— "+
				"⚠️ 手续费基准、保证金基准都要它；这份夹具不自足", sym)
		}
		q := futsim.Quote{Instrument: f.TradesOf(sym)[0].Instrument, PreSettlement: preSettle, HasPreSettlement: true}
		if last, ok := f.Positions[sym]["last_price"]; ok && !last.Absent && !last.IsText && last.Number.IsPositive() {
			q.Last, q.HasLast = last.Number, true
		}
		if err := sim.Mark(f.TradingDay, q); err != nil {
			return zero, fmt.Errorf("%s 计价：%w", sym, err)
		}
	}
	for _, sym := range syms {
		for _, tr := range f.TradesOf(sym) {
			if err := sim.ApplyTrade(f.TradingDay, match.Trade{
				Instrument: tr.Instrument, Direction: tr.Direction, Offset: tr.Offset,
				Hedge: types.Speculation, Price: tr.Price, Volume: tr.Volume,
			}); err != nil {
				return zero, fmt.Errorf("%s 成交 %s：%w", sym, tr.TradeID, err)
			}
		}
	}

	// —— 浮动盈亏（逐笔对冲口径）——
	//
	// ⚠️ 账户里没有它（view.AccountInput 的注释），由这里从门面的持仓算。
	// 它不进结存，所以不是记账链条的第二份实现。
	totalFloatProfit := decimal.Zero
	for _, sym := range syms {
		p, ok := sim.Position(f.TradesOf(sym)[0].Instrument, types.Speculation)
		if !ok || p.IsFlat() {
			continue
		}
		fp, err := floatProfitOf(f, sym, p, specs[sym].Multiplier)
		if err != nil {
			// ⚠️ 缺最新价时**不当成零**：那会让一个算不出的合约
			// 悄悄按「不盈不亏」计入合计，而合计看起来完全正常。
			return zero, fmt.Errorf("%s 盈亏：%w", sym, err)
		}
		totalFloatProfit = totalFloatProfit.Add(fp)
	}
	return Rebuilt{
		Account:     sim.Account(),
		FloatProfit: totalFloatProfit, HasFloatProfit: true,
	}, nil
}

// fixtureChoices 是快期夹具走门面时的口径：快期预设，外加两格**这条路的调用方**签的（不是快期的实测）——
//
//	取整 §13 #5 未收敛；在本批费率上「不取整」与「取到三位」同值
//	大边范围在快期上测不了（没实现大边，kq_facts 5），本批 spec.MaxMarginSide 全为假，范围取哪个都不起作用
//
// Rebuild 与 FrozenBook 共用它：两处各签一份，签的内容就可能分岔。
func fixtureChoices() futsim.Choices {
	ch := futsim.KQChoices()
	ch.FeeRounding = fee.NoRounding
	ch.SideScope = margin.ByInstrument
	return ch
}

// specRules 把 Rebuild 的规格表包成 refdata.Provider，喂给门面。
//
// ⚠️ PositionDateType 给 PositionDateNotNeeded：本路径**永不结算**（有昨仓的合约在上面就报错了），
// 那是调用方的声明，不是合约的属性 —— 所以这里不走 refdata.Builder（它按规则数据的标准拒绝这个值）。
// 编一个 UseHistory / NoUseHistory 会被后来的人当成实测值；NotNeeded 下裸 CLOSE 会被门面拒绝，
// 而本批夹具的成交只有 OPEN / CLOSETODAY。
//
// dates 给**实测过**的 PositionDateType（FrozenBook 用：UseHistory 上的裸 CLOSE 要它）；没有的合约照旧 PositionDateNotNeeded。
type specRules struct {
	version int64
	byID    map[types.InstrumentID]Spec
	dates   map[types.InstrumentID]refdata.PositionDateType
}

func (r specRules) spec(id types.InstrumentID) (Spec, error) {
	s, ok := r.byID[id]
	if !ok {
		return Spec{}, fmt.Errorf("合约 %s 没有规格", id)
	}
	return s, nil
}

func (r specRules) Instrument(id types.InstrumentID) (refdata.Instrument, error) {
	s, err := r.spec(id)
	if err != nil {
		return refdata.Instrument{}, err
	}
	dt := refdata.PositionDateNotNeeded
	if d, ok := r.dates[id]; ok {
		dt = d
	}
	return refdata.Instrument{ID: id, VolumeMultiple: s.Multiplier,
		PositionDateType: dt, MaxMarginSide: s.MaxMarginSide}, nil
}

func (r specRules) MarginRates(id types.InstrumentID, _ types.HedgeFlag) (refdata.MarginRates, error) {
	s, err := r.spec(id)
	return s.Margin, err
}

func (r specRules) CommissionRates(id types.InstrumentID, _ types.HedgeFlag) (refdata.CommissionRates, error) {
	s, err := r.spec(id)
	return s.Commission, err
}

func (r specRules) ProductInstruments(types.Exchange, string) []types.InstrumentID { return nil }

func (r specRules) Version() int64 { return r.version }

func toLegs(lots []position.Lot) []pnl.Leg {
	out := make([]pnl.Leg, 0, len(lots))
	for _, l := range lots {
		out = append(out, pnl.Leg{Volume: l.Volume, OpenPrice: l.OpenPrice, Basis: l.Basis})
	}
	return out
}

// floatProfitOf 用夹具持仓截面的最新价算浮动盈亏（逐笔对冲口径，基线是 Lot.OpenPrice）。
//
// ⚠️ 持仓盈亏（逐日盯市）由门面算、进账户；这里只算不进结存的那一栏。
// 今仓上两者必然相等（两条基线都是开仓价），分开只发生在昨仓上 —— 那正是本项目最核心的那条区分。
func floatProfitOf(f *Fixture, sym string, p *position.Position, multiplier decimal.Decimal) (decimal.Decimal, error) {
	last, ok := f.Positions[sym]["last_price"]
	if !ok || last.Absent || last.IsText || !last.Number.IsPositive() {
		return decimal.Zero, fmt.Errorf("没有最新价 —— 这是「没有」不是「零」")
	}
	prices := pnl.Prices{Last: last.Number, HasLast: true}
	total := decimal.Zero
	for _, d := range []types.Direction{types.Buy, types.Sell} {
		s, err := p.Side(d)
		if err != nil {
			return decimal.Zero, err
		}
		if s.Volume() == 0 {
			continue
		}
		fp, err := pnl.FloatProfit(toLegs(s.Lots()), d, prices, pnl.MarkLast, multiplier)
		if err != nil {
			return decimal.Zero, err
		}
		total = total.Add(fp)
	}
	return total, nil
}

func numberOf(m map[string]Value, key string) (decimal.Decimal, bool) {
	v, ok := m[key]
	if !ok || v.Absent || v.IsText {
		return decimal.Zero, false
	}
	return v.Number, true
}
