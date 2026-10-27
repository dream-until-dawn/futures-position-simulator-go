package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/conformance/fixture"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/lifecycle"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/replay"
	"github.com/dream-until-dawn/futures-position-simulator-go/risk"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// exampleConfiguration只用公开API创建合成规则，不是rb实际交割月份资料。
func exampleConfiguration(day types.TradingDay, version int64) (futsim.Config, *lifecycle.DailyRules, error) {
	d := decimal.RequireFromString
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2026, Month: 10}
	first := refdata.MarginRates{LongByMoney: d("0.1"), ShortByMoney: d("0.1"), CompanyAddOn: d("0.02")}
	base, err := refdata.NewBuilder(100).AddInstrument(refdata.Instrument{ID: id, VolumeMultiple: d("10"), PriceTick: d("1"), PositionDateType: refdata.UseHistory, ExpireDate: 20261029, IsTrading: true}).AddMarginRates(id, types.Speculation, first).AddCommissionRates(id, types.Speculation, refdata.CommissionRates{OpenByVolume: d("1"), CloseByVolume: d("2"), CloseTodayByVolume: d("3")}).Build()
	if err != nil {
		return futsim.Config{}, nil, err
	}
	plan := lifecycle.Schedule{First: 20261027, Last: 20261030, Investor: lifecycle.Individual, Source: "synthetic-example", Contracts: []lifecycle.Contract{{Instrument: id, LastTradingDay: 20261029, LastHoldingDay: 20261029, MarginSteps: []lifecycle.MarginStep{{From: 20261027, Rates: first}, {From: 20261028, Rates: refdata.MarginRates{LongByMoney: d("0.2"), ShortByMoney: d("0.2"), CompanyAddOn: d("0.03")}}}}}}
	rules, err := lifecycle.NewDailyRules(base, plan, day, version)
	if err != nil {
		return futsim.Config{}, nil, err
	}
	cal, err := refdata.NewCalendar([]types.TradingDay{20261027, 20261028, 20261029, 20261030}, []refdata.SessionTable{{Exchange: types.SHFE, Product: "rb", Day: []refdata.Session{{Start: refdata.MustClockTime(9, 0, 0), End: refdata.MustClockTime(15, 0, 0)}}}}, []int32{})
	if err != nil {
		return futsim.Config{}, nil, err
	}
	choices := futsim.CTPChoices()
	choices.FeeRounding = fee.NoRounding // 显式选择合成口径，不冒充已实测取整。
	return futsim.Config{Day: day, PreBalance: d("1000"), Rules: rules, Calendar: cal, Choices: choices}, rules, nil
}

func saveExample(rules *lifecycle.DailyRules, cal *refdata.Calendar) (json.RawMessage, error) {
	var raw bytes.Buffer
	err := rules.Save(&raw, refdata.EvidencePlaceholder, "synthetic-example", time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC), cal)
	return raw.Bytes(), err
}

// demonstrateLifecycle 展示完整规则存取、原子跨日、新台阶风控信号和配置绑定恢复。
func demonstrateLifecycle(invalid bool) (replay.Report, error) {
	cfg, current, err := exampleConfiguration(20261027, 2)
	if err != nil {
		return replay.Report{}, err
	}
	nextCfg, next, err := exampleConfiguration(20261028, 3)
	if err != nil {
		return replay.Report{}, err
	}
	initial, err := saveExample(current, cfg.Calendar)
	if err != nil {
		return replay.Report{}, err
	}
	nextRaw, err := saveExample(next, nextCfg.Calendar)
	if err != nil {
		return replay.Report{}, err
	}
	if invalid {
		nextRaw = nil
	} // 负例：日规则不能沿用昨日配置完成跨日。
	d := decimal.RequireFromString
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2026, Month: 10}
	p := replay.Payload{Lifecycle: initial, Settings: replay.Settings{Day: cfg.Day, PreBalance: cfg.PreBalance, Choices: cfg.Choices}, Events: []replay.Event{
		{ID: "mark", Day: cfg.Day, Kind: "mark", Quote: &futsim.Quote{Instrument: id, Last: d("100"), HasLast: true, PreSettlement: d("100"), HasPreSettlement: true}},
		{ID: "open", Day: cfg.Day, Kind: "trade", Trade: &match.Trade{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: d("100"), Volume: 1}},
		{ID: "settle", Day: cfg.Day, Kind: "settle", Settlement: &replay.Settlement{NextDay: nextCfg.Day, Prices: []replay.Price{{Instrument: id, Value: d("110")}}, NextRules: nextRaw}},
		{ID: "risk", Day: nextCfg.Day, Kind: "risk", RiskPolicy: &risk.Policy{MaxRiskRatio: d("0.2"), MinEquity: d("1"), MinAvailable: d("0")}},
	}}
	packet, err := replay.Encode(p)
	if err != nil {
		return replay.Report{}, err
	}
	decoded, err := replay.Decode(bytes.NewReader(packet))
	if err != nil {
		return replay.Report{}, err
	}
	report, err := replay.Run(decoded, replay.Options{AllowPlaceholder: true})
	if err != nil {
		return report, err
	}
	// State不包含规则；必须带对应日/版本的完整配置，不能只拿一份旧Provider恢复。
	loaded, cal, _, err := lifecycle.Load(bytes.NewReader(nextRaw), refdata.LoadOptions{AllowPlaceholder: true})
	if err != nil {
		return report, err
	}
	nextCfg.Rules, nextCfg.Calendar = loaded, cal
	restored, err := futsim.Restore(nextCfg, *report.Final)
	if err != nil {
		return report, err
	}
	state, err := restored.State()
	if err != nil {
		return report, err
	}
	if state.RulesVersion != 3 || !restored.Account().CurrMargin.Equal(d("253")) {
		return report, fmt.Errorf("外部模块规则恢复不等价")
	}
	return report, nil
}

// demonstrateFrozen 用独立前置种子与明确顺序重建两笔平今，第三笔超平不返回半本簿。
func demonstrateFrozen(invalid bool) (*order.Book, bool, error) {
	cfg, _, err := exampleConfiguration(20261027, 2)
	if err != nil {
		return nil, false, err
	}
	sim, err := futsim.New(cfg)
	if err != nil {
		return nil, false, err
	}
	d := decimal.RequireFromString
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2026, Month: 10}
	if err = sim.Mark(cfg.Day, futsim.Quote{Instrument: id, Last: d("100"), HasLast: true, PreSettlement: d("100"), HasPreSettlement: true}); err != nil {
		return nil, false, err
	}
	if err = sim.ApplyTrade(cfg.Day, match.Trade{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: d("100"), Volume: 2}); err != nil {
		return nil, false, err
	}
	seed, err := sim.State()
	if err != nil {
		return nil, false, err
	}
	seed.LastOrderSequence = 17
	text := func(s string) fixture.Value { return fixture.Value{Text: s, IsText: true} }
	record := func() map[string]fixture.Value {
		return map[string]fixture.Value{"status": text("ALIVE"), "exchange_id": text("SHFE"), "instrument_id": text("rb2610"), "direction": text("SELL"), "offset": text("CLOSETODAY"), "volume_left": {Number: d("1")}, "limit_price": {Number: d("100")}}
	}
	f := &fixture.Fixture{TradingDay: cfg.Day, HasOrders: true, Orders: map[string]map[string]fixture.Value{"first": record(), "second": record()}, Note: "synthetic independent pre-order seed"}
	ids := []string{"first", "second"}
	if invalid {
		f.Orders["third"] = record()
		ids = append(ids, "third")
	}
	return fixture.FrozenBookWithState(f, cfg, seed, ids)
}
