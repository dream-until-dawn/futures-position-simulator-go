package match

import (
	"errors"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/ctperr"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func sym(t *testing.T, s string) types.InstrumentID {
	t.Helper()
	id, err := types.ParseSymbol(s, types.NewTradingDay(2026, 9, 9))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// facts 是 SHFE.rb2701 上**八项全都查得了**的一组输入：多头今 0、昨 3（已结算）。
func facts(t *testing.T) order.Facts {
	t.Helper()
	inst := refdata.Instrument{
		ID: sym(t, "SHFE.rb2701"), VolumeMultiple: d("10"), PriceTick: d("1"),
		PositionDateType: refdata.UseHistory, IsTrading: true,
		MinLimitOrderVolume: 1, MaxLimitOrderVolume: 500,
		PriceLimitRatio: d("0.05"), HasPriceLimitRatio: true,
	}
	day := types.NewTradingDay(2026, 9, 8)
	p, err := position.New(inst.ID, types.Speculation, day, refdata.UseHistory)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Open(types.Buy, day, d("3150"), 3); err != nil {
		t.Fatal(err)
	}
	if err := p.Settle(day, d("3163"), types.NewTradingDay(2026, 9, 9)); err != nil {
		t.Fatal(err)
	}
	return order.Facts{
		Instrument: inst, HasInstrument: true,
		PreSettlement: d("3163"), HasPreSettlement: true,
		Rounding:  refdata.TickFloor,
		Position:  p,
		Available: d("100000"), HasAvailable: true,
		Need: d("2214.1"), HasNeed: true,
		PositionLimit: 100, HasPositionLimit: true,
		InSession: true, HasSession: true,
	}
}

func req(t *testing.T, dir types.Direction, off types.Offset, price string, vol int) order.Request {
	return order.Request{Instrument: sym(t, "SHFE.rb2701"), Direction: dir, Offset: off,
		Hedge: types.Speculation, Price: d(price), Volume: vol}
}

// TestFillTradesAtOwnPriceFullVolume 钉住裁决本身：价 = 报价、量 = 全部。
//
// ⚠️ 两笔单的价与量**都不同**：只用一笔的话，一个恒返回 (3163, 1) 的实现也能过。
func TestFillTradesAtOwnPriceFullVolume(t *testing.T) {
	f := facts(t)
	if res := order.Validate(req(t, types.Buy, types.Open, "3163", 1), f); !res.OK() {
		t.Fatalf("⚠️ 地基上这笔开仓就过不了八项：%v —— 下面的断言立不住", res)
	}
	for _, r := range []order.Request{
		req(t, types.Buy, types.Open, "3163", 1),
		req(t, types.Sell, types.CloseYesterday, "3100", 2),
	} {
		tr, err := Fill(r, f)
		if err != nil {
			t.Fatalf("%v %v %s×%d：不该拒，得到 %v", r.Direction, r.Offset, r.Price, r.Volume, err)
		}
		want := Trade{Instrument: r.Instrument, Direction: r.Direction, Offset: r.Offset,
			Hedge: r.Hedge, Price: r.Price, Volume: r.Volume}
		if !tr.Price.Equal(want.Price) || tr.Volume != want.Volume || tr.Instrument != want.Instrument ||
			tr.Direction != want.Direction || tr.Offset != want.Offset || tr.Hedge != want.Hedge {
			t.Errorf("成交 %+v，要 %+v —— 裁决是「按报价、全部手数」", tr, want)
		}
	}
}

// TestFillRefusesRejectedAndUncheckedDifferently 钉住「被拒」与「没查成」都不成交，且**分得开**。
func TestFillRefusesRejectedAndUncheckedDifferently(t *testing.T) {
	// 被拒：价格不是最小变动价位的整数倍
	_, err := Fill(req(t, types.Buy, types.Open, "3163.5", 1), facts(t))
	var rej *RejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("⚠️ 非整数倍价格要以 RejectedError 不成交，得到 %v", err)
	}
	if rej.Rejection.Check != order.CheckPriceTick {
		t.Errorf("拒因是 %s，要 %s", rej.Rejection.Check, order.CheckPriceTick)
	}
	var unc *UncheckedError
	if errors.As(err, &unc) {
		t.Error("⚠️ 被拒的单同时被当成了「没查成」—— 调用方分不出该改单还是该补事实")
	}

	// 没查成：不知道可用资金
	f := facts(t)
	f.HasAvailable = false
	_, err = Fill(req(t, types.Buy, types.Open, "3163", 1), f)
	if !errors.As(err, &unc) {
		t.Fatalf("⚠️ 缺可用资金要以 UncheckedError 不成交，得到 %v —— "+
			"「查不了」若被当成通过，回测会开出实际开不出的仓", err)
	}
	found := false
	for _, u := range unc.Unchecked {
		found = found || u.Check == order.CheckFunds
	}
	if !found || !strings.Contains(err.Error(), "没能查") {
		t.Errorf("没查成的错误要点名资金那一项并说「没能查」：%v", err)
	}
	if errors.As(err, &rej) {
		t.Error("⚠️ 没查成的单被当成了「被拒」")
	}
}

