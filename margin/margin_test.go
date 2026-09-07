package margin

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

const day = types.TradingDay(20260907)

func inst(t *testing.T, ex types.Exchange, code string) types.InstrumentID {
	t.Helper()
	id, err := types.ParseNative(ex, code, day)
	if err != nil {
		t.Fatalf("解析 %s.%s 失败: %v", ex, code, err)
	}
	return id
}

// 10% 保证金率，无加收。
var rate10 = Rates{
	LongByMoney:  d("0.10"),
	ShortByMoney: d("0.10"),
}

func leg(t *testing.T, code string, dir types.Direction, vol int, open, pre string, hist, maxSide bool) Leg {
	t.Helper()
	l := Leg{
		Instrument: inst(t, types.SHFE, code),
		Direction:  dir, Volume: vol,
		Multiplier: d("10"), Rates: rate10,
		IsHistory:     hist,
		MaxMarginSide: maxSide,
		OpenPrice:     d(open),
	}
	if pre != "" {
		l.PreSettlement, l.HasPreSettlement = d(pre), true
	}
	return l
}

// TestBasisChangesResult 断言四个计价基准在**同一持仓**上给出不同的数。
//
// 这正是实验 1 需要被实测的原因：候选 1/2 下盘中保证金基本静态，
// 候选 3 下它随行情连续变动，两种形态的可用资金曲线完全不同。
func TestBasisChangesResult(t *testing.T) {
	l := leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, false)
	l.Last, l.HasLast = d("3200"), true
	l.Settlement, l.HasSettlement = d("3180"), true

	got := map[PriceBasis]decimal.Decimal{}
	for _, b := range []PriceBasis{OpenTodayPreSettleHistory, PreSettleAll, LastAll, SettlementAll} {
		r, err := Compute([]Leg{l}, b, NoNetting)
		if err != nil {
			t.Fatalf("%v 失败: %v", b, err)
		}
		got[b] = r.Company
	}
	// 今仓：候选 1 用开仓价 3150 → 3150；候选 2 用昨结算 3160 → 3160
	if !got[OpenTodayPreSettleHistory].Equal(d("3150")) {
		t.Errorf("今仓用开仓价应为 3150，实为 %s", got[OpenTodayPreSettleHistory])
	}
	if !got[PreSettleAll].Equal(d("3160")) {
		t.Errorf("用昨结算价应为 3160，实为 %s", got[PreSettleAll])
	}
	if !got[LastAll].Equal(d("3200")) {
		t.Errorf("用最新价应为 3200，实为 %s", got[LastAll])
	}
	// ⚠️ 四个候选必须两两不等 —— 否则这个样本对实验 1 没有判别力。
	seen := map[string]PriceBasis{}
	for b, v := range got {
		if prev, dup := seen[v.String()]; dup {
			t.Errorf("⚠️ %v 与 %v 在本样本上同值（%s）—— 该样本对实验 1 无判别力", b, prev, v)
		}
		seen[v.String()] = b
	}
}

