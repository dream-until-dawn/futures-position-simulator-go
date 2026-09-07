package fee

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// 一份「平今远贵于平昨」的费率：按金额万分之一，平今万分之六。
// ⚠️ 这是**测试夹具**，不是任何真实品种的费率 —— 真值来自柜台。
var rbLike = Rates{
	OpenByMoney:       d("0.0001"),
	CloseByMoney:      d("0.0001"),
	CloseTodayByMoney: d("0.0006"),
}

// 一份「按手数」的费率：开平各 3 元/手，平今免。
var byLotLike = Rates{
	OpenByVolume:       d("3"),
	CloseByVolume:      d("3"),
	CloseTodayByVolume: decimal.Zero,
}

const (
	px   = "3150"
	mult = "10"
)

// TestThreeTiersAreDistinct 断言三档费率**各走各的**。
//
// ⚠️ 把平今当平昨算，日内策略的回测收益会系统性偏高，
// 且偏高幅度正比于交易频率 —— 频率越高被高估得越多。
func TestThreeTiersAreDistinct(t *testing.T) {
	cases := []struct {
		offset types.Offset
		want   string
	}{
		{types.Open, "3.15"},           // 1×3150×10×0.0001
		{types.Close, "3.15"},          // 平昨同价
		{types.CloseYesterday, "3.15"}, // 显式平昨，与 Close 同档
		{types.CloseToday, "18.9"},     // 1×3150×10×0.0006
	}
	if len(cases) != 4 {
		t.Fatalf("用例数应为 4，实际 %d", len(cases))
	}
	for _, c := range cases {
		got, err := Compute(rbLike, c.offset, d(px), d(mult), 1, decimalx.HalfUpToCent)
		if err != nil {
			t.Errorf("%v 计算失败: %v", c.offset, err)
			continue
		}
		if !got.Equal(d(c.want)) {
			t.Errorf("%v 的手续费 = %s，期望 %s", c.offset, got, c.want)
		}
	}
	// ⚠️ 平今必须与平昨**不等** —— 否则这份夹具没有判别力。
	open, _ := Compute(rbLike, types.Open, d(px), d(mult), 1, decimalx.HalfUpToCent)
	today, _ := Compute(rbLike, types.CloseToday, d(px), d(mult), 1, decimalx.HalfUpToCent)
	if open.Equal(today) {
		t.Error("⚠️ 开仓与平今同价 —— 这份夹具分不开三档，测试没有判别力")
	}
}

// TestBothTermsAlwaysSummed 断言按金额与按手数**两项都算**。
//
// ⚠️ 同一品种通常只用其中一种形态，另一种为 0 ——
// 而一个恒为 0 的加项不会暴露自己被漏掉了。所以要用**两项都非零**的费率来测。
func TestBothTermsAlwaysSummed(t *testing.T) {
	both := Rates{OpenByMoney: d("0.0001"), OpenByVolume: d("5")}
	got, err := Compute(both, types.Open, d(px), d(mult), 2, decimalx.HalfUpToCent)
	if err != nil {
		t.Fatal(err)
	}
	// 2×3150×10×0.0001 = 6.3 ；2×5 = 10 ；合计 16.3
	if !got.Equal(d("16.3")) {
		t.Errorf("两项相加应为 16.3，实为 %s —— 只算一项会得到 6.3 或 10", got)
	}

	// 单项为零的夹具**验不出漏项** —— 把这件事钉住。
	onlyMoney := Rates{OpenByMoney: d("0.0001")}
	a, _ := Compute(onlyMoney, types.Open, d(px), d(mult), 2, decimalx.HalfUpToCent)
	if !a.Equal(d("6.3")) {
		t.Fatalf("前提变了：只有按金额时应为 6.3，实为 %s", a)
	}
	t.Log("⚠️ 用单项费率的夹具去验「两项都算」，会验不出任何东西")
}

