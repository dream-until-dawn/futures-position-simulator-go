package futsim

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/pnl"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// valuation 是由「全部持仓 × 全部计价输入」算出的截面量。
type valuation struct {
	marginCompany, marginExchange decimal.Decimal
	positionProfit                decimal.Decimal
	// groups 是 margin.Compute 的分组分解（逐组多空两边），原样留着给 MarginGroups。
	//
	// ⚠️ 不是新算一遍：margin 包本来就返回它，门面此前把它丢掉了，于是对拍那侧只好自己再把持仓翻译成 leg —— 那层翻译才是 F8 收掉的重复。
	groups []margin.GroupResult
}

// value 对一组持仓与计价输入算截面。**纯函数**：不读不写 s 的状态，只读规则数据与口径。
//
// ⚠️ 任何一个有仓的合约缺计价价或昨结算价都报错 —— 不拿别的价顶，也不把那个合约当成「不盈不亏」跳过：
// 跳过的合约会以零计入合计，而合计看起来完全正常。
func (s *Simulator) value(positions map[posKey]*position.Position, prices map[types.InstrumentID]priceState) (valuation, error) {
	var v valuation
	var legs []margin.Leg
	for _, k := range sortedKeys(positions) {
		p := positions[k]
		if p.IsFlat() {
			continue
		}
		inst, err := s.rules.Instrument(k.inst)
		if err != nil {
			return v, err
		}
		rates, err := s.rules.MarginRates(k.inst, k.hedge)
		if err != nil {
			return v, err
		}
		px := prices[k.inst]
		if !px.hasLast || !px.hasPre {
			return v, fmt.Errorf("%s 有持仓而缺计价输入（最新价 %v / 昨结算价 %v）—— 先 Mark", k.inst, px.hasLast, px.hasPre)
		}
		for _, dir := range []types.Direction{types.Buy, types.Sell} {
			side, err := p.Side(dir)
			if err != nil {
				return v, err
			}
			if side.Volume() == 0 {
				continue
			}
			var pl []pnl.Leg
			for _, l := range side.Lots() {
				legs = append(legs, margin.Leg{
					Instrument: k.inst, Direction: dir, Volume: l.Volume,
					Multiplier: inst.VolumeMultiple, Rates: rates,
					IsHistory: l.Settled, MaxMarginSide: inst.MaxMarginSide,
					OpenPrice:     l.OpenPrice,
					PreSettlement: px.pre, HasPreSettlement: true,
					Last: px.last, HasLast: true,
				})
				pl = append(pl, pnl.Leg{Volume: l.Volume, OpenPrice: l.OpenPrice, Basis: l.Basis})
			}
			pp, err := pnl.PositionProfit(pl, dir,
				pnl.Prices{Last: px.last, HasLast: true, PreSettlement: px.pre, HasPreSettlement: true},
				s.choices.Mark, inst.VolumeMultiple)
			if err != nil {
				return v, fmt.Errorf("%s 持仓盈亏：%w", k.inst, err)
			}
			v.positionProfit = v.positionProfit.Add(pp)
		}
	}
	if len(legs) == 0 {
		return v, nil // 空仓：没有占用，也没有持仓盈亏
	}
	res, err := margin.Compute(legs, s.choices.MarginBasis, s.choices.SideScope)
	if err != nil {
		return v, fmt.Errorf("保证金：%w", err)
	}
	v.marginCompany, v.marginExchange, v.groups = res.Company, res.Exchange, res.Groups
	return v, nil
}

