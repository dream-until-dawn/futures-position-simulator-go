package order

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/ctperr"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// corpusObs 是拒单语料的一条（只取用得到的键）。
type corpusObs struct {
	Exchange     string   `json:"exchange"`
	Instrument   string   `json:"instrument"`
	Case         string   `json:"case"`
	Violates     []string `json:"violates"`
	Offset       string   `json:"offset"`
	ErrorID      *int     `json:"error_id"`
	ExchangeCode *int     `json:"exchange_code"`
	Outcome      string   `json:"outcome"`
}

// factsOn 是某交易所上一个合成合约的**八项全都查得了**的输入：tick 1、昨结 3000、涨跌幅 5%，多头空仓。
func factsOn(t *testing.T, ex, inst string) Facts {
	t.Helper()
	id, err := types.ParseSymbol(ex+"."+inst, types.NewTradingDay(2026, 9, 15))
	if err != nil {
		t.Fatal(err)
	}
	spec := refdata.Instrument{
		ID: id, VolumeMultiple: d("10"), PriceTick: d("1"),
		PositionDateType: refdata.UseHistory, IsTrading: true,
		MinLimitOrderVolume: 1, MaxLimitOrderVolume: 500,
		PriceLimitRatio: d("0.05"), HasPriceLimitRatio: true,
	}
	p, err := position.New(id, types.Speculation, types.NewTradingDay(2026, 9, 15), refdata.UseHistory)
	if err != nil {
		t.Fatal(err)
	}
	return Facts{
		Instrument: spec, HasInstrument: true,
		PreSettlement: d("3000"), HasPreSettlement: true, Rounding: refdata.TickFloor,
		Position:  p,
		Available: d("1000000"), HasAvailable: true, Need: d("1"), HasNeed: true,
		PositionLimit: 100, HasPositionLimit: true,
		InSession: true, HasSession: true,
	}
}

// TestRejectionCodesMatchCorpus 是 v0.4.0 验收「被拒报单的错误码一致」在**已拍语料**上的那一行。
//
// 对语料里每一条单一违反的拒单，在本库构造**同一种违反**，断言 Validate 拒在同一项、
// 且 Rejection.Kind 查出来的码与柜台给的码一致。
//
// ⚠️ 它独立验证的是「本库给这种违反挑的拒因」—— 码值那一半与 ctperr 的表**同源**（都来自这份语料），不算独立。
// ⚠️ 涨停 / 跌停只能靠用例标签分（语料的 violates 只写「涨跌停」），与 ctperr 的测试同一处依赖。
func TestRejectionCodesMatchCorpus(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "testdata", "refdata", "ctp-reject-codes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var all []corpusObs
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, o := range all {
		if o.Outcome != "rejected" || len(o.Violates) != 1 {
			continue
		}
		f := factsOn(t, o.Exchange, o.Instrument)
		req := Request{Instrument: f.Instrument.ID, Direction: types.Buy, Offset: types.Open,
			Hedge: types.Speculation, Price: d("3000"), Volume: 1}
		var wantCheck Check
		switch v := o.Violates[0]; {
		case v == "最小变动价位":
			req.Price, wantCheck = d("3000.5"), CheckPriceTick
		case v == "涨跌停" && strings.Contains(o.Case, "涨停"):
			req.Price, wantCheck = d("3200"), CheckPriceLimit
		case v == "涨跌停" && strings.Contains(o.Case, "跌停"):
			req.Price, wantCheck = d("2800"), CheckPriceLimit
		case v == "可平量" && o.Offset == "4":
			req.Direction, req.Offset, wantCheck = types.Sell, types.CloseYesterday, CheckClosable
		default:
			t.Logf("ⓘ 构造不了，跳过：%s %s %v", o.Exchange, o.Case, o.Violates)
			continue
		}
		res := Validate(req, f)
		if !res.FullyChecked() || res.Rejected == nil {
			t.Errorf("⚠️ %s %s：本库没拒（%v）—— 柜台拒了", o.Exchange, o.Case, res)
			continue
		}
		if res.Rejected.Check != wantCheck {
			t.Errorf("⚠️ %s %s：本库拒在 %s，要 %s", o.Exchange, o.Case, res.Rejected.Check, wantCheck)
			continue
		}
		var counter ctperr.Code
		switch {
		case o.ErrorID != nil:
			counter = ctperr.Code{Space: ctperr.SpaceCTP, Value: *o.ErrorID}
		case o.ExchangeCode != nil:
			counter = ctperr.Code{Space: ctperr.SpaceStatusPrefix, Value: *o.ExchangeCode}
		}
		got, ok := ctperr.Lookup(types.Exchange(o.Exchange), res.Rejected.Kind)
		if !ok || got != counter {
			t.Errorf("⚠️ %s %s：本库的拒因 %s 查出 %v（ok=%v），柜台给的是 %s", o.Exchange, o.Case, res.Rejected.Kind, got, ok, counter)
		}
		checked++
	}
	if checked < 12 {
		t.Fatalf("⚠️ 只核对了 %d 条（下界 12：三个交易所各 4 条）—— 语料读法或构造坏了", checked)
	}
	t.Logf("ⓘ 语料里 %d 条拒单，本库拒在同一项、码一致", checked)
}

