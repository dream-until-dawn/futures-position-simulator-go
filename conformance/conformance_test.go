package conformance

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var tol = d("0.0001") // 一分钱的百分之一

// TestUntriggeredIsNotAPass 断言「值对得上但从未触发」**不算通过**。
//
// ⚠️ 这一档是整套判定里最容易被当成通过的：值确实一样。
// 本仓库第一次连上快期就撞见了它的样子 —— 空仓时
// balance == ctp_balance == 1000000.0，看起来完全一致，实际什么都没证明。
func TestUntriggeredIsNotAPass(t *testing.T) {
	r := Classify("合成样本", []Field{
		{Name: "balance", Library: d("1000000"), Oracle: d("1000000"), Triggered: false},
	}, tol)
	if r.Verdicts["balance"] != Untriggered {
		t.Errorf("值相同但未触发，应判 Untriggered，实为 %v", r.Verdicts["balance"])
	}
	if r.Passed() {
		t.Error("⚠️ 只有「对得上但未触发」的一批字段被判为通过 —— " +
			"那正是零值假通过，这一档存在的全部意义就是不让它通过")
	}
	// 对照：同样的值，触发过就通过。
	r2 := Classify("合成样本", []Field{
		{Name: "balance", Library: d("1000000"), Oracle: d("1000000"), Triggered: true},
	}, tol)
	if !r2.Passed() {
		t.Errorf("对得上且触发过应当通过：%s", r2.Summary())
	}
}

// TestDeviationNeedsAllThreeGates 断言第四档的三条硬门槛缺一不可。
//
// ⚠️ 「已知口子差异」是唯一可能被滥用的一档。
// 一个可以随口声明的豁免档，比没有这一档更坏 —— 它会把真实的不一致洗成「已知」。
func TestDeviationNeedsAllThreeGates(t *testing.T) {
	full := &Deviation{
		Fixture: "testdata/probes/exp-fee-base-20260908.json",
		Arbiter: "state.md simnow_pending #7（基准价）",
		Chose:   "本库按 CTP 建模（成交价）",
	}
	cases := []struct {
		name string
		dev  *Deviation
		want Verdict
	}{
		{"三样齐备", full, KnownDeviation},
		{"缺出处", &Deviation{Arbiter: full.Arbiter, Chose: full.Chose}, Failed},
		{"缺裁决者", &Deviation{Fixture: full.Fixture, Chose: full.Chose}, Failed},
		{"缺选边理由", &Deviation{Fixture: full.Fixture, Arbiter: full.Arbiter}, Failed},
		{"三样全缺", &Deviation{}, Failed},
	}
	if len(cases) != 5 {
		t.Fatalf("用例 %d 条，应为 5 —— 增删了就同步改这个数", len(cases))
	}
	for _, c := range cases {
		r := Classify("合成样本", []Field{
			{Name: "commission", Library: d("0.3092"), Oracle: d("0.3100"),
				Triggered: true, Deviation: c.dev},
		}, tol)
		if got := r.Verdicts["commission"]; got != c.want {
			t.Errorf("%s：判为 %v，应为 %v", c.name, got, c.want)
		}
	}
}

// TestDeviationMustBeTriggered 断言未触发的「已知差异」是一句空话。
func TestDeviationMustBeTriggered(t *testing.T) {
	r := Classify("合成样本", []Field{
		{Name: "commission", Library: d("0.3092"), Oracle: d("0.3100"), Triggered: false,
			Deviation: &Deviation{Fixture: "x.json", Arbiter: "y", Chose: "z"}},
	}, tol)
	if r.Verdicts["commission"] != Failed {
		t.Errorf("未触发的已知差异应判失败，实为 %v", r.Verdicts["commission"])
	}
	if len(r.Errs) == 0 {
		t.Error("应当给出「没被触发的差异是一句空话」这条错误")
	}
}