// Mark 更新一个合约的计价输入，并按新价重算占用与持仓盈亏。
//
// ⚠️ 昨结算价一个交易日内不变：已经给过而这次给的不同 ⇒ 报错（多半是换了交易日没结算，或者喂错了合约）。
func (s *Simulator) Mark(day types.TradingDay, q Quote) error {
	if err := s.usable(day); err != nil {
		return err
	}
	if q.HasLast && !q.Last.IsPositive() {
		return fmt.Errorf("%s 最新价 %s 不为正 —— 没有这个价请把 HasLast 置假", q.Instrument, q.Last)
	}
	if q.HasPreSettlement && !q.PreSettlement.IsPositive() {
		return fmt.Errorf("%s 昨结算价 %s 不为正 —— 零只可能是缺失的伪装（§6.5）", q.Instrument, q.PreSettlement)
	}
	old := s.prices[q.Instrument]
	next := old
	if q.HasLast {
		next.last, next.hasLast = q.Last, true
	}
	if q.HasPreSettlement {
		if old.hasPre && !old.pre.Equal(q.PreSettlement) {
			return fmt.Errorf("%s 昨结算价在同一交易日 %d 里从 %s 变成 %s", q.Instrument, day, old.pre, q.PreSettlement)
		}
		next.pre, next.hasPre = q.PreSettlement, true
	}
	prices := make(map[types.InstrumentID]priceState, len(s.prices)+1)
	for k, v := range s.prices {
		prices[k] = v
	}
	prices[q.Instrument] = next
	v, err := s.value(s.positions, prices)
	if err != nil {
		return err
	}
	s.prices = prices
	return s.commit(day, v, decimal.Zero, decimal.Zero)
}

// ApplyTrade 把一笔**已经发生**的成交记进持仓与资金（灌成交路径，不跑八项校验）。
//
// 一步之内：持仓（副本上）→ 手续费 → 平仓盈亏（逐日盯市）→ 全部持仓重算占用与持仓盈亏 → 换进去、写账户。
// 换进去之前的任何失败，状态一点不动。
func (s *Simulator) ApplyTrade(day types.TradingDay, tr match.Trade) error {
	if err := s.usable(day); err != nil {
		return err
	}
	inst, err := s.rules.Instrument(tr.Instrument)
	if err != nil {
		return err
	}
	key := posKey{tr.Instrument, tr.Hedge}

	var p *position.Position
	if cur, ok := s.positions[key]; ok {
		p = cur.Clone()
	} else if p, err = position.New(tr.Instrument, tr.Hedge, day, inst.PositionDateType); err != nil {
		return err
	}

	commission, closeProfit := decimal.Zero, decimal.Zero
	todayLots := 0 // 这一笔按平今档收了几手（额度记账用，§15）

	switch {
	case tr.Offset == types.Open:
		if err := p.Open(tr.Direction, day, tr.Price, tr.Volume); err != nil {
			return err
		}
		if commission, _, err = s.commission(tr, nil); err != nil {
			return err
		}
	case tr.Offset.IsClose():
		tr.Offset = s.datedOffset(inst.PositionDateType, tr.Offset) // UseHistory 上的裸 CLOSE 按口径改写成平昨
		held := opposite(tr.Direction)                              // 卖出平仓平的是多头
		order := position.CloseOrderUnmeasured
		if !tr.Offset.SpecifiesPositionDate() {
			o, ok := position.MeasuredCloseOrder(p.DateType())
			if !ok {
				return fmt.Errorf("%s 上的 %v 没有实测的消耗顺序（PositionDateType %v）—— 显式给平今或平昨",
					tr.Instrument, tr.Offset, p.DateType())
			}
			order = o
		}
		var res position.CloseResult
		if tr.Offset.SpecifiesPositionDate() {
			if res, err = p.Close(held, tr.Offset, day, tr.Volume, order); err != nil {
				return err
			}
		} else {
			// ⚠️ 裸 CLOSE：在**没被挂单冻住**的今昨里先平昨 —— 与冻结时的拆分同一个 undatedSplit（评审 20260915 打回后改）。
			// 原来按 MeasuredCloseOrder 在整份持仓上消耗：已有挂单冻住昨仓时，这一笔会去平那手昨仓、被下面的守卫拒掉，
			// 于是「今1昨1 挂两笔裸平、先成交冻今的那笔」成交不了。没有挂单时与先平昨逐片相同（§13 #4）。
			if order != position.YesterdayFirst {
				return fmt.Errorf("%s 上裸 CLOSE 的实测消耗顺序是 %v，本库只按先平昨拆", tr.Instrument, order)
			}
			if _, err := p.Side(held); err != nil {
				return err
			}
			t, h, err := undatedSplit(tr.Instrument, tr.Volume, p.VolumeToday(held), p.VolumeHistory(held), s.book.TotalOf(tr.Instrument, held))
			if err != nil {
				return err
			}
			for _, part := range []struct {
				off types.Offset
				vol int
			}{{types.CloseYesterday, h}, {types.CloseToday, t}} {
				if part.vol == 0 {
					continue
				}
				r, err := p.Close(held, part.off, day, part.vol, position.CloseOrderUnmeasured)
				if err != nil {
					return err
				}
				res.Consumed = append(res.Consumed, r.Consumed...)
				res.VolumeToday += r.VolumeToday
				res.VolumeHistory += r.VolumeHistory
			}
		}
		// ⚠️ 平完之后剩下的今 / 昨仓不能少于挂着的平仓单冻住的手数（F4）：
		// 柜台不会让一笔成交与挂单冲突；冲突只可能是调用方把挂单的成交走了 ApplyTrade 而不是 Fill
		if fz := s.book.TotalOf(tr.Instrument, held); p.VolumeToday(held) < fz.VolumeToday || p.VolumeHistory(held) < fz.VolumeHistory {
			return fmt.Errorf("%s 这笔平仓会平掉挂单冻住的手数（平后 今 %d / 昨 %d，挂单冻住 今 %d / 昨 %d）—— 挂单的成交走 Fill",
				tr.Instrument, p.VolumeToday(held), p.VolumeHistory(held), fz.VolumeToday, fz.VolumeHistory)
		}
		if commission, todayLots, err = s.commission(tr, s.undatedCap(tr.Instrument, tr.Hedge, held, tr.Volume)); err != nil {
			return err
		}
		legs := make([]pnl.Leg, 0, len(res.Consumed))
		for _, l := range res.Consumed {
			legs = append(legs, pnl.Leg{Volume: l.Volume, OpenPrice: l.OpenPrice, Basis: l.Basis})
		}
		cp, err := pnl.CloseProfit(legs, held, tr.Price, inst.VolumeMultiple)
		if err != nil {
			return fmt.Errorf("%s 平仓盈亏：%w", tr.Instrument, err)
		}
		closeProfit = cp.ByDate // 逐日盯市口径进结存（§5）
	default:
		return fmt.Errorf("%s 开平标志 %v 认不得", tr.Instrument, tr.Offset)
	}

	positions := make(map[posKey]*position.Position, len(s.positions)+1)
	for k, v := range s.positions {
		positions[k] = v
	}
	positions[key] = p
	v, err := s.value(positions, s.prices)
	if err != nil {
		return err
	}
	s.positions = positions
	s.bookQuota(tr, todayLots)
	return s.commit(day, v, commission, closeProfit)
}