// TestKindStaysUnknownOutsideCorpus 钉住语料没测过的拒绝**不给拒因**，而测过的给。
func TestKindStaysUnknownOutsideCorpus(t *testing.T) {
	f := factsOn(t, "SHFE", "rb2701")
	day := types.NewTradingDay(2026, 9, 15)
	if err := f.Position.Open(types.Buy, day, d("3000"), 1); err != nil {
		t.Fatal(err)
	}
	base := Request{Instrument: f.Instrument.ID, Hedge: types.Speculation, Price: d("3000"), Volume: 2}
	cases := []struct {
		name string
		mut  func(*Request, *Facts)
		want ctperr.Reason
	}{
		{"平今 2 手而今仓 1 手（平今没有语料）", func(r *Request, _ *Facts) { r.Direction, r.Offset = types.Sell, types.CloseToday }, ctperr.ReasonUnknown},
		{"资金不够（没有语料）", func(r *Request, f *Facts) {
			r.Direction, r.Offset, r.Volume = types.Buy, types.Open, 1
			f.Available = d("0")
		}, ctperr.ReasonUnknown},
		{"合约不可交易（没有语料）", func(r *Request, f *Facts) {
			r.Direction, r.Offset, r.Volume = types.Buy, types.Open, 1
			f.Instrument.IsTrading = false
		}, ctperr.ReasonUnknown},
		{"平昨而昨仓为 0（语料那一条）", func(r *Request, _ *Facts) { r.Direction, r.Offset = types.Sell, types.CloseYesterday }, ctperr.ReasonCloseYesterdayExceeds},
	}
	for _, c := range cases {
		r, ff := base, f
		c.mut(&r, &ff)
		res := Validate(r, ff)
		if res.Rejected == nil {
			t.Errorf("%s：没拒（%v）", c.name, res)
			continue
		}
		if res.Rejected.Kind != c.want {
			t.Errorf("⚠️ %s：拒因是 %s，要 %s", c.name, res.Rejected.Kind, c.want)
		}
	}
	// ⚠️ 「有昨仓但不够」：结算出 1 手昨仓再平昨 2 手 —— 语料只测过昨仓为 0，这里必须不给拒因。
	g := factsOn(t, "SHFE", "rb2701")
	if err := g.Position.Open(types.Buy, day, d("3000"), 1); err != nil {
		t.Fatal(err)
	}
	if err := g.Position.Settle(day, d("3000"), types.NewTradingDay(2026, 9, 16)); err != nil {
		t.Fatal(err)
	}
	r := Request{Instrument: g.Instrument.ID, Direction: types.Sell, Offset: types.CloseYesterday,
		Hedge: types.Speculation, Price: d("3000"), Volume: 2}
	res := Validate(r, g)
	if res.Rejected == nil || res.Rejected.Check != CheckClosable {
		t.Fatalf("前提：平昨 2 手而昨仓 1 手要拒在可平量，得到 %v", res)
	}
	if res.Rejected.Kind != ctperr.ReasonUnknown {
		t.Errorf("⚠️ 昨仓有 1 手、平昨 2 手时拒因是 %s —— 语料只测过昨仓为 0，不许推过来", res.Rejected.Kind)
	}
}