// TestTodayHistoryDifferOnlyUnderCandidate1 钉住候选 1 与候选 2 的判别特征。
//
// ⚠️ **开仓价必须 ≠ 昨结算价**，否则两个候选给出同一个数。
// 21:00 那轮实验的样本守卫就是这一条。
func TestTodayHistoryDifferOnlyUnderCandidate1(t *testing.T) {
	today := leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, false)
	hist := leg(t, "rb2701", types.Buy, 1, "3150", "3160", true, false)

	for _, c := range []struct {
		basis      PriceBasis
		wantDiffer bool
		why        string
	}{
		{OpenTodayPreSettleHistory, true, "候选 1：今仓用开仓价、昨仓用昨结算价 → 每手不同"},
		{PreSettleAll, false, "候选 2：今昨都用昨结算价 → 每手相同"},
	} {
		a, err := Compute([]Leg{today}, c.basis, NoNetting)
		if err != nil {
			t.Fatal(err)
		}
		b, err := Compute([]Leg{hist}, c.basis, NoNetting)
		if err != nil {
			t.Fatal(err)
		}
		differ := !a.Company.Equal(b.Company)
		if differ != c.wantDiffer {
			t.Errorf("%v：今昨每手是否不同 = %v，期望 %v（%s）", c.basis, differ, c.wantDiffer, c.why)
		}
	}

	// ⚠️ 开仓价 == 昨结算价时，两个候选**必然同值** —— 把这个无判别力的样本钉住。
	same := leg(t, "rb2701", types.Buy, 1, "3160", "3160", false, false)
	x, _ := Compute([]Leg{same}, OpenTodayPreSettleHistory, NoNetting)
	y, _ := Compute([]Leg{same}, PreSettleAll, NoNetting)
	if !x.Company.Equal(y.Company) {
		t.Fatalf("前提变了：开仓价等于昨结算价时两候选本应同值，实为 %s vs %s", x.Company, y.Company)
	}
}

// TestScopeNeedsSameProductDifferentMonths 是实验 3 的**样本判据**。
//
// ⚠️ 单合约双向持仓时，「按品种合并」与「按合约合并」给出同一个数。
// 要分开必须是同品种、两个不同月份、方向相反。
func TestScopeNeedsSameProductDifferentMonths(t *testing.T) {
	singleContract := []Leg{
		leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, true),
		leg(t, "rb2701", types.Sell, 1, "3150", "3160", false, true),
	}
	spread := []Leg{
		leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, true),
		leg(t, "rb2610", types.Sell, 1, "3100", "3110", false, true),
	}

	if CanDiscriminateScope(singleContract) {
		t.Error("⚠️ 单合约样本被判为有判别力 —— 判据错了")
	}
	if !CanDiscriminateScope(spread) {
		t.Error("⚠️ 同品种两月份多空对锁被判为无判别力 —— 判据错了")
	}

	// 单合约上：两种合并范围**必然同值**。
	a, _ := Compute(singleContract, PreSettleAll, ByProduct)
	b, _ := Compute(singleContract, PreSettleAll, ByInstrument)
	if !a.Company.Equal(b.Company) {
		t.Fatalf("前提变了：单合约上两种范围本应同值，实为 %s vs %s", a.Company, b.Company)
	}

	// 跨月份上：必须分开。
	c, _ := Compute(spread, PreSettleAll, ByProduct)
	e, _ := Compute(spread, PreSettleAll, ByInstrument)
	if c.Company.Equal(e.Company) {
		t.Error("⚠️ 跨月份上两种范围仍同值 —— 那说明合并键没起作用")
	}
	// 按品种：max(3160, 3110) = 3160；按合约：3160 + 3110 = 6270
	if !c.Company.Equal(d("3160")) {
		t.Errorf("按品种合并应为 3160，实为 %s", c.Company)
	}
	if !e.Company.Equal(d("6270")) {
		t.Errorf("按合约合并应为 6270，实为 %s", e.Company)
	}
}

// TestGroupDiscriminatingFlag 断言「一边为零的组对大边没有判别力」被标出来。
func TestGroupDiscriminatingFlag(t *testing.T) {
	oneSide := []Leg{leg(t, "rb2701", types.Buy, 2, "3150", "3160", false, true)}
	r, err := Compute(oneSide, PreSettleAll, ByProduct)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Groups) != 1 {
		t.Fatalf("应有 1 个合并组，实为 %d", len(r.Groups))
	}
	if r.Groups[0].Discriminating() {
		t.Error("⚠️ 单边持仓的组被判为有判别力 —— max(多,0) 与 多+0 同值")
	}
	// 而双边的组有判别力。
	both := append(oneSide, leg(t, "rb2701", types.Sell, 1, "3150", "3160", false, true))
	r2, _ := Compute(both, PreSettleAll, ByProduct)
	if !r2.Groups[0].Discriminating() {
		t.Error("双边持仓的组应有判别力")
	}
}

