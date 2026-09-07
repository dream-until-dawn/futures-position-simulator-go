package types

import (
	"strings"
	"testing"
)

// TestParseNativeCZCEDecade 是郑商所三位年月定年代的判别测试。
//
// ⚠️ 每条用例都刻意落在「两个候选年份会给出不同答案」的地方——
// 若样本落在两者同值处，测试通过什么也不说明。
func TestParseNativeCZCEDecade(t *testing.T) {
	cases := []struct {
		instrument string
		asOf       TradingDay
		wantYear   int
		why        string
	}{
		{"TA701", 20260907, 2027, "近月合约：2027-01 距今 4 个月，2017/2037 都在百月开外"},
		{"TA609", 20260907, 2026, "当月合约"},
		{"TA608", 20260907, 2026, "⚠️ 刚过期一个月，仍应是 2026 而不是 2036"},
		{"AP610", 20260907, 2026, "距今 1 个月"},
		{"TA701", 20160907, 2017, "⚠️ 同一个代码，锚在 2016 时是 2017 —— 锚点决定年代"},
		{"TA701", 20360907, 2037, "锚在 2036 时是 2037"},
		{"CF505", 20241201, 2025, "跨年：2025-05 距今 5 个月"},
	}
	if len(cases) != 7 {
		t.Fatalf("用例数应为 7，实际 %d —— 增删了就同步更新下界", len(cases))
	}
	for _, c := range cases {
		got, err := ParseNative(CZCE, c.instrument, c.asOf)
		if err != nil {
			t.Errorf("ParseNative(CZCE, %q, %d) 报错: %v", c.instrument, c.asOf, err)
			continue
		}
		if got.Year != c.wantYear {
			t.Errorf("ParseNative(CZCE, %q, %d).Year = %d，期望 %d（%s）",
				c.instrument, c.asOf, got.Year, c.wantYear, c.why)
		}
	}
}

// TestParseNativeAnchorMatters 是「不能以现在为锚」这条的守卫。
//
// ⚠️ 同一个线格式代码，锚在不同交易日必须解出不同年份。
// 若某个实现改成用当前时间做锚，这条会红——而那种实现会让
// **同一段历史数据在不同年份跑出不同结果，且不报错**。
func TestParseNativeAnchorMatters(t *testing.T) {
	a, err1 := ParseNative(CZCE, "TA701", 20160907)
	b, err2 := ParseNative(CZCE, "TA701", 20260907)
	if err1 != nil || err2 != nil {
		t.Fatalf("解析失败: %v / %v", err1, err2)
	}
	if a.Year == b.Year {
		t.Fatalf("锚点没起作用：两次都解成 %d。"+
			"⚠️ 若实现改成以「现在」为锚，同一段历史数据会在不同年份跑出不同结果", a.Year)
	}
	if a.Year != 2017 || b.Year != 2027 {
		t.Errorf("锚 2016 应得 2017（实得 %d），锚 2026 应得 2027（实得 %d）", a.Year, b.Year)
	}
}

// TestNativeCanonicalRoundTrip 断言线格式与规范形式互转不丢信息。
func TestNativeCanonicalRoundTrip(t *testing.T) {
	cases := []struct {
		exchange      Exchange
		instrument    string
		asOf          TradingDay
		wantCanonical string
		wantNative    string
	}{
		{CZCE, "TA701", 20260907, "CZCE.TA2701", "CZCE.TA701"},
		{CZCE, "AP610", 20260907, "CZCE.AP2610", "CZCE.AP610"},
		{SHFE, "rb2701", 20260907, "SHFE.rb2701", "SHFE.rb2701"},
		{DCE, "m2701", 20260907, "DCE.m2701", "DCE.m2701"},
		{CFFEX, "IF2609", 20260907, "CFFEX.IF2609", "CFFEX.IF2609"},
		{GFEX, "si2612", 20260907, "GFEX.si2612", "GFEX.si2612"},
		{INE, "sc2611", 20260907, "INE.sc2611", "INE.sc2611"},
	}
	if len(cases) != 7 {
		t.Fatalf("用例数应为 7，实际 %d", len(cases))
	}
	for _, c := range cases {
		id, err := ParseNative(c.exchange, c.instrument, c.asOf)
		if err != nil {
			t.Errorf("%s.%s 解析失败: %v", c.exchange, c.instrument, err)
			continue
		}
		if got := id.Canonical(); got != c.wantCanonical {
			t.Errorf("%s.%s 规范形式 = %q，期望 %q", c.exchange, c.instrument, got, c.wantCanonical)
		}
		if got := id.Native(); got != c.wantNative {
			t.Errorf("%s.%s 线格式 = %q，期望 %q", c.exchange, c.instrument, got, c.wantNative)
		}
		// 线格式再解析一次，必须回到同一个规范形式。
		again, err := ParseSymbol(id.Native(), c.asOf)
		if err != nil {
			t.Errorf("%s 二次解析失败: %v", id.Native(), err)
			continue
		}
		if again != id {
			t.Errorf("%s 往返不一致：%+v vs %+v", c.instrument, again, id)
		}
	}
}

