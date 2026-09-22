package ctpfixture

import (
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

// TestFacadeF11ExtrapolationsAgainstCTP 从门面重放 F11 外推 A、C 的两所序列（ctp-quota-ext，交易日 20260923），
// 逐笔比每一笔成交的手续费增量（design.md 门面形状 §17，F13；结果见 state.md 两个「事前登记：F11 …外推」登记块）。
//
//	A 多手裸平     昨 1 → 买开 1（今 1 昨 1，额度 1）→ 一笔裸平 2 手：柜台 m2703 收 0.3、MA703 收 8（按额度拆）
//	C 开过反方向   多头昨 1 → 卖开 1 → 裸平多头 1：柜台 m2705 收 0.2、MA705 收 2（额度分方向）→ 收尾买平空头
//
// ⚠️ 种子是**按结果重建**的：交易日 20260922 开 1 手 → Settle → 20260923 昨 1。不是逐笔重放 20260922 ——
// 例如 C 的 m2705 那天实际开过三笔、平过一笔 2 手（F11 外推种子插曲），这里只取「昨 1」这个结果（评审 20260922）。
// ⚠️ 开平标志用柜台成交记录里的（改写之后的）。只比手续费：费率按手（声明值），乘数 10、保证金率 0.1 不影响它。
func TestFacadeF11ExtrapolationsAgainstCTP(t *testing.T) {
	fx := loadCTP(t)
	d := decimal.RequireFromString
	dce := refdata.CommissionRates{OpenByVolume: d("0.2"), CloseByVolume: d("0.2"), CloseTodayByVolume: d("0.1")}
	czce := refdata.CommissionRates{OpenByVolume: d("2"), CloseByVolume: d("2"), CloseTodayByVolume: d("6")}
	stage := func(n int) string {
		if n == 1 {
			return "ctp-slices-20260923.json"
		}
		return "ctp-slices-20260923-" + itoa(n) + ".json"
	}
	cases := []struct {
		name   string
		sym    string
		first  int // 第一份截面（①）的序号；之后每笔成交一份
		trades int // 当日本合约成交笔数
		rates  refdata.CommissionRates
		key    string // 判别那一笔柜台收的数（写死，防截面读错时两边一起错）
	}{
		{"A", "DCE.m2703", 15, 2, dce, "0.3"},
		{"A", "CZCE.MA703", 18, 2, czce, "8"},
		{"C", "DCE.m2705", 7, 3, dce, "0.2"},
		{"C", "CZCE.MA705", 11, 3, czce, "2"},
	}
	d0, d1 := types.TradingDay(20260922), types.TradingDay(20260923)
	for _, c := range cases {
		st := make([]ctpFixture, c.trades+1)
		for i := range st {
			f, ok := fx[stage(c.first+i)]
			if !ok {
				t.Fatalf("⚠️ 缺夹具 %s", stage(c.first+i))
			}
			if f.TradingDay != "20260923" {
				t.Fatalf("%s 的交易日 %s，期望 20260923", stage(c.first+i), f.TradingDay)
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
		mark := func(day types.TradingDay) {
			t.Helper()
			if err := sim.Mark(day, futsim.Quote{Instrument: id, Last: settle, HasLast: true, PreSettlement: settle, HasPreSettlement: true}); err != nil {
				t.Fatal(err)
			}
		}
		mark(d0)
		if err := sim.ApplyTrade(d0, match.Trade{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: settle, Volume: 1}); err != nil {
			t.Fatal(err)
		}
		if err := sim.Settle(d0, map[types.InstrumentID]decimal.Decimal{id: settle}, d1); err != nil {
			t.Fatal(err)
		}
		mark(d1)

		_, code, _ := strings.Cut(c.sym, ".")
		var trades []map[string]any
		for _, tr := range st[c.trades].Trades {
			if tr["InstrumentID"] == code {
				trades = append(trades, tr)
			}
		}
		sort.Slice(trades, func(i, j int) bool { return flt(t, trades[i], "SequenceNo") < flt(t, trades[j], "SequenceNo") })
		if len(trades) != c.trades {
			t.Fatalf("%s %s：当日成交 %d 笔，期望 %d", c.name, c.sym, len(trades), c.trades)
		}
		for k, tr := range trades {
			off, err := offsetOf(tr["OffsetFlag"])
			if err != nil {
				t.Fatalf("%s：%v", c.sym, err)
			}
			dir := types.Buy
			if tr["Direction"] == "1" {
				dir = types.Sell
			}
			want := num(t, st[k+1].Account, "Commission").Sub(num(t, st[k].Account, "Commission")).Round(6)
			if k == 1 && !want.Equal(d(c.key)) {
				t.Fatalf("⚠️ %s %s 判别那一笔：截面差 %s ≠ 登记结果 %s —— 截面读错了", c.name, c.sym, want, c.key)
			}
			before := sim.Account().Commission
			err = sim.ApplyTrade(d1, match.Trade{Instrument: id, Direction: dir, Offset: off, Hedge: types.Speculation,
				Price: num(t, tr, "Price"), Volume: int(flt(t, tr, "Volume"))})
			if err != nil {
				t.Errorf("⚠️ %s %s 第 %d 笔（%v %v %v 手）：本库报错 %v，柜台收 %s", c.name, c.sym, k+1, dir, off, flt(t, tr, "Volume"), err, want)
				break
			}
			if got := sim.Account().Commission.Sub(before); !got.Equal(want) {
				t.Errorf("⚠️ %s %s 第 %d 笔（%v %v）：本库收 %s，柜台收 %s", c.name, c.sym, k+1, dir, off, got, want)
			}
		}
	}
}

func itoa(n int) string {
	return decimal.NewFromInt(int64(n)).String()
}