// datedOffset 按口径 UndatedCloseOnUseHistory 改写开平标志：UseHistory 合约上的裸 CLOSE ⇒ 平昨，其余原样。
//
// 记账路径（ApplyTrade、FreezeOf）进门先过它，commission 收到的已是改写后的 —— 各判各的，就会冻昨仓、平今仓、收平今档。
// ⚠️ 只改 types.Close：强平标志也不指定今昨，但 kq_facts 32 只观测过 CLOSE。
func (s *Simulator) datedOffset(dt refdata.PositionDateType, off types.Offset) types.Offset {
	if off == types.Close && dt == refdata.UseHistory && s.choices.UndatedCloseOnUseHistory == UndatedCloseAsYesterday {
		return types.CloseYesterday
	}
	return off
}

// undatedSplit 把一笔裸 CLOSE 拆成今 / 昨手数：在**扣掉挂单冻住之后**的今昨里先平昨。
//
// 冻结（FreezeOf）与成交（ApplyTrade）共用它 —— 两处各拆各的，挂着的单就会冻一边、成交时平另一边
// （评审 20260915 打回 F4 的两处缺陷都是这个形状）。合计超过可平量报错。
// ⚠️ 「先平昨」只在 NoUseHistory 上有实测（§13 #4，且只观测过没有挂单时）；有挂单时在可平量里先平昨是推得。
func undatedSplit(inst types.InstrumentID, volume, today, history int, frozen order.Frozen) (toToday, toHistory int, err error) {
	freeToday, freeHistory := today-frozen.VolumeToday, history-frozen.VolumeHistory
	toHistory = min(volume, max(freeHistory, 0))
	toToday = volume - toHistory
	if toToday > max(freeToday, 0) {
		return 0, 0, fmt.Errorf("%s 裸 CLOSE %d 手超过可平今 %d / 昨 %d（已扣挂单冻住的 今 %d / 昨 %d）",
			inst, volume, max(freeToday, 0), max(freeHistory, 0), frozen.VolumeToday, frozen.VolumeHistory)
	}
	return toToday, toHistory, nil
}

