package pnl

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// rb 的乘数是 10 吨/手。⚠️ 这里写死只是测试夹具；
// 生产路径上乘数来自 refdata，本包把它当必填参数。
var mult = d("10")

// 一段昨仓：开仓价 3150，经一次结算后基线推进到 3160。
var histLeg = Leg{Volume: 2, OpenPrice: d("3150"), Basis: d("3160")}

// 一段今仓：两条基线都是开仓价。
var todayLeg = Leg{Volume: 1, OpenPrice: d("3170"), Basis: d("3170")}

func px(last, pre, settle string) Prices {
	p := Prices{}
	if last != "" {
		p.Last, p.HasLast = d(last), true
	}
	if pre != "" {
		p.PreSettlement, p.HasPreSettlement = d(pre), true
	}
	if settle != "" {
		p.Settlement, p.HasSettlement = d(settle), true
	}
	return p
}

// TestCloseProfitTwoStandardsDiffer 断言两套口径在**昨仓**上给出不同的数。
func TestCloseProfitTwoStandardsDiffer(t *testing.T) {
	r, err := CloseProfit([]Leg{histLeg, todayLeg}, types.Buy, d("3180"), mult)
	if err != nil {
		t.Fatal(err)
	}
	// 逐日盯市：(3180−3160)×2 + (3180−3170)×1 = 40+10 = 50，×10 = 500
	// 逐笔对冲：(3180−3150)×2 + (3180−3170)×1 = 60+10 = 70，×10 = 700
	if !r.ByDate.Equal(d("500")) {
		t.Errorf("逐日盯市应为 500，实为 %s", r.ByDate)
	}
	if !r.ByTrade.Equal(d("700")) {
		t.Errorf("逐笔对冲应为 700，实为 %s", r.ByTrade)
	}
	if r.Equal() {
		t.Error("⚠️ 两套口径在本样本上同值 —— 那样这条测试没有判别力")
	}
}

// TestTodayOnlySampleHasNoDiscriminatingPower 把「纯今仓测不出两套口径」钉成测试。
//
// ⚠️ 今仓的两条基线本就都是开仓价，于是纯今仓的样本上两套口径**必然相等**。
// 拿这种样本去「验证两套口径都实现了」，验不出任何东西 ——
// 与「空仓时两个权益口径必然相等」是同一个形状。
func TestTodayOnlySampleHasNoDiscriminatingPower(t *testing.T) {
	r, err := CloseProfit([]Leg{todayLeg}, types.Buy, d("3180"), mult)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Equal() {
		t.Fatalf("前提变了：纯今仓样本上两套口径本应相等，实为 %s vs %s —— "+
			"若确实分开了，说明 Leg 的构造变了，这条说明要同步更新", r.ByDate, r.ByTrade)
	}
}

// TestSellSignFlips 断言空头方向的符号是反的。
func TestSellSignFlips(t *testing.T) {
	buy, err := CloseProfit([]Leg{histLeg}, types.Buy, d("3180"), mult)
	if err != nil {
		t.Fatal(err)
	}
	sell, err := CloseProfit([]Leg{histLeg}, types.Sell, d("3180"), mult)
	if err != nil {
		t.Fatal(err)
	}
	if !buy.ByDate.Equal(sell.ByDate.Neg()) || !buy.ByTrade.Equal(sell.ByTrade.Neg()) {
		t.Errorf("空头应是多头的相反数：多 %s/%s，空 %s/%s",
			buy.ByDate, buy.ByTrade, sell.ByDate, sell.ByTrade)
	}
	if buy.ByDate.IsZero() {
		t.Error("⚠️ 多头盈亏为零 —— 那样正负号测试没有判别力")
	}
}

