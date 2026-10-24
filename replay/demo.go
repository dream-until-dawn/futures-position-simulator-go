package replay

import (
	"bytes"
	"fmt"
	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
	"time"
)

// Demo 构造完全合成的两合约多空、挂单冻结/手动成交、跨日结算与恢复输入。
// 故意使用placeholder等级，调用方必须显式允许；不能当作任何市场规则观测。
func Demo() (Payload, error) {
	return demoDays(2)
}

func demoDays(count int) (Payload, error) {
	d := decimal.NewFromInt
	a := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
	b := types.InstrumentID{Exchange: types.SHFE, Product: "cu", Year: 2027, Month: 1}
	builder := refdata.NewBuilder(1)
	for _, id := range []types.InstrumentID{a, b} {
		builder.AddInstrument(refdata.Instrument{ID: id, VolumeMultiple: d(10), PriceTick: d(1), PositionDateType: refdata.UseHistory, IsTrading: true}).AddCommissionRates(id, types.Speculation, refdata.CommissionRates{OpenByVolume: d(1), CloseByVolume: d(2), CloseTodayByVolume: d(3)}).AddMarginRates(id, types.Speculation, refdata.MarginRates{LongByMoney: decimal.RequireFromString("0.1"), ShortByMoney: decimal.RequireFromString("0.1")})
	}
	rules, err := builder.Build()
	if err != nil {
		return Payload{}, err
	}
	var days []types.TradingDay
	start := time.Date(2026, 10, 26, 0, 0, 0, 0, refdata.CNZone())
	for i := 0; i < count+1; i++ {
		stamp := start.AddDate(0, 0, i)
		day, err := types.ParseTradingDay(stamp.Format("20060102"))
		if err != nil {
			return Payload{}, err
		}
		days = append(days, day)
	}
	// 这是显式合成日历，包含自然周末也不代表真实交易日。
	cal, err := refdata.NewCalendar(days, []refdata.SessionTable{{Exchange: types.SHFE, Product: "rb", Day: []refdata.Session{{Start: refdata.MustClockTime(9, 0, 0), End: refdata.MustClockTime(15, 0, 0)}}}, {Exchange: types.SHFE, Product: "cu", Day: []refdata.Session{{Start: refdata.MustClockTime(9, 0, 0), End: refdata.MustClockTime(15, 0, 0)}}}}, nil)
	if err != nil {
		return Payload{}, err
	}
	var raw bytes.Buffer
	if err = rules.Save(&raw, refdata.EvidencePlaceholder, "完全合成的离线回放演示，非市场观测", start, cal); err != nil {
		return Payload{}, err
	}
	choices := futsim.CTPChoices()
	choices.FeeRounding = fee.NoRounding
	p := Payload{Rules: raw.Bytes(), Settings: Settings{Day: days[0], PreBalance: d(1000000), Choices: choices, TickRounding: []TickRule{{Exchange: types.SHFE, Rounding: refdata.TickFloor}}, PositionLimits: []Limit{{Instrument: a, Volume: 100}, {Instrument: b, Volume: 100}}}, Events: []Event{}}
	add := func(e Event) { e.ID = fmt.Sprintf("event-%06d", len(p.Events)); p.Events = append(p.Events, e) }
	for i := 0; i < count; i++ {
		day := days[i]
		for j, id := range []types.InstrumentID{a, b} {
			pre := int64(3000)
			if i > 0 {
				pre = 3001
			}
			add(Event{Day: day, Kind: "mark", Quote: &futsim.Quote{Instrument: id, Last: d(3000), HasLast: true, PreSettlement: d(pre), HasPreSettlement: true}})
			side := types.Buy
			if j == 1 {
				side = types.Sell
			}
			ref := fmt.Sprintf("order-%d-%d", i, j)
			add(Event{Day: day, Kind: "accept", Order: &Accepted{Ref: ref, Request: order.Request{Instrument: id, Direction: side, Offset: types.Open, Hedge: types.Speculation, Price: d(3000), Volume: 1}}})
			add(Event{Day: day, Kind: "fill", Ref: ref})
			// 同日反向平仓，只验证核算；成交时机由此显式事件给出。
			side = types.Sell
			if j == 1 {
				side = types.Buy
			}
			closeRef := ref + "-close"
			add(Event{Day: day, Kind: "accept", Order: &Accepted{Ref: closeRef, Request: order.Request{Instrument: id, Direction: side, Offset: types.CloseToday, Hedge: types.Speculation, Price: d(3001), Volume: 1}}})
			add(Event{Day: day, Kind: "fill", Ref: closeRef})
		}
		if i > 0 {
			add(Event{Day: day, Kind: "trade", Trade: &match.Trade{Instrument: a, Direction: types.Sell, Offset: types.CloseYesterday, Hedge: types.Speculation, Price: d(3001), Volume: 1}})
		}
		add(Event{Day: day, Kind: "trade", Trade: &match.Trade{Instrument: a, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: d(3000), Volume: 1}})
		add(Event{Day: day, Kind: "settle", Settlement: &Settlement{NextDay: days[i+1], Prices: []Price{{Instrument: a, Value: d(3001)}, {Instrument: b, Value: d(3001)}}}})
	}
	return p, nil
}