// TestKnownDeviationIsNotAPass 断言已知差异**也不算通过**。
//
// ⚠️ 它是一笔记在账上的欠款，不是一次验收。
// 若它算通过，「已知差异」就成了让验收变绿的最省力途径。
func TestKnownDeviationIsNotAPass(t *testing.T) {
	r := Classify("合成样本", []Field{
		{Name: "margin", Library: d("2210.60"), Oracle: d("2210.60"), Triggered: true},
		{Name: "commission", Library: d("0.3092"), Oracle: d("0.3100"), Triggered: true,
			Deviation: &Deviation{Fixture: "x.json", Arbiter: "y", Chose: "z"}},
	}, tol)
	if r.Passed() {
		t.Error("⚠️ 含已知口子差异的一批字段被判为通过 —— " +
			"那会让「声明差异」成为让验收变绿的最省力途径")
	}
	if r.Counts[KnownDeviation] != 1 || r.Counts[Matched] != 1 {
		t.Errorf("计数不对：%s", r.Summary())
	}
}

// TestClassifyRefusesBadInput 断言判定本身的守卫。
func TestClassifyRefusesBadInput(t *testing.T) {
	for _, c := range []struct {
		name   string
		fields []Field
		tol    decimal.Decimal
		msgHas string
	}{
		{"容差为零", []Field{{Name: "a", Triggered: true}}, decimal.Zero, "容差必须为正"},
		{"容差为负", []Field{{Name: "a", Triggered: true}}, d("-1"), "容差必须为正"},
		{"字段没名字", []Field{{Triggered: true}}, tol, "没有名字"},
		{"字段重名", []Field{{Name: "a", Triggered: true}, {Name: "a", Triggered: true}}, tol, "重复出现"},
	} {
		r := Classify("合成样本", c.fields, c.tol)
		if len(r.Errs) == 0 {
			t.Errorf("⚠️ %s：没有报错", c.name)
			continue
		}
		if !strings.Contains(r.Errs[0].Error(), c.msgHas) {
			t.Errorf("%s：错误信息里没有 %q：%v", c.name, c.msgHas, r.Errs[0])
		}
		if r.Passed() {
			t.Errorf("⚠️ %s：有错误却判为通过", c.name)
		}
	}
	// 空样本不算通过。
	if Classify("空样本", nil, tol).Passed() {
		t.Error("⚠️ 空样本被判为通过 —— 一个字段都没比，什么都没证明")
	}
}

// TestMarginConformanceAgainstKQ 是一次**真实**对拍：
// 本库的 margin.Compute 能不能重现快期模拟量到的每手保证金。
//
// 输入全部来自交易日 20260908 夜盘的实测（probes.md §7.2）：
//
//	rb2701  昨结 3158  乘数 10  费率 7%   柜台给 2210.60/手
//	m2701   昨结 3404  乘数 10  费率 7%   柜台给 2382.80/手
//	i2701   昨结 734.5 乘数 100 费率 11%  柜台给 8079.50/手
//	cu2701  昨结 108330 乘数 5  费率 11%  柜台给 59581.50/手
//	ag2702  昨结 16095 乘数 15  费率 22%  柜台给 53113.50/手
//
// ⚠️ 费率是从这些数**反解**出来的，所以单看一个合约是同义反复。
// 有判别力的是**跨合约**：五个合约、三档费率、两个交易所，
// 用同一个公式和各自的费率重现全部五个数。
func TestMarginConformanceAgainstKQ(t *testing.T) {
	cases := []struct {
		sym        string
		ex         types.Exchange
		product    string
		year       int
		month      int
		preSettle  string
		multiplier string
		rate       string
		oracle     string
	}{
		{"rb2701", types.SHFE, "rb", 2027, 1, "3158", "10", "0.07", "2210.60"},
		{"m2701", types.DCE, "m", 2027, 1, "3404", "10", "0.07", "2382.80"},
		{"i2701", types.DCE, "i", 2027, 1, "734.5", "100", "0.11", "8079.50"},
		{"cu2701", types.SHFE, "cu", 2027, 1, "108330", "5", "0.11", "59581.50"},
		{"ag2702", types.SHFE, "ag", 2027, 2, "16095", "15", "0.22", "53113.50"},
	}
	if len(cases) != 5 {
		t.Fatalf("对拍样本 %d 个，应为 5 —— 增删了就同步改这个数", len(cases))
	}
	// ⚠️ 判别力守卫：费率必须**不止一档**，否则「跨合约都对」只是同一次验证做了五遍。
	rates := map[string]bool{}
	for _, c := range cases {
		rates[c.rate] = true
	}
	if len(rates) < 2 {
		t.Fatalf("⚠️ 五个样本只有 %d 档费率 —— 跨合约就没有判别力了，"+
			"那是「一次观测做了五遍」，不是五次独立复核", len(rates))
	}

	var fields []Field
	for _, c := range cases {
		id := types.InstrumentID{Exchange: c.ex, Product: c.product, Year: c.year, Month: c.month}
		leg := margin.Leg{
			Instrument: id, Direction: types.Buy, Volume: 1,
			Multiplier: d(c.multiplier),
			Rates: refdata.MarginRates{
				LongByMoney: d(c.rate), LongByVolume: decimal.Zero,
				ShortByMoney: d(c.rate), ShortByVolume: decimal.Zero,
				CompanyAddOn: decimal.Zero,
			},
			IsHistory:        false,
			MaxMarginSide:    false, // 实测：快期上大边未启用（probes.md §7.2）
			PreSettlement:    d(c.preSettle),
			HasPreSettlement: true,
		}
		// ⚠️ PreSettleAll 是实测定下的基准（cn-futures-rules.md §6，今仓用昨结算价）。
		res, err := margin.Compute([]margin.Leg{leg}, margin.PreSettleAll, margin.ByInstrument)
		if err != nil {
			t.Fatalf("%s：%v", c.sym, err)
		}
		fields = append(fields, Field{
			Name: c.sym + ".margin", Library: res.Exchange, Oracle: d(c.oracle),
			Triggered: true, // 本次样本里这个字段确实被算了、也确实非零
		})
	}

	r := Classify("交易日 20260908 夜盘实测（probes.md §7.2）", fields, tol)
	t.Log("\n" + r.Summary())
	if !r.Passed() {
		t.Errorf("⚠️ 保证金对拍未通过：\n%s", r.Summary())
	}
}

