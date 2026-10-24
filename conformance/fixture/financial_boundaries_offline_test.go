package fixture

import (
	"errors"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"testing"
)

func TestOfflineFeeTiersAreDistinct(t *testing.T) {
	rates := refdata.CommissionRates{OpenByMoney: dd("0.01"), OpenByVolume: dd("1"), CloseByMoney: dd("0.02"), CloseByVolume: dd("2"), CloseTodayByMoney: dd("0.03"), CloseTodayByVolume: dd("3")}
	for _, tc := range []struct {
		off  types.Offset
		want string
	}{{types.Open, "22"}, {types.CloseYesterday, "44"}, {types.CloseToday, "66"}} {
		got, err := fee.ComputeRaw(rates, tc.off, dd("100"), dd("10"), 2)
		if err != nil || !got.Equal(dd(tc.want)) {
			t.Fatalf("合成手续费档位混用：%v got=%s want=%s err=%v", tc.off, got, tc.want, err)
		}
	}
	rates.CloseTodayByMoney, rates.CloseTodayByVolume = dd("0"), dd("0")
	got, err := fee.ComputeRaw(rates, types.CloseToday, dd("100"), dd("10"), 2)
	if err != nil || !got.IsZero() {
		t.Fatalf("免平今错误：%s %v", got, err)
	}
	if _, err = fee.ComputeRaw(rates, types.Offset(255), dd("100"), dd("10"), 2); err == nil {
		t.Fatal("未知开平标志未拒绝")
	}
}

// offlineReplayFixture 的规格和价差全部为手算输入，不能进入真实观测目录。
func offlineReplayFixture(t *testing.T) (*Fixture, string) {
	t.Helper()
	sym := "SHFE.rb2701"
	day := mustDay(t, "20260908")
	id, err := types.ParseSymbol(sym, day)
	if err != nil {
		t.Fatal(err)
	}
	return &Fixture{TradingDay: day, Account: map[string]Value{"pre_balance": {Number: dd("1000000")}}, Positions: map[string]map[string]Value{sym: {"last_price": {Number: dd("100")}}}, Quotes: map[string]map[string]Value{sym: {"pre_settlement": {Number: dd("90")}}}, Trades: []Trade{{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: dd("100"), Volume: 2, At: 1}}}, sym
}

func TestOfflineExchangeMarginExcludesCompanyAddon(t *testing.T) {
	f, sym := offlineReplayFixture(t)
	spec := mustSpec(t, f, sym)
	spec.Multiplier = dd("10")
	spec.Margin = refdata.MarginRates{LongByMoney: dd("0.1"), ShortByMoney: dd("0.1"), CompanyAddOn: dd("0.03")}
	r, err := ReplayOnFacade(f, sym, spec, refdata.PositionDateNotNeeded, dd("90"), false)
	if err != nil || !r.HasMargin || !r.MarginLong.Equal(dd("180")) || !r.MarginShort.IsZero() {
		t.Fatalf("公司加收污染交易所占用：%+v %v", r, err)
	}
	spec.Margin.CompanyAddOn = dd("0")
	r, err = ReplayOnFacade(f, sym, spec, refdata.PositionDateNotNeeded, dd("90"), false)
	if err != nil || !r.MarginLong.Equal(dd("180")) {
		t.Fatalf("零加收对照错误：%v", err)
	}
}

func TestOfflineBorrowedPreNeverExposesMargin(t *testing.T) {
	f, sym := offlineReplayFixture(t)
	sibling, _ := offlineReplayFixture(t)
	delete(f.Quotes, sym)
	r, borrowed, err := positionOf(t, []*Fixture{f, sibling}, f, sym)
	if err != nil || !borrowed || r.HasMargin {
		t.Fatalf("借值占用越过证据边界：borrowed=%v has=%v err=%v", borrowed, r.HasMargin, err)
	}
	f.Quotes = sibling.Quotes
	r, borrowed, err = positionOf(t, []*Fixture{f}, f, sym)
	if err != nil || borrowed || !r.HasMargin {
		t.Fatalf("自带昨结算价对照错误：%v", err)
	}
	f.Positions[sym]["volume_long_his"] = Value{Number: dd("1")}
	_, _, err = positionOf(t, nil, f, sym)
	if !errors.Is(err, errNoCarrySource) {
		t.Fatalf("缺结转源未显式拒绝：%v", err)
	}
	expectEvidence(t, "真实缺源错误不能静默", "取不到门面前提", func(r evidenceReporter) { checkReplayPrerequisites(r, []string{err.Error()}) })
}
