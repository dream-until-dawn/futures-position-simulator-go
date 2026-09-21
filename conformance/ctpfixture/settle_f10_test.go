package ctpfixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestFacadeSettlesDay20260918AgainstCTP 从门面重放交易日 20260918 的全部成交（结算单成交记录），结算到 20260921，
// 上日结存与柜台 19995806.40 一分不差（design.md 门面形状 §14，F10：结算时按笔重算手续费）。
//
//	起点   20260917 开 MA701 两手 @3025 并按 3025 结算 ⇒ 20260918 上日结存 19997514.88（与柜台相同）、MA 昨 2
//	当日   郑商所 X1 / 开今 / X2 / X0 / 种子开仓，j2701 两手单四张，m2701 种子开仓（结算单里的价、手数、开平）
//	结算   MA701 2977、m2701 3429（20260921 行情的 PreSettlementPrice）；j 已平
//
// 柜台：平仓盈亏 −1260、结算单手续费 108.48、持仓盈亏 −340 ⇒ 19995806.40。
// ⚠️ 门面盘中**不收** j 那个每手 0.005 的常数项（§13 #19），盘中手续费与柜台不同 —— 结算费与它无关（四舍五入(按额) + 按手），所以次日结存照样对得上。
// ⚠️ 这条对拍在 F10 之前会红：结算按盘中费结存，差 0.008（门面盘中 j 按额部分合计 94.272，结算单 94.28）。
func TestFacadeSettlesDay20260918AgainstCTP(t *testing.T) {
	d := decimal.RequireFromString
	d0, d1, d2 := types.TradingDay(20260917), types.TradingDay(20260918), types.TradingDay(20260921)
	id := func(sym string) types.InstrumentID {
		x, err := types.ParseSymbol(sym, d1)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	ma, j, m := id("CZCE.MA701"), id("DCE.j2701"), id("DCE.m2701")
	mr := refdata.MarginRates{LongByMoney: d("0.1"), ShortByMoney: d("0.1")} // 不影响结存
	inst := func(x types.InstrumentID, mult, tick string) refdata.Instrument {
		return refdata.Instrument{ID: x, VolumeMultiple: d(mult), PriceTick: d(tick), PositionDateType: refdata.NoUseHistory, IsTrading: true,
			MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000}
	}
	rules, err := refdata.NewBuilder(20260918).
		AddInstrument(inst(ma, "10", "1")).
		AddCommissionRates(ma, types.Speculation, refdata.CommissionRates{OpenByVolume: d("2"), CloseByVolume: d("2"), CloseTodayByVolume: d("6")}).
		AddMarginRates(ma, types.Speculation, mr).
		AddInstrument(inst(j, "100", "0.5")).
		AddCommissionRates(j, types.Speculation, refdata.CommissionRates{OpenByMoney: d("0.00006"), CloseByMoney: d("0.00006"), CloseTodayByMoney: d("0.00006")}).
		AddMarginRates(j, types.Speculation, mr).
		AddInstrument(inst(m, "10", "1")).
		AddCommissionRates(m, types.Speculation, refdata.CommissionRates{OpenByVolume: d("0.2"), CloseByVolume: d("0.2"), CloseTodayByVolume: d("0.1")}).
		AddMarginRates(m, types.Speculation, mr).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	ch := futsim.CTPChoices()
	ch.FeeRounding = fee.NoRounding
	sim, err := futsim.New(futsim.Config{Day: d0, PreBalance: d("19997518.88"), Rules: rules, Choices: ch})
	if err != nil {
		t.Fatal(err)
	}
	mark := func(day types.TradingDay, x types.InstrumentID, last, pre string) {
		t.Helper()
		if err := sim.Mark(day, futsim.Quote{Instrument: x, Last: d(last), HasLast: true, PreSettlement: d(pre), HasPreSettlement: true}); err != nil {
			t.Fatal(err)
		}
	}
	// 起点：MA 昨 2（20260917 结算价 3025），开仓费 2 × 2 = 4 ⇒ 上日结存 19997514.88
	mark(d0, ma, "3025", "3025")
	if err := sim.ApplyTrade(d0, match.Trade{Instrument: ma, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: d("3025"), Volume: 2}); err != nil {
		t.Fatal(err)
	}
	if err := sim.Settle(d0, map[types.InstrumentID]decimal.Decimal{ma: d("3025")}, d1); err != nil {
		t.Fatal(err)
	}
	if got := sim.Account().PreBalance; !got.Equal(d("19997514.88")) {
		t.Fatalf("起点上日结存 %s，期望 19997514.88", got)
	}

	mark(d1, ma, "3025", "3025")
	mark(d1, j, "2065", "2065") // ctp-slices-20260918-7 行情的 PreSettlementPrice；CTP 预设按成交价计费，它只影响保证金
	mark(d1, m, "3449", "3449") // m 的昨结算价只影响保证金与盘中盈亏，结存按结算价兑现

	raw, err := os.ReadFile(filepath.FromSlash("../../testdata/refdata/ctp-settlement-20260918.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Summary map[string]string   `json:"summary"`
		Trades  []map[string]string `json:"trades"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ids := map[string]types.InstrumentID{"MA701": ma, "j2701": j, "m2701": m}
	for i, r := range doc.Trades {
		x, ok := ids[r["Instrument"]]
		if !ok {
			t.Fatalf("结算单里有没接的合约 %s", r["Instrument"])
		}
		off, dir := types.Open, types.Buy
		if r["O/C"] != "Open" {
			off = types.Close // 结算单记作「平」：大商所会把显式平今 / 平昨改写成平仓，郑商所这几笔本来就是裸平
		}
		if r["B/S"] == "Sell" {
			dir = types.Sell
		}
		var lots int
		if err := json.Unmarshal([]byte(r["Lots"]), &lots); err != nil {
			t.Fatal(err)
		}
		if err := sim.ApplyTrade(d1, match.Trade{Instrument: x, Direction: dir, Offset: off, Hedge: types.Speculation, Price: d(r["Price"]), Volume: lots}); err != nil {
			t.Fatalf("第 %d 笔 %v：%v", i+1, r, err)
		}
	}
	if got := sim.Account().CloseProfit; !got.Equal(d(doc.Summary["Realized P/L"])) {
		t.Errorf("⚠️ 平仓盈亏：门面 %s，结算单 %s", got, doc.Summary["Realized P/L"])
	}
	if err := sim.Settle(d1, map[types.InstrumentID]decimal.Decimal{ma: d("2977"), m: d("3429")}, d2); err != nil {
		t.Fatal(err)
	}
	if got, want := sim.Account().PreBalance, d(doc.Summary["Balance C/F"]); !got.Equal(want) || !want.Equal(d("19995806.40")) {
		t.Errorf("⚠️ 20260921 上日结存：门面 %s，柜台 %s（结算单 Balance C/F；20260921 截面 PreBalance 19995806.40）", got, want)
	}
}
