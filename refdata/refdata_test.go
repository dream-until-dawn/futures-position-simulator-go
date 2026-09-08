package refdata

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

const asOf = types.TradingDay(20260907)

func id(t *testing.T, ex types.Exchange, code string) types.InstrumentID {
	t.Helper()
	v, err := types.ParseNative(ex, code, asOf)
	if err != nil {
		t.Fatalf("解析 %s.%s 失败: %v", ex, code, err)
	}
	return v
}

func spec(t *testing.T, ex types.Exchange, code string) Instrument {
	t.Helper()
	return Instrument{
		ID:                  id(t, ex, code),
		VolumeMultiple:      d("10"),
		PriceTick:           d("1"),
		PositionDateType:    UseHistory,
		MaxMarginSide:       true,
		ExpireDate:          20270115,
		IsTrading:           true,
		MinLimitOrderVolume: 1,
		MaxLimitOrderVolume: 500,
		PriceLimitRatio:     d("0.05"),
		HasPriceLimitRatio:  true,
	}
}

// TestKeyIsCanonicalNotNative 是本包最要紧的一条：**键必须是规范形式**。
//
// ⚠️ 郑商所三位年月十年一轮回。按线格式做键，一份跨十年的快照里
// TA701(2027) 与 TA701(2037) 会撞——而十年以内的样本上两种做法给出同一个数。
func TestKeyIsCanonicalNotNative(t *testing.T) {
	a := id(t, types.CZCE, "TA701") // 锚 2026 → 2027-01
	b, err := types.ParseNative(types.CZCE, "TA701", 20360907)
	if err != nil {
		t.Fatal(err)
	}
	if a.Native() != b.Native() {
		t.Fatalf("前提不成立：两者线格式应相同，实为 %q / %q", a.Native(), b.Native())
	}
	if key(a) == key(b) {
		t.Fatalf("⚠️ 规范形式也撞了（%s）—— 那样规则数据就分不开这两个合约", key(a))
	}

	sa, sb := spec(t, types.CZCE, "TA701"), spec(t, types.CZCE, "TA701")
	sb.ID = b
	sa.VolumeMultiple, sb.VolumeMultiple = d("5"), d("20") // 刻意不同，撞键时会显形

	snap, err := NewBuilder(1).AddInstrument(sa).AddInstrument(sb).Build()
	if err != nil {
		t.Fatalf("两个不同年代的合约本该都能加入: %v", err)
	}
	if snap.Count() != 2 {
		t.Fatalf("⚠️ 应有 2 个合约，实为 %d —— 撞键让后一份悄悄覆盖了前一份", snap.Count())
	}
	got, _ := snap.Instrument(a)
	if !got.VolumeMultiple.Equal(d("5")) {
		t.Errorf("查 2027 的合约取到了 2037 的规格：乘数 %s", got.VolumeMultiple)
	}
}

// TestDuplicateIsRejected 断言同一合约重复加入**报错**而不是静默覆盖。
func TestDuplicateIsRejected(t *testing.T) {
	s := spec(t, types.SHFE, "rb2701")
	_, err := NewBuilder(1).AddInstrument(s).AddInstrument(s).Build()
	if err == nil {
		t.Fatal("⚠️ 重复加入本该报错 —— 静默覆盖会让后一份悄悄赢，而两份规格不同时没有提示")
	}
	if !strings.Contains(err.Error(), "重复") {
		t.Errorf("报错应说明重复，实为 %v", err)
	}
}

// TestMissingReturnsErrorNotZero 断言查不到时**报错**而不是返回零值规格。
//
// ⚠️ 一个乘数为 0 的合约规格会让所有金额变成 0，而 0 看起来完全合理。
func TestMissingReturnsErrorNotZero(t *testing.T) {
	snap, err := NewBuilder(1).AddInstrument(spec(t, types.SHFE, "rb2701")).Build()
	if err != nil {
		t.Fatal(err)
	}
	missing := id(t, types.SHFE, "rb2610")
	if _, err := snap.Instrument(missing); err == nil {
		t.Error("⚠️ 查不到的合约本该报错，而不是返回零值规格")
	}
	if _, err := snap.MarginRates(missing, types.Speculation); err == nil {
		t.Error("查不到的保证金率本该报错")
	}
	if _, err := snap.CommissionRates(missing, types.Speculation); err == nil {
		t.Error("查不到的手续费率本该报错")
	}
	// 存在的合约但缺该投机套保档，同样报错 —— 不能退回别的档。
	full, err := NewBuilder(1).
		AddInstrument(spec(t, types.SHFE, "rb2701")).
		AddMarginRates(id(t, types.SHFE, "rb2701"), types.Speculation, MarginRates{LongByMoney: d("0.1")}).
		Build()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := full.MarginRates(id(t, types.SHFE, "rb2701"), types.Hedge); err == nil {
		t.Error("⚠️ 缺套保档时本该报错 —— 投机套保标志决定保证金率，不能退回投机档")
	}
}

