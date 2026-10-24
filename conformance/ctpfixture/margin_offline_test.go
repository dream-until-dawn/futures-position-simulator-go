package ctpfixture

import (
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
	"testing"
)

func TestOfflineMarginIncludesPerLot(t *testing.T) {
	d := decimal.RequireFromString
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
	leg := margin.Leg{Instrument: id, Direction: types.Buy, Volume: 3, Multiplier: d("10"), OpenPrice: d("100"), Rates: refdata.MarginRates{LongByMoney: d("0.1"), LongByVolume: d("7"), CompanyAddOn: d("0.02")}}
	r, err := margin.Compute([]margin.Leg{leg}, margin.OpenTodayPreSettleHistory, margin.NoNetting)
	if err != nil || !r.Exchange.Equal(d("321")) || !r.Company.Equal(d("381")) {
		t.Fatalf("按手保证金加项丢失：%+v %v", r, err)
	}
	leg.Rates.LongByVolume = d("0")
	r, err = margin.Compute([]margin.Leg{leg}, margin.OpenTodayPreSettleHistory, margin.NoNetting)
	if err != nil || !r.Exchange.Equal(d("300")) {
		t.Fatalf("零按手对照错误：%v", err)
	}
	leg.Rates.LongByVolume = d("-1")
	if _, err = margin.Compute([]margin.Leg{leg}, margin.OpenTodayPreSettleHistory, margin.NoNetting); err == nil {
		t.Fatal("负按手费率未拒绝")
	}
}