// TestVolumeAndMultiplierAreLinear 是漏乘的属性测试。
func TestVolumeAndMultiplierAreLinear(t *testing.T) {
	base, err := Compute(rbLike, types.Open, d(px), d(mult), 1, decimalx.NoRounding)
	if err != nil {
		t.Fatal(err)
	}
	if base.IsZero() {
		t.Fatal("⚠️ 基准为零 —— 线性性质在零上恒成立，没有判别力")
	}
	dbl, _ := Compute(rbLike, types.Open, d(px), d(mult), 2, decimalx.NoRounding)
	if !dbl.Equal(base.Mul(d("2"))) {
		t.Errorf("手数翻倍时手续费应翻倍：%s → %s", base, dbl)
	}
	dblMult, _ := Compute(rbLike, types.Open, d(px), d("20"), 1, decimalx.NoRounding)
	if !dblMult.Equal(base.Mul(d("2"))) {
		t.Errorf("乘数翻倍时手续费应翻倍：%s → %s", base, dblMult)
	}
	// 按手数那一档**不随乘数变**，这是它与按金额档最容易分开的地方。
	lotBase, _ := Compute(byLotLike, types.Open, d(px), d(mult), 1, decimalx.NoRounding)
	lotDbl, _ := Compute(byLotLike, types.Open, d(px), d("20"), 1, decimalx.NoRounding)
	if !lotBase.Equal(lotDbl) {
		t.Errorf("⚠️ 按手数的费率不该随乘数变：%s vs %s", lotBase, lotDbl)
	}
}

// TestForceCloseRefusesToGuess 断言强平标志**拒绝猜**费率档。
//
// ⚠️ 强平按平今还是平昨计费，取决于它平掉的是哪一边，
// 而那个信息不在开平标志里。猜错的方向不会有任何提示。
func TestForceCloseRefusesToGuess(t *testing.T) {
	forced := []types.Offset{types.ForceClose, types.ForceOff, types.LocalForceClose}
	if len(forced) != 3 {
		t.Fatalf("用例数应为 3，实际 %d", len(forced))
	}
	for _, o := range forced {
		_, err := Compute(rbLike, o, d(px), d(mult), 1, decimalx.HalfUpToCent)
		if err == nil {
			t.Errorf("⚠️ %v 本该拒绝而不是挑一档费率", o)
			continue
		}
		if !strings.Contains(err.Error(), "不在开平标志里") {
			t.Errorf("%v 的报错应说明信息不足，实为 %v", o, err)
		}
	}
}

// TestRoundingUnmeasuredRefuses 断言取整口径未实测时**拒绝运行**。
func TestRoundingUnmeasuredRefuses(t *testing.T) {
	_, err := Compute(rbLike, types.Open, d(px), d(mult), 1, decimalx.RoundingUnmeasured)
	if err == nil {
		t.Fatal("⚠️ 取整口径未实测时本该报错")
	}
	if !strings.Contains(err.Error(), "实验 5") {
		t.Errorf("报错应指向实验 5，实为 %v", err)
	}
}

// TestRoundingChangesResult 断言三个取整候选在**同一笔成交**上给出不同的数。
//
// 这正是实验 5 需要被实测的原因：单笔差几厘，逐日累加进结存会漂出可见的量。
func TestRoundingChangesResult(t *testing.T) {
	// 构造一笔手续费理论值落在半分附近的成交。
	//
	// ⚠️ 第一版用的是 3150.5（理论值 3.1505），而 3.1505 的第三位小数是 0，
	// 四舍五入与截断【同值】—— 这条测试自己的判别力守卫当场报了它。
	// 要分开两者，第三位小数必须 ≥ 5。
	rates := Rates{OpenByMoney: d("0.0001")}
	// 1 × 3155.6 × 10 × 0.0001 = 3.1556 → 四舍五入 3.16 ／ 截断 3.15
	got := map[decimalx.Rounding]decimal.Decimal{}
	for _, r := range []decimalx.Rounding{decimalx.HalfUpToCent, decimalx.TruncateToCent, decimalx.NoRounding} {
		v, err := Compute(rates, types.Open, d("3155.6"), d(mult), 1, r)
		if err != nil {
			t.Fatalf("%v 失败: %v", r, err)
		}
		got[r] = v
	}
	if got[decimalx.HalfUpToCent].Equal(got[decimalx.TruncateToCent]) {
		t.Errorf("⚠️ 四舍五入与截断同值（%s）—— 这笔成交没有判别力，"+
			"换一笔理论值落在半分附近的", got[decimalx.HalfUpToCent])
	}
	if !got[decimalx.NoRounding].Equal(d("3.1556")) {
		t.Errorf("未取整应为 3.1556，实为 %s", got[decimalx.NoRounding])
	}
	if !got[decimalx.HalfUpToCent].Equal(d("3.16")) {
		t.Errorf("四舍五入到分应为 3.16，实为 %s", got[decimalx.HalfUpToCent])
	}
	if !got[decimalx.TruncateToCent].Equal(d("3.15")) {
		t.Errorf("截断到分应为 3.15，实为 %s", got[decimalx.TruncateToCent])
	}
}