// TestOrphanRatesRejected 断言费率挂在不存在的合约上会**报错**。
//
// ⚠️ 静默忽略会让那份费率永远查不到，而查不到时的报错指向合约缺失，
// 把排查引到别处去。
func TestOrphanRatesRejected(t *testing.T) {
	_, err := NewBuilder(1).
		AddInstrument(spec(t, types.SHFE, "rb2701")).
		AddMarginRates(id(t, types.SHFE, "rb2610"), types.Speculation, MarginRates{LongByMoney: d("0.1")}).
		Build()
	if err == nil {
		t.Fatal("⚠️ 费率挂在不存在的合约上，本该报错")
	}
	if !strings.Contains(err.Error(), "不存在的合约") {
		t.Errorf("报错应说明合约不存在，实为 %v", err)
	}
}

// TestInstrumentValidate 覆盖规格校验。
func TestInstrumentValidate(t *testing.T) {
	base := spec(t, types.SHFE, "rb2701")
	mut := func(f func(*Instrument)) Instrument { s := base; f(&s); return s }
	bad := []struct {
		name string
		inst Instrument
		why  string
	}{
		{"乘数为零", mut(func(i *Instrument) { i.VolumeMultiple = decimal.Zero }), "会让所有金额变成 0，而 0 看起来完全合理"},
		{"乘数为负", mut(func(i *Instrument) { i.VolumeMultiple = d("-10") }), ""},
		{"最小变动价位为零", mut(func(i *Instrument) { i.PriceTick = decimal.Zero }), ""},
		{"PositionDateType 未知", mut(func(i *Instrument) { i.PositionDateType = PositionDateUnknown }), "它决定报单要不要显式声明平今平昨"},
		{"手数下限大于上限", mut(func(i *Instrument) { i.MinLimitOrderVolume, i.MaxLimitOrderVolume = 10, 5 }), ""},
		{"涨跌幅比例为负", mut(func(i *Instrument) { i.PriceLimitRatio = d("-0.05") }), ""},
	}
	if len(bad) != 6 {
		t.Fatalf("反例数应为 6，实际 %d", len(bad))
	}
	for _, c := range bad {
		if err := c.inst.Validate(); err == nil {
			t.Errorf("%s 本该报错（%s）", c.name, c.why)
		}
	}
	if err := base.Validate(); err != nil {
		t.Errorf("合法规格不该报错: %v", err)
	}
}

// TestPriceLimitsDistinguishAbsentFromZero 是「零值不是安全的默认」在涨跌停上的落点。
//
// ⚠️ 推不出来必须报「没有」，调用方据此**跳过校验并给出原因**，
// 而不是读成「没有涨跌停限制」。
func TestPriceLimitsDistinguishAbsentFromZero(t *testing.T) {
	inst := spec(t, types.SHFE, "rb2701") // 涨跌幅 5%
	// ⚠️ 取整方向要显式给 —— 两家交易所不同（probes.md §12）。
	// 这里用 TickNone 是为了保住这条测试原来的意思：验的是「有没有值」，不是取整。
	up, lo, ok := inst.PriceLimits(d("3160"), true, TickNone)
	if !ok {
		t.Fatal("有比例有昨结算价时应能推出")
	}
	if !up.Equal(d("3318")) || !lo.Equal(d("3002")) {
		t.Errorf("涨跌停应为 3318 / 3002，实为 %s / %s", up, lo)
	}

	noRatio := inst
	noRatio.HasPriceLimitRatio = false
	cases := []struct {
		name string
		inst Instrument
		pre  decimal.Decimal
		has  bool
	}{
		{"没有涨跌幅比例", noRatio, d("3160"), true},
		{"没有昨结算价", inst, decimal.Zero, false},
		{"昨结算价为零（缺失的伪装）", inst, decimal.Zero, true},
	}
	if len(cases) != 3 {
		t.Fatalf("反例数应为 3，实际 %d", len(cases))
	}
	for _, c := range cases {
		if _, _, ok := c.inst.PriceLimits(c.pre, c.has, TickNone); ok {
			t.Errorf("⚠️ %s，本该报「推不出来」", c.name)
		}
	}

	// ⚠️ 比例为零是**合法**的（意味着不许波动），必须与「没有比例」分开。
	zeroRatio := inst
	zeroRatio.PriceLimitRatio = decimal.Zero
	u2, l2, ok2 := zeroRatio.PriceLimits(d("3160"), true, TickNone)
	if !ok2 {
		t.Fatal("⚠️ 比例为零是合法的，不该报「推不出来」")
	}
	if !u2.Equal(d("3160")) || !l2.Equal(d("3160")) {
		t.Errorf("比例为零时涨跌停都应等于昨结算价，实为 %s / %s", u2, l2)
	}
}