// TestFloatDiffersOnHistory 是把持仓盈亏与浮动盈亏分开的判别点。
//
//	今仓：两条基线都是开仓价 → 两者必然相等，**没有判别力**
//	昨仓：基线不同           → 两者必须不等
func TestFloatDiffersOnHistory(t *testing.T) {
	prices := px("3200", "3160", "")

	// 今仓：应当相等。
	pTod, err := PositionProfit([]Leg{todayLeg}, types.Buy, prices, MarkLast, mult)
	if err != nil {
		t.Fatal(err)
	}
	fTod, err := FloatProfit([]Leg{todayLeg}, types.Buy, prices, MarkLast, mult)
	if err != nil {
		t.Fatal(err)
	}
	if !pTod.Equal(fTod) {
		t.Errorf("⚠️ 今仓下两者本应相等（基线都是开仓价），实为 %s vs %s", pTod, fTod)
	}

	// 昨仓：必须不等。
	pHis, err := PositionProfit([]Leg{histLeg}, types.Buy, prices, MarkLast, mult)
	if err != nil {
		t.Fatal(err)
	}
	fHis, err := FloatProfit([]Leg{histLeg}, types.Buy, prices, MarkLast, mult)
	if err != nil {
		t.Fatal(err)
	}
	// 持仓盈亏 (3200−3160)×2×10 = 800；浮动盈亏 (3200−3150)×2×10 = 1000
	if !pHis.Equal(d("800")) {
		t.Errorf("昨仓持仓盈亏应为 800，实为 %s", pHis)
	}
	if !fHis.Equal(d("1000")) {
		t.Errorf("昨仓浮动盈亏应为 1000，实为 %s", fHis)
	}
	if pHis.Equal(fHis) {
		t.Error("⚠️ 昨仓下两者相等 —— 那说明 Basis 没起作用")
	}
}

// TestMarkPreSettlementZeroesHistoryProfit 钉住实验 2 两个候选的**判别特征**。
//
// ⚠️ 若盘中用昨结算价计价，昨仓的持仓盈亏**恒为零**（基线与计价价相同），
// 只有今仓会随行情动。这是它与「用最新价」最容易分开的地方，
// 也是 21:00 那轮实验要看的东西。
func TestMarkPreSettlementZeroesHistoryProfit(t *testing.T) {
	prices := px("3200", "3160", "")
	got, err := PositionProfit([]Leg{histLeg}, types.Buy, prices, MarkPreSettlement, mult)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("⚠️ 用昨结算价计价时昨仓持仓盈亏应恒为零，实为 %s", got)
	}
	// 而今仓在同一基准下不为零 —— 否则这条特征没有判别力。
	todayGot, err := PositionProfit([]Leg{todayLeg}, types.Buy, prices, MarkPreSettlement, mult)
	if err != nil {
		t.Fatal(err)
	}
	if todayGot.IsZero() {
		t.Error("⚠️ 今仓在同一基准下也为零 —— 那样这条判别特征失效")
	}
}

// TestMarkUnmeasuredRefuses 断言未实测的计价基准**拒绝运行**。
func TestMarkUnmeasuredRefuses(t *testing.T) {
	prices := px("3200", "3160", "3165")
	fns := []struct {
		name string
		err  error
	}{
		{"PositionProfit", mustErr2(PositionProfit([]Leg{histLeg}, types.Buy, prices, MarkUnmeasured, mult))},
		{"FloatProfit", mustErr2(FloatProfit([]Leg{histLeg}, types.Buy, prices, MarkUnmeasured, mult))},
	}
	if len(fns) != 2 {
		t.Fatalf("用例数应为 2，实际 %d", len(fns))
	}
	for _, f := range fns {
		if f.err == nil {
			t.Errorf("⚠️ %s 在计价基准未实测时本该报错，而不是挑一个默认", f.name)
			continue
		}
		if !strings.Contains(f.err.Error(), "实验 1/2") {
			t.Errorf("%s 的报错应指向实验 1/2，实为 %v", f.name, f.err)
		}
	}
}

// TestMissingPriceIsNotZero 断言「没有这个价」与「价是零」被分开。
func TestMissingPriceIsNotZero(t *testing.T) {
	cases := []struct {
		name   string
		prices Prices
		mark   Mark
	}{
		{"要最新价但没有", px("", "3160", "3165"), MarkLast},
		{"要昨结算价但没有", px("3200", "", "3165"), MarkPreSettlement},
		{"要今结算价但没有", px("3200", "3160", ""), MarkSettlement},
	}
	if len(cases) != 3 {
		t.Fatalf("反例数应为 3，实际 %d", len(cases))
	}
	for _, c := range cases {
		if _, err := PositionProfit([]Leg{histLeg}, types.Buy, c.prices, c.mark, mult); err == nil {
			t.Errorf("⚠️ %s，本该报错而不是拿 0 顶替", c.name)
		}
	}
}

