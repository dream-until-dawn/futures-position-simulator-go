package futsim

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// offlineFixture 是纯合成规格：同价交易无盈亏，每手所有开平档固定收费1元。
// 它只用于验证程序性质与测量性能，不能作为交易所规则观测。
func offlineFixture(tb testing.TB) (Config, types.InstrumentID) {
	tb.Helper()
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
	rules, err := refdata.NewBuilder(1).
		AddInstrument(refdata.Instrument{ID: id, VolumeMultiple: decimal.NewFromInt(10), PriceTick: decimal.NewFromInt(1), PositionDateType: refdata.UseHistory, IsTrading: true}).
		AddCommissionRates(id, types.Speculation, refdata.CommissionRates{OpenByVolume: decimal.NewFromInt(1), CloseByVolume: decimal.NewFromInt(1), CloseTodayByVolume: decimal.NewFromInt(1)}).
		AddMarginRates(id, types.Speculation, refdata.MarginRates{LongByMoney: decimal.RequireFromString("0.1"), ShortByMoney: decimal.RequireFromString("0.1")}).Build()
	if err != nil {
		tb.Fatal(err)
	}
	return Config{Day: simDay, PreBalance: decimal.NewFromInt(1000000000), Rules: rules, Choices: ctpChoices()}, id
}

func offlineNew(tb testing.TB, cfg Config, id types.InstrumentID) *Simulator {
	tb.Helper()
	s, err := New(cfg)
	if err != nil {
		tb.Fatal(err)
	}
	if err = s.Mark(cfg.Day, Quote{Instrument: id, Last: decimal.NewFromInt(3000), HasLast: true, PreSettlement: decimal.NewFromInt(3000), HasPreSettlement: true}); err != nil {
		tb.Fatal(err)
	}
	return s
}