// TestCZCECollisionIsRealAcrossDecades 把「跨十年会撞」这件事钉成测试。
//
// ⚠️ 它证明的不是一个 bug，是一个**必须被设计绕开的事实**：
// 规则数据按线格式做键，在跨十年的快照里必然撞。
func TestCZCECollisionIsRealAcrossDecades(t *testing.T) {
	a, _ := ParseNative(CZCE, "TA701", 20260907) // 2027-01
	b, _ := ParseNative(CZCE, "TA701", 20360907) // 2037-01
	if a.Native() != b.Native() {
		t.Fatalf("前提不成立：两者线格式应相同，实为 %q / %q", a.Native(), b.Native())
	}
	if a.Canonical() == b.Canonical() {
		t.Fatalf("规范形式也撞了 —— 那样就没有任何东西能把它们分开：%q", a.Canonical())
	}
	if !a.SameProduct(b) {
		t.Errorf("同品种判定失败：%s 与 %s", a, b)
	}
}

// TestParseNativeRejects 断言坏输入**报错**，不是返回零值。
func TestParseNativeRejects(t *testing.T) {
	bad := []struct {
		exchange   Exchange
		instrument string
		why        string
	}{
		{"XXXX", "rb2701", "未知交易所"},
		{SHFE, "2701", "没有品种前缀"},
		{SHFE, "rb", "没有年月"},
		{SHFE, "rb27013", "五位年月"},
		{SHFE, "rb2713", "月份 13"},
		{CZCE, "TA700", "月份 0"},
		{SHFE, "rb27a1", "数字后面又有字母"},
	}
	if len(bad) != 7 {
		t.Fatalf("反例数应为 7，实际 %d", len(bad))
	}
	for _, c := range bad {
		if _, err := ParseNative(c.exchange, c.instrument, 20260907); err == nil {
			t.Errorf("%s.%s 本该报错（%s），却通过了", c.exchange, c.instrument, c.why)
		}
	}
}

// TestWireRoundTrip 是两套线格式映射的往返测试。
func TestWireRoundTrip(t *testing.T) {
	dirs := []Direction{Buy, Sell}
	offsetsCTP := []Offset{Open, Close, CloseToday, CloseYesterday, ForceClose, ForceOff, LocalForceClose}
	offsetsDIFF := []Offset{Open, Close, CloseToday}
	hedges := []HedgeFlag{Speculation, Arbitrage, Hedge}
	if len(dirs) != 2 || len(offsetsCTP) != 7 || len(offsetsDIFF) != 3 || len(hedges) != 3 {
		t.Fatalf("枚举取值数变了：dir=%d ctpOffset=%d diffOffset=%d hedge=%d —— "+
			"⚠️ 取值是比方法签名更硬的承诺，新增一个下游照样编译、只是有分支永远不进。"+
			"改了就同步更新这些下界并记进 roadmap",
			len(dirs), len(offsetsCTP), len(offsetsDIFF), len(hedges))
	}

	for _, d := range dirs {
		s, ok := d.CTP()
		if !ok {
			t.Errorf("%v 没有 CTP 取值", d)
		} else if back, err := DirectionFromCTP(s); err != nil || back != d {
			t.Errorf("CTP 往返失败 %v -> %q -> %v (%v)", d, s, back, err)
		}
		s, ok = d.DIFF()
		if !ok {
			t.Errorf("%v 没有 DIFF 取值", d)
		} else if back, err := DirectionFromDIFF(s); err != nil || back != d {
			t.Errorf("DIFF 往返失败 %v -> %q -> %v (%v)", d, s, back, err)
		}
	}
	for _, o := range offsetsCTP {
		s, ok := o.CTP()
		if !ok {
			t.Errorf("%v 没有 CTP 取值", o)
			continue
		}
		if back, err := OffsetFromCTP(s); err != nil || back != o {
			t.Errorf("CTP 往返失败 %v -> %q -> %v (%v)", o, s, back, err)
		}
	}
	for _, o := range offsetsDIFF {
		s, ok := o.DIFF()
		if !ok {
			t.Errorf("%v 没有 DIFF 取值", o)
			continue
		}
		if back, err := OffsetFromDIFF(s); err != nil || back != o {
			t.Errorf("DIFF 往返失败 %v -> %q -> %v (%v)", o, s, back, err)
		}
	}
	for _, h := range hedges {
		for _, pair := range []struct {
			get   func() (string, bool)
			parse func(string) (HedgeFlag, error)
			wire  string
		}{
			{h.CTP, HedgeFromCTP, "CTP"}, {h.DIFF, HedgeFromDIFF, "DIFF"},
		} {
			s, ok := pair.get()
			if !ok {
				t.Errorf("%v 没有 %s 取值", h, pair.wire)
				continue
			}
			if back, err := pair.parse(s); err != nil || back != h {
				t.Errorf("%s 往返失败 %v -> %q -> %v (%v)", pair.wire, h, s, back, err)
			}
		}
	}
}