// TestProductIndex 覆盖品种索引。
func TestProductIndex(t *testing.T) {
	b := NewBuilder(1)
	for _, code := range []string{"rb2701", "rb2610", "rb2605"} {
		b.AddInstrument(spec(t, types.SHFE, code))
	}
	b.AddInstrument(spec(t, types.DCE, "m2701"))
	snap, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}

	rb := snap.ProductInstruments(types.SHFE, "rb")
	if len(rb) != 3 {
		t.Fatalf("rb 应有 3 个合约，实为 %d", len(rb))
	}
	// 按到期年月排序。
	for i := 1; i < len(rb); i++ {
		if rb[i-1].YearMonth() >= rb[i].YearMonth() {
			t.Errorf("未按到期年月排序：%v", rb)
			break
		}
	}
	// ⚠️ 跨交易所不能混。
	if len(snap.ProductInstruments(types.DCE, "rb")) != 0 {
		t.Error("⚠️ DCE 下不该有 rb —— 品种键必须带交易所")
	}
	if len(snap.ProductInstruments(types.SHFE, "m")) != 0 {
		t.Error("⚠️ SHFE 下不该有 m")
	}
	// 返回的是副本，改它不该影响快照。
	rb[0] = types.InstrumentID{}
	if snap.ProductInstruments(types.SHFE, "rb")[0].Product == "" {
		t.Error("⚠️ 返回的不是副本 —— 调用方能改到快照内部")
	}
}

// TestSnapshotVersionIsFixed 断言快照的版本恒定。
//
// ⚠️ 回测必须用不可变快照：规则一旦在回测途中改变，结果就不再可复现，
// 而且不会有任何报错。
func TestSnapshotVersionIsFixed(t *testing.T) {
	snap, err := NewBuilder(1757000000000).AddInstrument(spec(t, types.SHFE, "rb2701")).Build()
	if err != nil {
		t.Fatal(err)
	}
	a := snap.Version()
	_ = snap.ProductInstruments(types.SHFE, "rb")
	_, _ = snap.Instrument(id(t, types.SHFE, "rb2701"))
	if snap.Version() != a || a != 1757000000000 {
		t.Errorf("版本应恒定为 1757000000000，实为 %d → %d", a, snap.Version())
	}
	if _, err := NewBuilder(0).AddInstrument(spec(t, types.SHFE, "rb2701")).Build(); err == nil {
		t.Error("⚠️ 版本戳为零本该报错 —— 它是判断规则是否变更的唯一依据")
	}
}

// TestBuildReportsAllProblemsAtOnce 断言构造错误一次性全报。
//
// ⚠️ 一次只报一个，会让人修一个跑一次，而中途放弃时剩下的问题**看起来不存在**。
func TestBuildReportsAllProblemsAtOnce(t *testing.T) {
	bad1 := spec(t, types.SHFE, "rb2701")
	bad1.VolumeMultiple = decimal.Zero
	bad2 := spec(t, types.SHFE, "rb2610")
	bad2.PriceTick = decimal.Zero
	_, err := NewBuilder(1).AddInstrument(bad1).AddInstrument(bad2).Build()
	if err == nil {
		t.Fatal("两处都不合法，本该报错")
	}
	if !strings.Contains(err.Error(), "2 处问题") {
		t.Errorf("⚠️ 应一次性报出 2 处问题，实为 %v", err)
	}
}

// TestNegativeRatesRejected 断言负费率在构造期就被拦下。
func TestNegativeRatesRejected(t *testing.T) {
	i := id(t, types.SHFE, "rb2701")
	cases := []struct {
		name string
		add  func(*Builder) *Builder
	}{
		{"保证金率为负", func(b *Builder) *Builder {
			return b.AddMarginRates(i, types.Speculation, MarginRates{LongByMoney: d("-0.1")})
		}},
		{"公司加收为负", func(b *Builder) *Builder {
			return b.AddMarginRates(i, types.Speculation, MarginRates{CompanyAddOn: d("-0.01")})
		}},
		{"手续费率为负", func(b *Builder) *Builder {
			return b.AddCommissionRates(i, types.Speculation, CommissionRates{OpenByMoney: d("-0.0001")})
		}},
		{"未指定投机套保档", func(b *Builder) *Builder {
			return b.AddMarginRates(i, types.HedgeUnknown, MarginRates{LongByMoney: d("0.1")})
		}},
	}
	if len(cases) != 4 {
		t.Fatalf("反例数应为 4，实际 %d", len(cases))
	}
	for _, c := range cases {
		b := NewBuilder(1).AddInstrument(spec(t, types.SHFE, "rb2701"))
		if _, err := c.add(b).Build(); err == nil {
			t.Errorf("%s 本该报错", c.name)
		}
	}
}

