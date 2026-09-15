package futsim

import (
	"errors"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/ctperr"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// MeasuredTickRounding 返回**实测过**的涨跌停取整方向：上期所向下、大商所四舍五入（probes.md §12，七个合约反解）。
//
// ⚠️ 能源中心、郑商所、广期所、中金所不在里面 —— 没测过就不填。报单落在这些交易所时，涨跌停那一项「没查成」，不成交。
func MeasuredTickRounding() map[types.Exchange]refdata.TickRounding {
	return map[types.Exchange]refdata.TickRounding{
		types.SHFE: refdata.TickFloor,
		types.DCE:  refdata.TickHalfUp,
	}
}

// FreezeOf 算一笔报单冻结什么（保证金、手续费、可平量）。F3 里只用它算资金校验要占用多少；挂单记账是 F4。
//
// 保证金（只有开仓）按 Choices.FreezeMargin 取价：挂单价走 margin.Compute 的今仓腿（开仓价 = 挂单价），
// 昨结算价走 PreSettleAll —— 不另写「名义额 × 费率」。手续费与成交走同一个 commission（挂单价顶成交价的位置）。
func (s *Simulator) FreezeOf(day types.TradingDay, req order.Request) (order.Frozen, error) {
	if err := s.usable(day); err != nil {
		return order.Frozen{}, err
	}
	inst, err := s.rules.Instrument(req.Instrument)
	if err != nil {
		return order.Frozen{}, err
	}
	in := order.FreezeInput{}
	req.Offset = s.datedOffset(inst.PositionDateType, req.Offset) // 冻昨仓，与成交时平昨一致
	tr := match.Trade{Instrument: req.Instrument, Direction: req.Direction, Offset: req.Offset,
		Hedge: req.Hedge, Price: req.Price, Volume: req.Volume}

	if req.Offset == types.Open {
		rates, err := s.rules.MarginRates(req.Instrument, req.Hedge)
		if err != nil {
			return order.Frozen{}, err
		}
		leg := margin.Leg{Instrument: req.Instrument, Direction: req.Direction, Volume: req.Volume,
			Multiplier: inst.VolumeMultiple, Rates: rates}
		var basis margin.PriceBasis
		switch s.choices.FreezeMargin {
		case order.FreezeAtOrderPrice:
			leg.OpenPrice, basis = req.Price, margin.OpenTodayPreSettleHistory
		case order.FreezeAtPreSettlement:
			px := s.prices[req.Instrument]
			leg.PreSettlement, leg.HasPreSettlement, basis = px.pre, px.hasPre, margin.PreSettleAll
		default:
			return order.Frozen{}, fmt.Errorf("冻结保证金基准 %v 认不得", s.choices.FreezeMargin)
		}
		res, err := margin.Compute([]margin.Leg{leg}, basis, margin.NoNetting)
		if err != nil {
			return order.Frozen{}, fmt.Errorf("%s 冻结保证金：%w", req.Instrument, err)
		}
		in.Margin = res.Company
		if in.Commission, err = s.commission(tr, 0); err != nil {
			return order.Frozen{}, err
		}
	} else {
		today, history := 0, 0
		dt := inst.PositionDateType
		if p, ok := s.positions[posKey{req.Instrument, req.Hedge}]; ok {
			today, history = p.VolumeToday(opposite(req.Direction)), p.VolumeHistory(opposite(req.Direction))
			dt = p.DateType()
		}
		// ⚠️ 冻结手续费的档位按**持有的**今仓算（与此刻成交时 ApplyTrade 看的「平仓前今仓」一致），不扣挂单冻住的 ——
		// 挂单阶段用持有还是可用，§13 #21 的候选都没说，是推得
		if in.Commission, err = s.commission(tr, today); err != nil {
			return order.Frozen{}, err
		}
		if !req.Offset.SpecifiesPositionDate() {
			// 裸 CLOSE：按实测的消耗顺序拆今 / 昨。⚠️ 持仓侧冻今还是冻昨在 CTP 上没观测，这里是**推得**（与成交时消耗的那一边一致）
			if ord, ok := position.MeasuredCloseOrder(dt); !ok || ord != position.YesterdayFirst {
				return order.Frozen{}, fmt.Errorf("%s 上的裸 CLOSE 没有实测的消耗顺序（PositionDateType %v）—— 显式给平今或平昨", req.Instrument, dt)
			}
			// 合计超了可平量时 undatedSplit 报错：校验会拒在可平量（validate 先让拒因说话），这里不猜一份冻结
			if in.UndatedToday, in.UndatedHistory, err = undatedSplit(req.Instrument, req.Volume, today, history,
				s.book.TotalOf(req.Instrument, opposite(req.Direction))); err != nil {
				return order.Frozen{}, err
			}
		}
	}
	return order.FreezeOf(req, in)
}

// validate 组装八项校验的事实、算这笔单冻结什么，并做校验。Submit 与 Place 共用。
//
// 返回：被拒 ⇒ *match.RejectedError；没查成 ⇒ *match.UncheckedError；时刻与交易日矛盾、算不出资金（且没有拒因）⇒ 普通错误。
// 通过时返回按口径改写过开平标志的请求（见下）、冻结额与（已填好 Need 的）事实。
//
// ⚠️ 八项也跟第八项口径（评审 20260915 打回 F6a 后改）：UseHistory 上的裸 CLOSE 在口径为平昨时**按平昨校验**。
// 上一版只在记账路径改写，于是快期口径下同一笔单 ApplyTrade 收、Submit 拒，而拒因原话说「本库拒绝按平昨处理：只在快期实测过」——
// 调用方选的恰恰是快期口径，报错对判据的描述与实际行为不一致。
// 改写得来的拒单**不给 CTP 拒因码**（Kind 清成 ReasonUnknown）：码的语料是 CTP 上显式平昨的拒单，快期口径下的裸 CLOSE 不在里面，不外推。
func (s *Simulator) validate(day types.TradingDay, at time.Time, req order.Request) (order.Request, order.Frozen, order.Facts, error) {
	var f order.Facts

	inst, err := s.rules.Instrument(req.Instrument)
	rewritten := false
	if err == nil {
		f.Instrument, f.HasInstrument = inst, true
		if off := s.datedOffset(inst.PositionDateType, req.Offset); off != req.Offset {
			req.Offset, rewritten = off, true
		}
	}
	reject := func(r order.Rejection) error {
		if rewritten {
			r.Kind = ctperr.ReasonUnknown
		}
		return &match.RejectedError{Rejection: r, Exchange: req.Instrument.Exchange}
	}
	px := s.prices[req.Instrument]
	f.PreSettlement, f.HasPreSettlement = px.pre, px.hasPre
	f.Rounding = s.tickRounding[req.Instrument.Exchange]

	// 持仓：没仓给一个**空**持仓 —— nil 在 order 里是「不知道」
	if p, ok := s.positions[posKey{req.Instrument, req.Hedge}]; ok {
		f.Position = p.Clone()
	} else if f.HasInstrument {
		if empty, err := position.New(req.Instrument, req.Hedge, day, inst.PositionDateType); err == nil {
			f.Position = empty
		}
	}
	// 挂着的平仓单占着可平量（F4）
	if req.Offset.IsClose() {
		f.FrozenClose = s.book.TotalOf(req.Instrument, opposite(req.Direction))
	}

	if limit, ok := s.positionLimits[req.Instrument]; ok {
		f.PositionLimit, f.HasPositionLimit = limit, true
	}

	if s.calendar != nil {
		d, err := s.calendar.TradingDayAt(at, req.Instrument.Exchange, req.Instrument.Product)
		switch {
		case err == nil && d != day:
			return req, order.Frozen{}, f, fmt.Errorf("报单时刻 %s 属于交易日 %d，而模拟器在交易日 %d —— 时刻与交易日矛盾",
				at.In(refdata.CNZone()).Format("2006-01-02 15:04:05"), d, day)
		case err == nil:
			f.InSession, f.HasSession = true, true
		case errors.Is(err, refdata.ErrOutsideSession):
			f.InSession, f.HasSession = false, true
		}
		// 其余报错（没有时段表、日历矛盾）⇒ HasSession 为假 ⇒ 没查成
	}

	f.Available, f.HasAvailable = s.acc.Available(), true
	var fr order.Frozen
	if f.HasInstrument {
		fr, err = s.FreezeOf(day, req)
		if err != nil {
			// ⚠️ 规格在、却算不出要占用多少（缺昨结算价、§13 #21 分歧段……）：直接报这个原因，
			// 不塞进「没查成」—— 那里只会说「保证金与手续费」，把真正缺的东西说丢了。
			// ⚠️ 但先看有没有更高优先级的拒因：无仓裸平这类单该拒在可平量，不该被「算不出资金」盖住
			if res := order.Validate(req, f); res.Rejected != nil {
				return req, order.Frozen{}, f, reject(*res.Rejected)
			}
			return req, order.Frozen{}, f, fmt.Errorf("算这笔单要占用的资金：%w", err)
		}
		f.Need, f.HasNeed = fr.Margin.Add(fr.Commission), true
	}

	res := order.Validate(req, f)
	if res.Rejected != nil {
		return req, order.Frozen{}, f, reject(*res.Rejected)
	}
	if !res.FullyChecked() {
		return req, order.Frozen{}, f, &match.UncheckedError{Unchecked: res.Unchecked}
	}
	return req, fr, f, nil
}

// Submit 报一笔单：组装八项校验的事实 ⇒ match.Fill（通过即按报价全量成交，裁决）⇒ ApplyTrade。
//
// at 是报单时刻，查交易时段用。被拒返回 *match.RejectedError（带 Code()），没查成返回 *match.UncheckedError ——
// 调用方用 errors.As 分：被拒要改单，没查成要补事实（取行情、给限仓、给日历）。两种都不动状态。
func (s *Simulator) Submit(day types.TradingDay, at time.Time, req order.Request) (match.Trade, error) {
	if err := s.usable(day); err != nil {
		return match.Trade{}, err
	}
	dated, _, f, err := s.validate(day, at, req)
	if err != nil {
		return match.Trade{}, err
	}
	trade, err := match.Fill(dated, f)
	if err != nil {
		return match.Trade{}, err
	}
	trade.Offset = req.Offset // 成交记录报的是委托上的开平标志（快期成交里裸 CLOSE 仍记作 CLOSE）；记账时 ApplyTrade 照口径再改写
	if err := s.ApplyTrade(day, trade); err != nil {
		return match.Trade{}, err
	}
	return trade, nil
}
