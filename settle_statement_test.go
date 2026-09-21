package futsim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestSettleFeeFormulaAgainstStatements：CTP 结算单上 j2701（按额）与 m2701（按手）的每一笔，
// 用门面自己的 commission 算出的结算口径手续费逐笔等于结算单 Fee（§13 #5，design.md 门面形状 §14）。
//
// 0918 四笔两手单里含 1965 那一笔：盘中 23.59、(i-t) 给 23.59，结算单 23.58；0921 十二笔三手单里含 1982.5 那一笔（d = 5）。
// ⚠️ 费率是柜台声明值（j 按额 6e-05 三档同、m 按手 0.2 / 0.2 / 0.1），乘数 j 100 / m 10。只比与额度无关的笔（j 全部、m 开仓）；
// m 的平仓与郑商所那几笔的档位依赖额度序列，由整天重放那条对拍管。
func TestSettleFeeFormulaAgainstStatements(t *testing.T) {
	d := decimal.RequireFromString
	day := types.TradingDay(20260918)
	j, err := types.ParseSymbol("DCE.j2701", day)
	if err != nil {
		t.Fatal(err)
	}
	m, err := types.ParseSymbol("DCE.m2701", day)
	if err != nil {
		t.Fatal(err)
	}
	b := refdata.NewBuilder(1).
		AddInstrument(refdata.Instrument{ID: j, VolumeMultiple: d("100"), PriceTick: d("0.5"), PositionDateType: refdata.NoUseHistory, IsTrading: true,
			MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000}).
		AddCommissionRates(j, types.Speculation, refdata.CommissionRates{OpenByMoney: d("0.00006"), CloseByMoney: d("0.00006"), CloseTodayByMoney: d("0.00006")}).
		AddMarginRates(j, types.Speculation, refdata.MarginRates{LongByMoney: d("0.1"), ShortByMoney: d("0.1")}).
		AddInstrument(refdata.Instrument{ID: m, VolumeMultiple: d("10"), PriceTick: d("1"), PositionDateType: refdata.NoUseHistory, IsTrading: true,
			MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000}).
		AddCommissionRates(m, types.Speculation, refdata.CommissionRates{OpenByVolume: d("0.2"), CloseByVolume: d("0.2"), CloseTodayByVolume: d("0.1")}).
		AddMarginRates(m, types.Speculation, refdata.MarginRates{LongByMoney: d("0.1"), ShortByMoney: d("0.1")})
	rules, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	c := ctpChoices()
	c.FeeRounding = fee.NoRounding
	s, err := New(Config{Day: day, PreBalance: d("20000000"), Rules: rules, Choices: c})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]types.InstrumentID{"j2701": j, "m2701": m}
	n := 0
	for _, f := range []string{"ctp-settlement-20260918.json", "ctp-settlement-20260921.json"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "refdata", f))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Trades []map[string]string `json:"trades"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, r := range doc.Trades {
			id, ok := ids[r["Instrument"]]
			if !ok {
				continue
			}
			off := types.Open
			if r["O/C"] != "Open" {
				if r["Instrument"] != "j2701" {
					continue // m 的平仓档位由当日开仓额度定（0921 有 ctp-quota 的三笔裸平），与结算口径无关，归整天重放那条管
				}
				off = types.CloseYesterday // j 三档同费率：档位不影响数，显式给，不走额度
			}
			dir := types.Buy
			if r["B/S"] == "Sell" {
				dir = types.Sell
			}
			var lots int
			if err := json.Unmarshal([]byte(r["Lots"]), &lots); err != nil {
				t.Fatal(err)
			}
			_, _, atSettle, err := s.commission(match.Trade{Instrument: id, Direction: dir, Offset: off, Hedge: types.Speculation,
				Price: d(r["Price"]), Volume: lots}, nil)
			if err != nil {
				t.Fatalf("%s %v：%v", f, r, err)
			}
			if want := d(r["Fee"]); !atSettle.Equal(want) {
				t.Errorf("⚠️ %s %s %s × %d 手：门面结算口径 %s，结算单 %s", f, r["Instrument"], r["Price"], lots, atSettle, want)
			}
			n++
		}
	}
	if n < 17 {
		t.Fatalf("⚠️ 只比了 %d 笔（下界 17：0918 j 四笔 + m 一笔、0921 j 十二笔）—— 结算单或读法变了", n)
	}
}