// TestMaxMarginSideFlagIsPerContract 断言大边开关来自规则数据，不按交易所硬编码。
func TestMaxMarginSideFlagIsPerContract(t *testing.T) {
	on := []Leg{
		leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, true),
		leg(t, "rb2610", types.Sell, 1, "3100", "3110", false, true),
	}
	off := []Leg{
		leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, false),
		leg(t, "rb2610", types.Sell, 1, "3100", "3110", false, false),
	}
	a, _ := Compute(on, PreSettleAll, ByProduct)
	b, _ := Compute(off, PreSettleAll, ByProduct)
	if a.Company.Equal(b.Company) {
		t.Error("⚠️ 开关不起作用：启用与不启用大边给出同一个数")
	}
	if !b.Company.Equal(d("6270")) {
		t.Errorf("不启用大边应为两边相加 6270，实为 %s", b.Company)
	}

	// ⚠️ 同组内开关不一致 → 拒绝猜。
	mixed := []Leg{
		leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, true),
		leg(t, "rb2610", types.Sell, 1, "3100", "3110", false, false),
	}
	if _, err := Compute(mixed, PreSettleAll, ByProduct); err == nil {
		t.Error("⚠️ 同一合并组内大边开关不一致，本该拒绝而不是挑一个")
	}
}

// TestCompanyAddOnRaisesOnlyCompanySide 断言加收只影响公司口径。
func TestCompanyAddOnRaisesOnlyCompanySide(t *testing.T) {
	l := leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, false)
	base, _ := Compute([]Leg{l}, PreSettleAll, NoNetting)
	if !base.Company.Equal(base.Exchange) {
		t.Errorf("⚠️ 加收为零时两个口径应相等，实为 %s vs %s", base.Company, base.Exchange)
	}

	l.Rates.CompanyAddOn = d("0.02")
	withAddOn, _ := Compute([]Leg{l}, PreSettleAll, NoNetting)
	if !withAddOn.Exchange.Equal(base.Exchange) {
		t.Errorf("⚠️ 加收不该改变交易所口径：%s → %s", base.Exchange, withAddOn.Exchange)
	}
	// 12% 而不是 10%：3160 × 1.2 = 3792
	if !withAddOn.Company.Equal(d("3792")) {
		t.Errorf("加收百分之二后公司口径应为 3792，实为 %s", withAddOn.Company)
	}
	if withAddOn.Company.LessThan(withAddOn.Exchange) {
		t.Error("⚠️ 公司口径低于交易所口径 —— 加收为负不成立")
	}
}

// TestLongShortRatesCanDiffer 断言多空保证金率可以不等。
func TestLongShortRatesCanDiffer(t *testing.T) {
	asym := Rates{LongByMoney: d("0.10"), ShortByMoney: d("0.15")}
	long := leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, false)
	long.Rates = asym
	short := leg(t, "rb2701", types.Sell, 1, "3150", "3160", false, false)
	short.Rates = asym

	a, _ := Compute([]Leg{long}, PreSettleAll, NoNetting)
	b, _ := Compute([]Leg{short}, PreSettleAll, NoNetting)
	if a.Company.Equal(b.Company) {
		t.Error("⚠️ 多空率不同却算出同一个数 —— 方向没起作用")
	}
	if !b.Company.Equal(d("4740")) {
		t.Errorf("空头百分之十五应为 4740，实为 %s", b.Company)
	}
}

// TestBothTermsAlwaysSummed 断言按金额与按手数两项都算。
func TestBothTermsAlwaysSummed(t *testing.T) {
	l := leg(t, "rb2701", types.Buy, 2, "3150", "3160", false, false)
	l.Rates = Rates{LongByMoney: d("0.10"), LongByVolume: d("50")}
	r, err := Compute([]Leg{l}, PreSettleAll, NoNetting)
	if err != nil {
		t.Fatal(err)
	}
	// 2×3160×10×0.10 = 6320 ；2×50 = 100 ；合计 6420
	if !r.Company.Equal(d("6420")) {
		t.Errorf("两项相加应为 6420，实为 %s —— 只算一项会得到 6320 或 100", r.Company)
	}
}