// commission 算一笔成交（或一笔报单按报价成交时）的手续费，并返回其中按平今档收了几手（额度记账用）。
//
// undatedCap 只对裸 CLOSE 有用：两档费率不同时调用它取平今档手数（§13 #23 的 (f)，ApplyTrade / FreezeOf 传 undatedCap，
// 恢复时核挂单冻结传固定的 k）；两档相同时不调用（不论哪种读法都同值，不该因为额度读法分岔而报错）。开仓与显式今昨传 nil。
// ApplyTrade 与 FreezeOf 共用它：「这一笔收多少」只有一份定义。
func (s *Simulator) commission(tr match.Trade, undatedCap func() (int, error)) (decimal.Decimal, int, error) {
	inst, err := s.rules.Instrument(tr.Instrument)
	if err != nil {
		return decimal.Zero, 0, err
	}
	rates, err := s.rules.CommissionRates(tr.Instrument, tr.Hedge)
	if err != nil {
		return decimal.Zero, 0, err
	}
	px := s.prices[tr.Instrument]
	feePrice, err := fee.BasisPrice(s.choices.FeeBasis, tr.Price, px.pre, px.hasPre)
	if err != nil {
		return decimal.Zero, 0, fmt.Errorf("%s：%w", tr.Instrument, err)
	}
	total := decimal.Zero
	charge := func(off types.Offset, vol int) error {
		if vol == 0 {
			return nil
		}
		c, err := fee.Compute(rates, off, feePrice, inst.VolumeMultiple, vol, s.choices.FeeRounding)
		if err != nil {
			return fmt.Errorf("%s 手续费：%w", tr.Instrument, err)
		}
		total = total.Add(c)
		return nil
	}
	todayLots := 0
	switch {
	case tr.Offset == types.Open:
		err = charge(types.Open, tr.Volume)
	case tr.Offset.SpecifiesPositionDate():
		err = charge(tr.Offset, tr.Volume)
		if tr.Offset == types.CloseToday {
			todayLots = tr.Volume
		}
	default:
		if undatedCap == nil {
			return decimal.Zero, 0, fmt.Errorf("%s 裸 CLOSE 没给额度 —— 调用方漏传（内部错误）", tr.Instrument)
		}
		if todayLots, err = chargeUndated(tr, undatedCap, rates, charge); err != nil {
			return decimal.Zero, 0, err
		}
	}
	if err != nil {
		return decimal.Zero, 0, err
	}
	return total, todayLots, nil
}

