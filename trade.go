package futsim

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
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
	v.marginCompany, v.marginExchange = res.Company, res.Exchange
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

	switch {
	case tr.Offset == types.Open:
		if err := p.Open(tr.Direction, day, tr.Price, tr.Volume); err != nil {
			return err
		}
		if commission, err = s.commission(tr, 0); err != nil {
			return err
		}
	case tr.Offset.IsClose():
		held := opposite(tr.Direction) // 卖出平仓平的是多头
		order := position.CloseOrderUnmeasured
		if !tr.Offset.SpecifiesPositionDate() {
			o, ok := position.MeasuredCloseOrder(p.DateType())
			if !ok {
				return fmt.Errorf("%s 上的 %v 没有实测的消耗顺序（PositionDateType %v）—— 显式给平今或平昨",
					tr.Instrument, tr.Offset, p.DateType())
			}
			order = o
		}
		todayBefore := p.VolumeToday(held)
		res, err := p.Close(held, tr.Offset, day, tr.Volume, order)
		if err != nil {
			return err
		}
		if commission, err = s.commission(tr, todayBefore); err != nil {
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
	return s.commit(day, v, commission, closeProfit)
}

// commission 算一笔成交（或一笔报单按报价成交时）的手续费。
//
// todayBefore 是**平仓前**这一侧的今仓手数（开仓传 0）—— 裸 CLOSE 的档位由它定（§13 #21）。
// ApplyTrade 与 FreezeOf 共用它：「这一笔收多少」只有一份定义。
func (s *Simulator) commission(tr match.Trade, todayBefore int) (decimal.Decimal, error) {
	inst, err := s.rules.Instrument(tr.Instrument)
	if err != nil {
		return decimal.Zero, err
	}
	rates, err := s.rules.CommissionRates(tr.Instrument, tr.Hedge)
	if err != nil {
		return decimal.Zero, err
	}
	px := s.prices[tr.Instrument]
	feePrice, err := fee.BasisPrice(s.choices.FeeBasis, tr.Price, px.pre, px.hasPre)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s：%w", tr.Instrument, err)
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
	if tr.Offset == types.Open {
		err = charge(types.Open, tr.Volume)
	} else if tr.Offset.SpecifiesPositionDate() {
		err = charge(tr.Offset, tr.Volume)
	} else if err := chargeUndated(tr, todayBefore, rates, charge); err != nil {
		return decimal.Zero, err
	}
	return total, err
}

// chargeUndated 给裸 CLOSE 与强平标志收手续费 —— §13 #21 未收敛，只做两个残余候选一致的那一段。
//
// ⚠️ 已被否：「按实际消耗的明细拆档，昨仓部分走平昨档」。DCE.m2701 通用平仓消耗了昨仓，
// 柜台收的是平今档 0.1 而不是平昨档 0.2（ctp-slices-20260915-{2,3}）。
//
//	(a) min(平仓量, 平仓前今仓量) 走平今，其余走平昨
//	(b) 一律走平今
//	(c) 按消耗拆档没错，是大商所行为上的平昨费率 ≠ 声明（评审 20260915 补；语料里没有大商所显式平昨成交）
//
// 平仓量 ≤ 平仓前今仓量时 a、b 都是「全走平今」；⚠️ c 未排除，它在这一段预言的是「行为平昨费率」，
// 与声明平今费率相等只是 m2701 上的巧合 ⇒ **这一段只在 m2701 上有观测**。
// 超出的部分只在两档声明费率相同时 a、b 同值 —— 不同就报错，不猜。
func chargeUndated(tr match.Trade, todayBefore int, rates refdata.CommissionRates, charge func(types.Offset, int) error) error {
	todayPart := tr.Volume
	if todayPart > todayBefore {
		todayPart = todayBefore
	}
	if err := charge(types.CloseToday, todayPart); err != nil {
		return err
	}
	rest := tr.Volume - todayPart
	if rest == 0 {
		return nil
	}
	if !rates.CloseTodayByMoney.Equal(rates.CloseByMoney) || !rates.CloseTodayByVolume.Equal(rates.CloseByVolume) {
		return fmt.Errorf("%s 的 %v %d 手里有 %d 手超出平仓前的今仓（%d 手），而平今档与平昨档费率不同 —— "+
			"这一段收哪一档没有观测（§13 #21：按今仓量认定则收平昨，一律平今则收平今）", tr.Instrument, tr.Offset, tr.Volume, rest, todayBefore)
	}
	return charge(types.CloseYesterday, rest) // 两档同费率：两个候选同值
}

// commit 把算好的数写进账户。⚠️ 走到这里状态已经换进去了：任何失败都让模拟器失效。
func (s *Simulator) commit(day types.TradingDay, v valuation, commission, closeProfit decimal.Decimal) error {
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