// TestComputeRawIsUnrounded 断言给实验 5 用的理论值是未取整的。
//
// ⚠️ 先取整再与柜台值比较，等于把待验证的口径当成了已知。
func TestComputeRawIsUnrounded(t *testing.T) {
	rates := Rates{OpenByMoney: d("0.0001")}
	got, err := ComputeRaw(rates, types.Open, d("3150.5"), d(mult), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(d("3.1505")) {
		t.Errorf("⚠️ ComputeRaw 应保留完整精度，期望 3.1505，实为 %s", got)
	}
}

// TestCloseTodayPremium 覆盖平今溢价的量化。
func TestCloseTodayPremium(t *testing.T) {
	r, ok := rbLike.CloseTodayPremium(d(px), d(mult))
	if !ok {
		t.Fatal("平昨费率非零时应能作比")
	}
	if !r.Equal(d("6")) {
		t.Errorf("平今是平昨的 6 倍，实为 %s", r)
	}
	// 平昨免费时无法作比 —— 必须报「没有」而不是返回 0 或无穷。
	free := Rates{CloseTodayByMoney: d("0.0006")}
	if _, ok := free.CloseTodayPremium(d(px), d(mult)); ok {
		t.Error("⚠️ 平昨费率为零时本该报「无法作比」，而不是给一个数")
	}
}

// TestRejects 覆盖非法输入。
func TestRejects(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"负费率", mustErr(Compute(Rates{OpenByMoney: d("-0.0001")}, types.Open, d(px), d(mult), 1, decimalx.NoRounding))},
		{"手数为零", mustErr(Compute(rbLike, types.Open, d(px), d(mult), 0, decimalx.NoRounding))},
		{"手数为负", mustErr(Compute(rbLike, types.Open, d(px), d(mult), -1, decimalx.NoRounding))},
		{"成交价为零", mustErr(Compute(rbLike, types.Open, decimal.Zero, d(mult), 1, decimalx.NoRounding))},
		{"乘数为零", mustErr(Compute(rbLike, types.Open, d(px), decimal.Zero, 1, decimalx.NoRounding))},
		{"开平标志未指定", mustErr(Compute(rbLike, types.OffsetUnknown, d(px), d(mult), 1, decimalx.NoRounding))},
	}
	if len(cases) != 6 {
		t.Fatalf("反例数应为 6，实际 %d", len(cases))
	}
	for _, c := range cases {
		if c.err == nil {
			t.Errorf("%s 本该报错", c.name)
		}
	}
}

func mustErr(_ decimal.Decimal, err error) error { return err }

// TestZeroRateIsAllowed 断言零费率**合法**（免平今是真实存在的）。
func TestZeroRateIsAllowed(t *testing.T) {
	got, err := Compute(byLotLike, types.CloseToday, d(px), d(mult), 3, decimalx.HalfUpToCent)
	if err != nil {
		t.Fatalf("免平今是真实存在的，不该报错: %v", err)
	}
	if !got.IsZero() {
		t.Errorf("免平今的手续费应为 0，实为 %s", got)
	}
	// ⚠️ 但它必须与「费率缺失」分得开 —— 本包的做法是：Rates 是值类型，
	// 缺失由调用方（refdata）负责，本包只对拿到的费率算钱。
}