// chargeUndated 给裸 CLOSE 与强平标志收手续费，返回按平今档收了几手 —— §13 #23 的 (f)（20260918 夜盘收敛，CTP 单柜台，
// 大商所与郑商所各一次事前登记的三笔，ctp-slices-20260921{,-2..-10}）：
//
//	平今档手数 = min(平仓量, 当日开仓量 − 当日已按平今档收过的手数)，其余走平昨档 —— 与平的是哪一片、平仓前今仓几手都无关
//
// 额度由 cap 给（todayTierLots；三处没实测的外推在那里报错不猜，design.md 门面形状 §15）。
//
// ⚠️ 它替换的是 §13 #21 在大商所收敛的 (a)「min(平仓量, 平仓前今仓量)」：两者在大商所此前的全部观测上同值，
// 20260918 夜盘第 3 笔（额度用完之后平剩下的今仓）第一次分岔，柜台收平昨档（silent-risks 100）。
// ⚠️ E2（大商所显式平昨）在成交记录里被改写成 '1'，从成交记录重放时它就是一笔裸 CLOSE。
//
// ⚠️⚠️ **范围：大商所与郑商所**（undatedCloseMeasuredOn）。其余 NoUseHistory 交易所（广期所等）在两档费率不同时**整笔报错不猜**。
// 两档相同时不问额度：不论哪种读法都同值，按平昨档收、记 0 手平今。
// ⚠️ 只有一个柜台 ⇒ 不进 rules_measured。
func chargeUndated(tr match.Trade, cap func() (int, error), rates refdata.CommissionRates, charge func(types.Offset, int) error) (int, error) {
	tiersDiffer := !rates.CloseTodayByMoney.Equal(rates.CloseByMoney) || !rates.CloseTodayByVolume.Equal(rates.CloseByVolume)
	if !tiersDiffer {
		return 0, charge(types.CloseYesterday, tr.Volume)
	}
	if !undatedCloseMeasuredOn(tr.Instrument.Exchange) {
		return 0, fmt.Errorf("%s 的 %v %d 手：平今档与平昨档费率不同，而 %s 上裸平收哪一档没有观测 —— "+
			"§13 #23 的 (f) 只在大商所、郑商所实测过，不猜", tr.Instrument, tr.Offset, tr.Volume, tr.Instrument.Exchange)
	}
	today, err := cap()
	if err != nil {
		return 0, err
	}
	if err := charge(types.CloseToday, today); err != nil {
		return 0, err
	}
	if rest := tr.Volume - today; rest > 0 {
		if err := charge(types.CloseYesterday, rest); err != nil {
			return 0, err
		}
	}
	return today, nil
}

// undatedCloseMeasuredOn 报告 §13 #23 的 (f) 在这个交易所上**有没有观测**。
//
// ⚠️ 单独立一个函数，是为了让「范围」这件事在代码里只有一处：将来补测了广期所，改的是这里，
// 而不是在 chargeUndated 里再加一个 || —— 那样范围就散在判断条件里，读不出「它是一张实测名单」。
func undatedCloseMeasuredOn(ex types.Exchange) bool {
	return ex == types.DCE || ex == types.CZCE
}

// bookQuota 在一笔成交换进去之后记额度：开仓加当日开仓量；按平今档收了几手就加几手（显式平今另记一份）。
func (s *Simulator) bookQuota(tr match.Trade, todayLots int) {
	if s.quotas == nil {
		s.quotas = map[quotaKey]quotaCount{}
	}
	if tr.Offset == types.Open {
		k := quotaKey{tr.Instrument, tr.Hedge, tr.Direction}
		q := s.quotas[k]
		q.Opened += tr.Volume
		s.quotas[k] = q
		return
	}
	if todayLots == 0 {
		return
	}
	k := quotaKey{tr.Instrument, tr.Hedge, opposite(tr.Direction)}
	q := s.quotas[k]
	q.Charged += todayLots
	if tr.Offset == types.CloseToday {
		q.Explicit += todayLots
	}
	s.quotas[k] = q
}

// commit 把算好的数写进账户（并留下这次计价的分组分解）。⚠️ 走到这里状态已经换进去了：任何失败都让模拟器失效。
func (s *Simulator) commit(day types.TradingDay, v valuation, commission, closeProfit decimal.Decimal) error {
	s.groups = v.groups
	steps := []func() error{
		func() error { return s.acc.AddCommission(day, commission) },
		func() error { return s.acc.AddCloseProfit(day, closeProfit) },
		func() error { return s.acc.SetMargin(day, v.marginCompany, v.marginExchange) },
		func() error { return s.acc.SetPositionProfit(day, v.positionProfit) },
		s.acc.Check,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			s.broken = err
			return fmt.Errorf("⚠️ 持仓已更新而账户写入失败，模拟器失效：%w", err)
		}
	}
	return nil
}

func opposite(d types.Direction) types.Direction {
	if d == types.Buy {
		return types.Sell
	}
	return types.Buy
}