// TestUnmeasuredRefuses 断言两个未实测项都**拒绝运行**。
func TestUnmeasuredRefuses(t *testing.T) {
	l := leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, false)
	cases := []struct {
		name  string
		basis PriceBasis
		scope SideScope
		want  string
	}{
		{"计价基准未实测", PriceBasisUnmeasured, NoNetting, "实验 1"},
		{"合并范围未实测", PreSettleAll, SideScopeUnmeasured, "实验 3"},
	}
	if len(cases) != 2 {
		t.Fatalf("用例数应为 2，实际 %d", len(cases))
	}
	for _, c := range cases {
		_, err := Compute([]Leg{l}, c.basis, c.scope)
		if err == nil {
			t.Errorf("⚠️ %s 时本该报错而不是挑一个默认", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s 的报错应指向%s，实为 %v", c.name, c.want, err)
		}
	}
}

// TestMissingPriceIsNotZero 断言缺价时报错而不是拿 0 顶替。
func TestMissingPriceIsNotZero(t *testing.T) {
	noPre := leg(t, "rb2701", types.Buy, 1, "3150", "", false, false)
	cases := []struct {
		name  string
		leg   Leg
		basis PriceBasis
	}{
		{"要昨结算价但没有", noPre, PreSettleAll},
		{"昨仓要昨结算价但没有", func() Leg { l := noPre; l.IsHistory = true; return l }(), OpenTodayPreSettleHistory},
		{"要最新价但没有", noPre, LastAll},
		{"要今结算价但没有", noPre, SettlementAll},
	}
	if len(cases) != 4 {
		t.Fatalf("反例数应为 4，实际 %d", len(cases))
	}
	for _, c := range cases {
		if _, err := Compute([]Leg{c.leg}, c.basis, NoNetting); err == nil {
			t.Errorf("⚠️ %s，本该报错而不是拿 0 顶替", c.name)
		}
	}
}

// TestRejects 覆盖其余非法输入。
func TestRejects(t *testing.T) {
	base := leg(t, "rb2701", types.Buy, 1, "3150", "3160", false, false)
	mut := func(f func(*Leg)) []Leg { l := base; f(&l); return []Leg{l} }
	cases := []struct {
		name string
		legs []Leg
	}{
		{"手数为零", mut(func(l *Leg) { l.Volume = 0 })},
		{"乘数为零", mut(func(l *Leg) { l.Multiplier = decimal.Zero })},
		{"买卖方向未指定", mut(func(l *Leg) { l.Direction = types.DirectionUnknown })},
		{"保证金率为负", mut(func(l *Leg) { l.Rates.LongByMoney = d("-0.1") })},
		{"加收为负", mut(func(l *Leg) { l.Rates.CompanyAddOn = d("-0.01") })},
	}
	if len(cases) != 5 {
		t.Fatalf("反例数应为 5，实际 %d", len(cases))
	}
	for _, c := range cases {
		if _, err := Compute(c.legs, PreSettleAll, NoNetting); err == nil {
			t.Errorf("%s 本该报错", c.name)
		}
	}
}

// TestEmptyIsZero 断言空持仓返回零而不是报错。
func TestEmptyIsZero(t *testing.T) {
	r, err := Compute(nil, PreSettleAll, ByProduct)
	if err != nil {
		t.Fatalf("空持仓不该报错: %v", err)
	}
	if !r.Company.IsZero() || !r.Exchange.IsZero() || len(r.Groups) != 0 {
		t.Errorf("空持仓应返回零，实为 %+v", r)
	}
}