// TestRejectsBadInputs 覆盖乘数与各段的校验。
func TestRejectsBadInputs(t *testing.T) {
	good := []Leg{histLeg}
	cases := []struct {
		name string
		err  error
	}{
		{"乘数为零（漏乘乘数）", mustErr1(CloseProfit(good, types.Buy, d("3180"), decimal.Zero))},
		{"乘数为负", mustErr1(CloseProfit(good, types.Buy, d("3180"), d("-10")))},
		{"平仓价为零", mustErr1(CloseProfit(good, types.Buy, decimal.Zero, mult))},
		{"买卖方向未指定", mustErr1(CloseProfit(good, types.DirectionUnknown, d("3180"), mult))},
		{"手数为零", mustErr1(CloseProfit([]Leg{{Volume: 0, OpenPrice: d("1"), Basis: d("1")}}, types.Buy, d("3180"), mult))},
		{"开仓价为零", mustErr1(CloseProfit([]Leg{{Volume: 1, OpenPrice: decimal.Zero, Basis: d("1")}}, types.Buy, d("3180"), mult))},
		{"基线为零", mustErr1(CloseProfit([]Leg{{Volume: 1, OpenPrice: d("1"), Basis: decimal.Zero}}, types.Buy, d("3180"), mult))},
	}
	if len(cases) != 7 {
		t.Fatalf("反例数应为 7，实际 %d", len(cases))
	}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("%s 本该报错", c.name)
		}
	}
}

func mustErr1(_ Result, err error) error          { return err }
func mustErr2(_ decimal.Decimal, err error) error { return err }

// TestMultiplierIsLinear 是「乘数漏乘」的属性测试。
//
// ⚠️ 漏乘乘数得到的是一个**量级正确到肉眼看不出**的错值，
// 所以要用「结果对乘数线性」这条性质把它钉住。
func TestMultiplierIsLinear(t *testing.T) {
	base, err := CloseProfit([]Leg{histLeg}, types.Buy, d("3180"), d("10"))
	if err != nil {
		t.Fatal(err)
	}
	doubled, err := CloseProfit([]Leg{histLeg}, types.Buy, d("3180"), d("20"))
	if err != nil {
		t.Fatal(err)
	}
	if !doubled.ByDate.Equal(base.ByDate.Mul(d("2"))) {
		t.Errorf("乘数翻倍时逐日盯市应翻倍：%s → %s", base.ByDate, doubled.ByDate)
	}
	if !doubled.ByTrade.Equal(base.ByTrade.Mul(d("2"))) {
		t.Errorf("乘数翻倍时逐笔对冲应翻倍：%s → %s", base.ByTrade, doubled.ByTrade)
	}
	if base.ByDate.IsZero() {
		t.Error("⚠️ 基准值为零 —— 线性性质在零上恒成立，没有判别力")
	}
}

// TestSolveBasisRecoversAndRefuses 覆盖反解的正向与拒绝。
func TestSolveBasisRecoversAndRefuses(t *testing.T) {
	cases := []struct {
		name                     string
		markPx, openPx, baseline string
		vol, mul                 string
	}{
		{"昨结算低于开仓价", "3200", "3150", "3140", "2", "10"},
		{"昨结算高于开仓价", "3100", "3150", "3180", "3", "10"},
		{"乘数很大（沪金）", "512.50", "512.34", "511.90", "8", "1000"},
		{"手数与乘数都变，解应不变", "3200", "3150", "3140", "7", "300"},
	}
	if len(cases) != 4 {
		t.Fatalf("用例数应为 4，实际 %d", len(cases))
	}
	for _, c := range cases {
		markPx, openPx, base := d(c.markPx), d(c.openPx), d(c.baseline)
		q := d(c.vol).Mul(d(c.mul))
		fp := markPx.Sub(openPx).Mul(q)
		pp := markPx.Sub(base).Mul(q)
		got, ok := SolveBasis(markPx, openPx, fp, pp)
		if !ok {
			t.Errorf("%s: 反解失败", c.name)
			continue
		}
		if got.Sub(base).Abs().GreaterThan(d("0.000001")) {
			t.Errorf("%s: 反解 = %s，期望 %s", c.name, got, base)
		}
	}

	// ⚠️ 分母接近零时必须报「解不出」，不是返回一个看起来合理的数。
	// 解不出来是「测不出」，不是「基线等于开仓价」。
	bad := []struct{ mark, open, fp, pp string }{
		{"3150", "3150", "0", "12.5"},
		{"3150", "3150", "0.0000000001", "12.5"},
	}
	if len(bad) != 2 {
		t.Fatalf("反例数应为 2，实际 %d", len(bad))
	}
	for _, c := range bad {
		if _, ok := SolveBasis(d(c.mark), d(c.open), d(c.fp), d(c.pp)); ok {
			t.Errorf("浮动盈亏 %s 时本该报解不出，却返回了一个值", c.fp)
		}
	}
}

// TestDayProfit 覆盖当日盈亏。
func TestDayProfit(t *testing.T) {
	if got := DayProfit(d("500"), d("-200")); !got.Equal(d("300")) {
		t.Errorf("当日盈亏应为 300，实为 %s", got)
	}
}
