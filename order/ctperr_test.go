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
//
// ⚠️ PositionDateType **按交易所给**（评审 20260915）：大商所 NoUseHistory，上期所 / 能源中心 UseHistory ——
// 上一版一律 UseHistory，于是「大商所」那几行其实是按上期所的今昨模型跑的。
func factsOn(t *testing.T, ex, inst string) Facts {
	t.Helper()
	id, err := types.ParseSymbol(ex+"."+inst, types.NewTradingDay(2026, 9, 15))
	if err != nil {
		t.Fatal(err)
	}
	pdt := refdata.UseHistory
	if types.Exchange(ex) == types.DCE || types.Exchange(ex) == types.CZCE {
		pdt = refdata.NoUseHistory
	}
	spec := refdata.Instrument{
		ID: id, VolumeMultiple: d("10"), PriceTick: d("1"),
		PositionDateType: pdt, IsTrading: true,
		MinLimitOrderVolume: 1, MaxLimitOrderVolume: 500,
		PriceLimitRatio: d("0.05"), HasPriceLimitRatio: true,
	}
	p, err := position.New(id, types.Speculation, types.NewTradingDay(2026, 9, 15), pdt)
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

// TestRejectionCodesMatchCorpus 是 v0.4.0 验收「被拒报单的错误码一致」在**已拍语料**上的那一行：
// **对语料中 4 种拒因、账上无仓的情形成立**（评审 20260915 要求把范围写准）。
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

// TestKindStaysUnknownOutsideCorpus 钉住语料没测过的拒绝**不给拒因**，而语料那一条（账上无仓时平昨）给。
//
// ⚠️ 20260915 评审打回：上一版把「语料那一条」建在一个已经开了 1 手今仓的持仓上，测的其实是「今 1 / 昨 0」——
// 外推被标成了语料。本版每条用例各自从空仓起步。
func TestKindStaysUnknownOutsideCorpus(t *testing.T) {
	day, next := types.NewTradingDay(2026, 9, 15), types.NewTradingDay(2026, 9, 16)
	closeYd := func(f Facts, vol int) Request {
		return Request{Instrument: f.Instrument.ID, Direction: types.Sell, Offset: types.CloseYesterday,
			Hedge: types.Speculation, Price: d("3000"), Volume: vol}
	}
	kindOf := func(t *testing.T, name string, r Request, f Facts, wantCheck Check) ctperr.Reason {
		t.Helper()
		res := Validate(r, f)
		if res.Rejected == nil || res.Rejected.Check != wantCheck {
			t.Fatalf("%s：前提是拒在 %s，得到 %v", name, wantCheck, res)
		}
		return res.Rejected.Kind
	}

	// 语料那一条：账上无仓时平昨 ⇒ 给拒因
	f := factsOn(t, "SHFE", "rb2701")
	if k := kindOf(t, "无仓平昨", closeYd(f, 1), f, CheckClosable); k != ctperr.ReasonCloseYesterdayExceeds {
		t.Errorf("⚠️ 账上无仓时平昨（语料那一条）拒因是 %s，要 %s", k, ctperr.ReasonCloseYesterdayExceeds)
	}

	// ⚠️ 今 1 / 昨 0 平昨 ⇒ 不给（语料只测过无仓）
	g := factsOn(t, "SHFE", "rb2701")
	if err := g.Position.Open(types.Buy, day, d("3000"), 1); err != nil {
		t.Fatal(err)
	}
	if k := kindOf(t, "今1昨0平昨", closeYd(g, 1), g, CheckClosable); k != ctperr.ReasonUnknown {
		t.Errorf("⚠️ 今 1 / 昨 0 平昨的拒因是 %s —— 语料只测过账上无仓，不许推过来", k)
	}

	// ⚠️⚠️ 大商所（NoUseHistory）开 1 手跨过结算再平昨 ⇒ **不拒**。CTP 实测记作昨仓且接受平昨（#4 夹具 ①）；
	// 本库此前仍记作今仓而拒绝 —— 20260915 评审发现那一版还会配上 CTP 30。§13 #20 使用者裁决跟 CTP 之后，它是昨仓、可平。
	h := factsOn(t, "DCE", "m2701")
	if err := h.Position.Open(types.Buy, day, d("3000"), 1); err != nil {
		t.Fatal(err)
	}
	if err := h.Position.Settle(day, d("3000"), next); err != nil {
		t.Fatal(err)
	}
	if res := Validate(closeYd(h, 1), h); res.Rejected != nil {
		t.Errorf("⚠️ 大商所跨结算后平昨 1 手被拒了：%v —— 柜台会接受这笔单（#4 夹具 ①，§13 #20 跟 CTP）", res.Rejected)
	}

	// ⚠️ 有昨仓但不够：结算出 1 手昨仓再平昨 2 手 ⇒ 不给
	m := factsOn(t, "SHFE", "rb2701")
	if err := m.Position.Open(types.Buy, day, d("3000"), 1); err != nil {
		t.Fatal(err)
	}
	if err := m.Position.Settle(day, d("3000"), next); err != nil {
		t.Fatal(err)
	}
	if k := kindOf(t, "昨1平昨2", closeYd(m, 2), m, CheckClosable); k != ctperr.ReasonUnknown {
		t.Errorf("⚠️ 昨仓有 1 手、平昨 2 手时拒因是 %s —— 语料只测过账上无仓，不许推过来", k)
	}

	// 其余几种没有语料的拒绝 ⇒ 不给
	n := factsOn(t, "SHFE", "rb2701")
	if err := n.Position.Open(types.Buy, day, d("3000"), 1); err != nil {
		t.Fatal(err)
	}
	ct := Request{Instrument: n.Instrument.ID, Direction: types.Sell, Offset: types.CloseToday, Hedge: types.Speculation, Price: d("3000"), Volume: 2}
	if k := kindOf(t, "平今超量", ct, n, CheckClosable); k != ctperr.ReasonUnknown {
		t.Errorf("⚠️ 平今超量的拒因是 %s —— 平今没有语料", k)
	}
	o := factsOn(t, "SHFE", "rb2701")
	o.Available = d("0")
	op := Request{Instrument: o.Instrument.ID, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: d("3000"), Volume: 1}
	if k := kindOf(t, "资金不够", op, o, CheckFunds); k != ctperr.ReasonUnknown {
		t.Errorf("⚠️ 资金不够的拒因是 %s —— 没有语料", k)
	}
}