// TestFillRefusesMismatchedInstrument 钉住报单、合约规格、持仓三处必须是同一个合约。
//
// ⚠️ order.Validate 不核对这件事：一笔 ag2702 的单拿着 rb2701 的规格与持仓去查，
// 最小变动价位、涨跌停、可平量全部按 rb2701 算，然后以 ag2702 成交 —— 不报错。
func TestFillRefusesMismatchedInstrument(t *testing.T) {
	ag := req(t, types.Buy, types.Open, "3163", 1)
	ag.Instrument = sym(t, "SHFE.ag2702")
	if res := order.Validate(ag, facts(t)); !res.OK() {
		t.Fatalf("⚠️ 前提：order.Validate 本身**放过**这笔错配的单（它不核对合约）—— 现在它不放过了：%v。"+
			"那是好事，但本条的动机要跟着改", res)
	}
	if _, err := Fill(ag, facts(t)); err == nil || !strings.Contains(err.Error(), "合约规格") {
		t.Errorf("⚠️ 报单与合约规格不是同一合约，却成交了：%v", err)
	}
	// 只有持仓错配
	f := facts(t)
	other, err := position.New(sym(t, "SHFE.ag2702"), types.Speculation, types.NewTradingDay(2026, 9, 9), refdata.UseHistory)
	if err != nil {
		t.Fatal(err)
	}
	f.Position = other
	if _, err := Fill(req(t, types.Buy, types.Open, "3163", 1), f); err == nil || !strings.Contains(err.Error(), "持仓") {
		t.Errorf("⚠️ 报单与持仓不是同一合约，却成交了：%v", err)
	}
}

// TestPackageDocStatesBothDeviations 钉住包文档**同时**写着两个偏离，而且方向配对没写反。
//
// ⚠️ 这是 roadmap 对「不做盘口」那条裁决的落地要求：两个方向写在导出面，不埋在实现里。
// 而这段话在任何对拍上都不会红 —— 夹具不留盘口 —— 于是只能由本条守住它不被删、不被改反。
func TestPackageDocStatesBothDeviations(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "match.go", nil, parser.ParseComments|parser.PackageClauseOnly)
	if err != nil || f.Doc == nil {
		t.Fatalf("读不到 match 的包文档：%v", err)
	}
	doc := f.Doc.Text()
	for _, must := range []string{"100% 全量成交", "silent-risks.md 第 9 条", "价格维度", "成交与否维度"} {
		if !strings.Contains(doc, must) {
			t.Errorf("⚠️ 包文档里没有 %q", must)
		}
	}
	// ⚠️ 配对：「保守」必须落在价格维度那一段，「乐观」必须落在成交与否维度那一段。
	// 只查两个词都在的话，把方向写反的文档也能过 —— 而写反正是「只记住一个维度」的人最容易犯的。
	p, q := strings.Index(doc, "价格维度"), strings.Index(doc, "成交与否维度")
	cons, opt := strings.Index(doc, "保守"), strings.Index(doc, "乐观")
	if p < 0 || q < 0 || !(p < cons && cons < q) || !(q < opt) {
		t.Errorf("⚠️ 方向配对不对：价格维度@%d 保守@%d 成交与否维度@%d 乐观@%d —— "+
			"要「价格 ⇒ 保守」「成交与否 ⇒ 乐观」", p, cons, q, opt)
	}
}

// TestMatchDoesNotTouchState 钉住 match 不 import 状态层：成交记录与记账分开。
func TestMatchDoesNotTouchState(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "match.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Imports) == 0 {
		t.Fatal("⚠️ 一个 import 都没读到 —— 本条在空集上跑")
	}
	for _, im := range f.Imports {
		path, _ := strconv.Unquote(im.Path.Value)
		for _, banned := range []string{"/position", "/account"} {
			if strings.HasSuffix(path, banned) {
				t.Errorf("⚠️ match 直接 import 了 %s —— 撮合规则与记账规则会长进同一个函数", path)
			}
		}
	}
}

// TestRejectedErrorCarriesCode 钉住被拒的成交错误能说出柜台会给的码，没有观测时说查不到。
func TestRejectedErrorCarriesCode(t *testing.T) {
	_, err := Fill(req(t, types.Buy, types.Open, "3163.5", 1), facts(t))
	var rej *RejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("前提：非整数倍价格要被拒，得到 %v", err)
	}
	if rej.Exchange != types.SHFE {
		t.Errorf("⚠️ RejectedError 没带上交易所：%q —— 同一拒因的码随交易所变，查不了", rej.Exchange)
	}
	if c, ok := rej.Code(); !ok || c != (ctperr.Code{Space: ctperr.SpaceStatusPrefix, Value: 48}) {
		t.Errorf("⚠️ 上期所最小变动价位要查到前缀码 48，得到 %v ok=%v", c, ok)
	}
	// ⚠️ 反向：没拍过的交易所、没有语料粒度的拒因，都要查不到。
	czce := &RejectedError{Rejection: order.Rejection{Kind: ctperr.ReasonPriceTick}, Exchange: types.CZCE}
	if c, ok := czce.Code(); ok {
		t.Errorf("⚠️ 郑商所查到了 %v —— 一条语料都没有", c)
	}
	funds := &RejectedError{Rejection: order.Rejection{Check: order.CheckFunds}, Exchange: types.SHFE}
	if c, ok := funds.Code(); ok {
		t.Errorf("⚠️ 资金不足查到了 %v —— 它没有语料", c)
	}
}