// TestOffsetsMissingFromDIFFAreExplicit 断言 DIFF 侧缺失的取值**明确返回 false**。
//
// ⚠️ 这不是缺陷，是信息：DIFF 只认三种开平标志。调用方必须处理 false，
// 而不是拿零值串发出去 —— 那会变成一笔**开仓**。
func TestOffsetsMissingFromDIFFAreExplicit(t *testing.T) {
	missing := []Offset{CloseYesterday, ForceClose, ForceOff, LocalForceClose}
	if len(missing) != 4 {
		t.Fatalf("用例数应为 4，实际 %d", len(missing))
	}
	for _, o := range missing {
		s, ok := o.DIFF()
		if ok {
			t.Errorf("%v 在 DIFF 侧本不该有取值，却返回 %q", o, s)
		}
		if s != "" {
			t.Errorf("%v 缺失时应返回空串，实为 %q", o, s)
		}
	}
}

// TestWireParseRejectsUnknown 断言未知取值**报错**而不是返回零值。
func TestWireParseRejectsUnknown(t *testing.T) {
	if d, err := DirectionFromCTP("9"); err == nil {
		t.Errorf("未知 CTP 方向 \"9\" 本该报错，却得到 %v", d)
	}
	if o, err := OffsetFromDIFF("CLOSEYESTERDAY"); err == nil {
		t.Errorf("DIFF 侧没有 CLOSEYESTERDAY，本该报错，却得到 %v", o)
	}
	if h, err := HedgeFromCTP(""); err == nil {
		t.Errorf("空串本该报错，却得到 %v", h)
	}
	// ⚠️ 零值必须是「未知」，不能是任何有意义的取值。
	if DirectionUnknown == Buy || DirectionUnknown == Sell {
		t.Error("Direction 的零值撞上了有意义的取值")
	}
	if OffsetUnknown == Open {
		t.Error("⚠️ Offset 的零值等于「开仓」—— 解析失败会静默变成开仓")
	}
	if HedgeUnknown == Speculation {
		t.Error("HedgeFlag 的零值撞上了「投机」")
	}
}

// TestOffsetIsClose 断言三种强平也算平仓。
func TestOffsetIsClose(t *testing.T) {
	closes := []Offset{Close, CloseToday, CloseYesterday, ForceClose, ForceOff, LocalForceClose}
	opens := []Offset{Open, OffsetUnknown}
	if len(closes) != 6 || len(opens) != 2 {
		t.Fatalf("用例数变了：closes=%d opens=%d", len(closes), len(opens))
	}
	for _, o := range closes {
		if !o.IsClose() {
			t.Errorf("%v 应算平仓 —— 漏掉强平会让强平成交被当成开仓，而账面依旧平", o)
		}
	}
	for _, o := range opens {
		if o.IsClose() {
			t.Errorf("%v 不该算平仓", o)
		}
	}
	if Close.SpecifiesPositionDate() {
		t.Error("⚠️ 裸 Close 不算显式声明今昨 —— 见 state.md 的 simnow_pending#1")
	}
	if !CloseToday.SpecifiesPositionDate() || !CloseYesterday.SpecifiesPositionDate() {
		t.Error("平今/平昨应算显式声明")
	}
}

// TestTradingDay 覆盖形态校验与自然日换算的边界。
func TestTradingDay(t *testing.T) {
	d, err := ParseTradingDay("20260907")
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if d.Year() != 2026 || d.Month() != 9 || d.Day() != 7 {
		t.Errorf("拆解错误: %d-%d-%d", d.Year(), d.Month(), d.Day())
	}
	if d.String() != "20260907" {
		t.Errorf("String() = %q", d.String())
	}

	bad := []string{"2026090", "202609077", "2026-09-07", "20260230", "20261301", "abcdefgh", ""}
	if len(bad) != 7 {
		t.Fatalf("反例数应为 7，实际 %d", len(bad))
	}
	for _, s := range bad {
		if got, err := ParseTradingDay(s); err == nil {
			t.Errorf("%q 本该报错，却得到 %d", s, got)
		}
	}
	if err := TradingDay(20260230).Validate(); err == nil ||
		!strings.Contains(err.Error(), "真实存在") {
		t.Errorf("2 月 30 日应被日历核出来，实得 %v", err)
	}
}
