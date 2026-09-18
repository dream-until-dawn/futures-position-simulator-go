package ctpfixture

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestFacadeUndatedCloseQuotaAgainstCTP 从门面重放 §13 #23 的两所序列（ctp-quota，交易日 20260921），
// 逐笔比每一笔平仓的手续费增量（design.md 门面形状 §15，F11）。
//
//	种子：交易日 20260918 开 1 手 → Settle → 20260921 是昨 1
//	开今两手（今 2 昨 1）→ 裸平 1 手 × 3：柜台 m2701 收 0.1 / 0.1 / 0.2，MA701 收 6 / 6 / 2
//
// ⚠️ 平仓的开平标志用**柜台成交记录里的**（改写之后的），不是当初发的 —— 灌成交路径本来就是这样用的。
// ⚠️ 只比手续费：费率按手（m 0.2/0.2/0.1、MA 2/2/6，声明值，#23 的登记表同源），乘数与保证金率不影响它，按 10 / 0.1 给；
// 持仓与盈亏字段另有对拍，这里不比。
func TestFacadeUndatedCloseQuotaAgainstCTP(t *testing.T) {
	fx := loadCTP(t)
	d := decimal.RequireFromString
	cases := []struct {
		sym    string
		stages [5]string // ① 种子 ② 开今两手后 ③④⑤ 第 1/2/3 笔裸平后
		rates  refdata.CommissionRates
	}{
		{"DCE.m2701", [5]string{"ctp-slices-20260921.json", "ctp-slices-20260921-2.json", "ctp-slices-20260921-3.json", "ctp-slices-20260921-4.json", "ctp-slices-20260921-5.json"},
			refdata.CommissionRates{OpenByVolume: d("0.2"), CloseByVolume: d("0.2"), CloseTodayByVolume: d("0.1")}},
		{"CZCE.MA701", [5]string{"ctp-slices-20260921-6.json", "ctp-slices-20260921-7.json", "ctp-slices-20260921-8.json", "ctp-slices-20260921-9.json", "ctp-slices-20260921-10.json"},
			refdata.CommissionRates{OpenByVolume: d("2"), CloseByVolume: d("2"), CloseTodayByVolume: d("6")}},
	}
	d0, d1 := types.TradingDay(20260918), types.TradingDay(20260921)
	for _, c := range cases {
		var st [5]ctpFixture
		for i, n := range c.stages {
			f, ok := fx[n]
			if !ok {
				t.Fatalf("⚠️ 缺夹具 %s", n)
			}
			if f.TradingDay != "20260921" {
				t.Fatalf("%s 的交易日 %s，期望 20260921", n, f.TradingDay)
			}
			st[i] = f
		}
		id, err := types.ParseSymbol(c.sym, d0)
		if err != nil {
			t.Fatal(err)
		}
		rules := oneInstrumentRules{
			inst: refdata.Instrument{ID: id, VolumeMultiple: decimal.NewFromInt(10), PriceTick: decimal.NewFromInt(1),
				PositionDateType: refdata.NoUseHistory},
			commission: c.rates,
			margin:     refdata.MarginRates{LongByMoney: d("0.1"), ShortByMoney: d("0.1")},
		}
		ch := futsim.CTPChoices()
		ch.FeeRounding = fee.NoRounding
		sim, err := futsim.New(futsim.Config{Day: d0, PreBalance: decimal.NewFromInt(20000000), Rules: rules, Choices: ch})
		if err != nil {
			t.Fatal(err)
		}
		settle := num(t, st[0].Quotes[c.sym], "PreSettlementPrice")
		q := func(day types.TradingDay) {
			t.Helper()
			if err := sim.Mark(day, futsim.Quote{Instrument: id, Last: settle, HasLast: true, PreSettlement: settle, HasPreSettlement: true}); err != nil {
				t.Fatal(err)
			}
		}
		q(d0)
		if err := sim.ApplyTrade(d0, match.Trade{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: settle, Volume: 1}); err != nil {
			t.Fatal(err)
		}
		if err := sim.Settle(d0, map[types.InstrumentID]decimal.Decimal{id: settle}, d1); err != nil {
			t.Fatal(err)
		}
		q(d1)

		// 当日成交（按柜台序号），合约代码去掉交易所前缀比
		_, code, _ := strings.Cut(c.sym, ".")
		var trades []map[string]any
		for _, tr := range st[4].Trades {
			if tr["InstrumentID"] == code {
				trades = append(trades, tr)
			}
		}
		sort.Slice(trades, func(i, j int) bool { return flt(t, trades[i], "SequenceNo") < flt(t, trades[j], "SequenceNo") })
		if len(trades) != 5 {
			t.Fatalf("%s：当日成交 %d 笔，期望 5（开 2 + 平 3）", c.sym, len(trades))
		}
		closes := 0
		for _, tr := range trades {
			off, err := offsetOf(tr["OffsetFlag"])
			if err != nil {
				t.Fatalf("%s：%v", c.sym, err)
			}
			dir := types.Buy
			if tr["Direction"] == "1" {
				dir = types.Sell
			}
			before := sim.Account().Commission
			err = sim.ApplyTrade(d1, match.Trade{Instrument: id, Direction: dir, Offset: off, Hedge: types.Speculation,
				Price: num(t, tr, "Price"), Volume: int(flt(t, tr, "Volume"))})
			if off == types.Open {
				if err != nil {
					t.Fatalf("%s 开仓：%v", c.sym, err)
				}
				continue
			}
			closes++
			want := num(t, st[1+closes].Account, "Commission").Sub(num(t, st[closes].Account, "Commission")).Round(6)
			if err != nil {
				t.Errorf("⚠️ %s 第 %d 笔裸平：本库报错 %v，柜台收 %s", c.sym, closes, err, want)
				break
			}
			if got := sim.Account().Commission.Sub(before); !got.Equal(want) {
				t.Errorf("⚠️ %s 第 %d 笔裸平：本库收 %s，柜台收 %s", c.sym, closes, got, want)
			}
		}
		if closes != 3 && !t.Failed() {
			t.Errorf("%s：只走到 %d 笔平仓", c.sym, closes)
		}
	}
}

// offsetOf 把柜台成交记录的开平标志翻成本库的。
func offsetOf(v any) (types.Offset, error) {
	switch v {
	case "0":
		return types.Open, nil
	case "1":
		return types.Close, nil
	case "3":
		return types.CloseToday, nil
	case "4":
		return types.CloseYesterday, nil
	}
	return 0, fmt.Errorf("认不得的开平标志 %v", v)
}