// TestFeeConformanceShowsTheKnownDeviation 是一次**故意对不上**的对拍。
//
// 本库的 fee.Compute 按 CTP 建模：按额手续费用**成交价**。
// 快期模拟实测用的是**昨结算价**（probes.md §7，同合约一买一卖、
// 成交价差一个 tick 而费额完全相同）。
//
// ⚠️ 两边各自都对，值却不同 —— 这正是第四档存在的理由。
// 本测试的意义不是「让它绿」，是**让这笔欠款出现在账上并且不被读成通过**。
func TestFeeConformanceShowsTheKnownDeviation(t *testing.T) {
	rates := refdata.CommissionRates{
		OpenByMoney: d("0.00001"), OpenByVolume: decimal.Zero,
		CloseByMoney: d("0.00001"), CloseByVolume: decimal.Zero,
		CloseTodayByMoney: d("0.00001"), CloseTodayByVolume: decimal.Zero,
	}
	// rb2610：成交价 3092，昨结算价 3100，乘数 10，柜台给 0.3100。
	byTrade, err := fee.Compute(rates, types.Open, d("3092"), d("10"), 1, decimalx.NoRounding)
	if err != nil {
		t.Fatal(err)
	}
	bySettle, err := fee.Compute(rates, types.Open, d("3100"), d("10"), 1, decimalx.NoRounding)
	if err != nil {
		t.Fatal(err)
	}
	// 前提核对：两者必须**不同**，否则本条没有判别力。
	if byTrade.Equal(bySettle) {
		t.Fatalf("⚠️ 用成交价与用昨结算价算出同一个数（%s）—— "+
			"那本条什么都说明不了，换一个成交价与昨结算价不同的样本", byTrade)
	}
	if !bySettle.Equal(d("0.31")) {
		t.Fatalf("前提变了：用昨结算价应算出 0.31，实为 %s", bySettle)
	}

	r := Classify("交易日 20260908 夜盘实测（probes.md §7）", []Field{
		{
			Name: "rb2610.open_commission", Library: byTrade, Oracle: d("0.3100"),
			Triggered: true,
			Deviation: &Deviation{
				Fixture: "testdata/probes/exp-fee-base-20260908.json",
				Arbiter: "state.md simnow_pending #7（保证金/手续费的基准价）",
				Chose: "本库按 CTP 建模，用**成交价**；快期模拟用昨结算价。" +
					"选 CTP 是因为本库的目标是真实柜台，而快期是对拍口子之一。" +
					"⚠️ 差额在同一天内是常数，因此最容易被读成「对上了」",
			},
		},
	}, tol)
	if r.Verdicts["rb2610.open_commission"] != KnownDeviation {
		t.Errorf("应判为已知口子差异，实为 %v", r.Verdicts["rb2610.open_commission"])
	}
	if r.Passed() {
		t.Error("⚠️ 一笔已知欠款被判成了验收通过")
	}
	t.Logf("本库（成交价）%s vs 快期（昨结算价）%s，差 %s\n%s",
		byTrade, bySettle, bySettle.Sub(byTrade), r.Summary())
}