// TestSnapshotSatisfiesProvider 是编译期断言的运行期确认。
func TestSnapshotSatisfiesProvider(t *testing.T) {
	snap, err := NewBuilder(1).AddInstrument(spec(t, types.SHFE, "rb2701")).Build()
	if err != nil {
		t.Fatal(err)
	}
	var p Provider = snap
	if p.Version() != 1 {
		t.Errorf("通过接口取版本应为 1，实为 %d", p.Version())
	}
}

// TestPriceLimitsRounding 把**实测的**取整行为钉在测试里。
//
// ⚠️ 两家交易所的取整方向不同（probes.md §12，各两个品种）：
//
//	上期所  rb2701 昨结 3158、5%  理论 3315.9 / 3000.1  柜台给 3315 / 3000  向下
//	大商所  i2701  昨结 734.5、9%  理论 800.605 / 668.395 柜台给 800.5 / 668.5 四舍五入
//
// 而这两组数**同时**排除了「上下各取一边」这个很自然的猜测：
// 上期所两边都向下（3315.9→3315 是向下，3000.1→3000 也是向下），
// 大商所两边都四舍五入（800.605→800.5 是向下，668.395→668.5 是**向上**）。
// 若规则是「上取下、下取上」，大商所的上限应当是 800.5、下限 668.0 —— 与实测不符。
func TestPriceLimitsRounding(t *testing.T) {
	rb := spec(t, types.SHFE, "rb2701")
	rb.PriceLimitRatio, rb.HasPriceLimitRatio = d("0.05"), true
	rb.PriceTick = d("1")

	i := spec(t, types.DCE, "i2701")
	i.PriceLimitRatio, i.HasPriceLimitRatio = d("0.09"), true
	i.PriceTick = d("0.5")

	cases := []struct {
		name           string
		inst           Instrument
		pre            string
		rounding       TickRounding
		wantUp, wantLo string
	}{
		{"上期所 rb 向下取整（实测）", rb, "3158", TickFloor, "3315", "3000"},
		{"大商所 i 四舍五入（实测）", i, "734.5", TickHalfUp, "800.5", "668.5"},
		// ⚠️ 反例：拿另一家的取整方向去算，会得到与实测**不同**的数 ——
		// 那正是「默认挑一种会在另一家上静默错」的具体形状。
		{"⚠️ 用错方向：rb 四舍五入", rb, "3158", TickHalfUp, "3316", "3000"},
		{"⚠️ 用错方向：i 向下取整", i, "734.5", TickFloor, "800.5", "668"},
		{"不取整", rb, "3158", TickNone, "3315.9", "3000.1"},
	}
	if len(cases) != 5 {
		t.Fatalf("用例 %d 条，应为 5", len(cases))
	}
	for _, c := range cases {
		up, lo, ok := c.inst.PriceLimits(d(c.pre), true, c.rounding)
		if !ok {
			t.Errorf("%s：推不出来", c.name)
			continue
		}
		if !up.Equal(d(c.wantUp)) || !lo.Equal(d(c.wantLo)) {
			t.Errorf("%s：得到 %s / %s，应为 %s / %s",
				c.name, up, lo, c.wantUp, c.wantLo)
		}
	}
	// ⚠️ 用错方向确实会得出不同的数 —— 这一条把「默认挑一种是危险的」变成可见的。
	rightUp, _, _ := rb.PriceLimits(d("3158"), true, TickFloor)
	wrongUp, _, _ := rb.PriceLimits(d("3158"), true, TickHalfUp)
	if rightUp.Equal(wrongUp) {
		t.Error("⚠️ 两种取整给出同一个数 —— 那本条就没有判别力了，换个样本")
	}
	t.Logf("ⓘ 同一个合约：向下取整 %s，四舍五入 %s —— 差一个 tick，"+
		"而那个价**报不出去**", rightUp, wrongUp)

	// ⚠️ 零值必须报「推不出来」，而不是悄悄不取整。
	if _, _, ok := rb.PriceLimits(d("3158"), true, TickRoundingUnknown); ok {
		t.Error("⚠️ 没指定取整方向却推出了结果 —— " +
			"零值落进某一种会静默算错，而错的量级不到一个 tick：" +
			"数字看起来完全正常，只是那个价报不出去")
	}
	// ⚠️ 要取整却没有 tick，也是「推不出来」，不是「不用取整」。
	noTick := rb
	noTick.PriceTick = decimal.Zero
	if _, _, ok := noTick.PriceLimits(d("3158"), true, TickFloor); ok {
		t.Error("⚠️ 没有最小变动价位却取整成功了")
	}
}