// TestAbsentIsNotZero 断言「无值」与「零」在判定里是两回事。
//
// ⚠️ 这条堵的是一个**能双向全绿**的洞，而它是从数据里逼出来的：
// 快期在空仓方向上对 open_price / position_price / margin
// 返回字符串 "-" 而不是 0（实测 188/188，probes.md §9）。
// 对拍侧若把 "-" 解析成 0，这些字段会碰巧一致；
// 若把 "-" 当成缺失整个跳过，它们会全部落进「未触发」。
// 两条路都能让测试全绿，而它们互相矛盾 —— 所以「无值」必须有自己的表示，
// 且**一侧无值一侧有值必须判失败**。
func TestAbsentIsNotZero(t *testing.T) {
	// ① 一侧说没有、一侧给 0 —— 这正是要抓的那一类。
	r := Classify("合成样本", []Field{
		{Name: "open_price_short", LibraryAbsent: true, Oracle: decimal.Zero, Triggered: true},
	}, tol)
	if got := r.Verdicts["open_price_short"]; got != Failed {
		t.Errorf("⚠️ 本库说「没有」而口子给 0，应判失败，实为 %v —— "+
			"判成一致等于把 \"-\" 读成了 0", got)
	}
	// ② 反向也一样：口子说没有、本库给 0。
	r2 := Classify("合成样本", []Field{
		{Name: "margin_short", Library: decimal.Zero, OracleAbsent: true, Triggered: true},
	}, tol)
	if got := r2.Verdicts["margin_short"]; got != Failed {
		t.Errorf("⚠️ 口子说「没有」而本库给 0，应判失败，实为 %v", got)
	}
	// ③ 两侧都说没有 —— 一致，但仍要求触发过才算通过。
	r3 := Classify("合成样本", []Field{
		{Name: "margin_short", LibraryAbsent: true, OracleAbsent: true, Triggered: true},
	}, tol)
	if got := r3.Verdicts["margin_short"]; got != Matched {
		t.Errorf("两侧都说没有且触发过，应判 Matched，实为 %v", got)
	}
	if !r3.Passed() {
		t.Errorf("两侧都说没有且触发过应当通过：%s", r3.Summary())
	}
	// ④ 两侧都说没有但**未触发** —— 不算通过。
	//
	// ⚠️ 这一条比看起来重要：空仓时几乎每个字段两侧都「没有」，
	// 若「都没有」直接算通过，一份空仓截面能把整套验收刷成全绿。
	r4 := Classify("合成样本", []Field{
		{Name: "margin_short", LibraryAbsent: true, OracleAbsent: true, Triggered: false},
	}, tol)
	if got := r4.Verdicts["margin_short"]; got != Untriggered {
		t.Errorf("两侧都说没有但未触发，应判 Untriggered，实为 %v", got)
	}
	if r4.Passed() {
		t.Error("⚠️ 一份「两侧都没有且未触发」的样本被判通过 —— " +
			"空仓截面上几乎每个字段都是这个形状，那会把整套验收刷成全绿")
	}
}

// TestAbsentMustNotCarryNumber 断言既声明无值又带着数的字段被拒。
//
// ⚠️ 这样的字段读它的人会各按各的理解取用：
// 有人看 Absent 就跳过，有人看 Library 就拿数 —— 两种读法都「合理」。
func TestAbsentMustNotCarryNumber(t *testing.T) {
	r := Classify("合成样本", []Field{
		{Name: "margin_short", Library: d("1"), LibraryAbsent: true,
			OracleAbsent: true, Triggered: true},
	}, tol)
	if len(r.Errs) == 0 {
		t.Error("⚠️ 声明为无值却带着数值 1 的字段应当报错")
	}
	if r.Passed() {
		t.Error("⚠️ 带错误的报告不能算通过")
	}
}
