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
				// ⚠️ 缺失是**合法状态**：套利与套保的 DIFF 线值至今未实测，
				// 而缺失会让调用方当场拿到 false，比一个猜错的线值安全得多。
				// 哪些可以缺、缺的是不是这两个，由
				// TestHedgeDIFFTokensAreMeasuredNotGuessed 单独钉住。
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

// TestHedgeDIFFTokensAreMeasuredNotGuessed 断言 DIFF 的投机套保线值是**实测**的那个。
//
// ⚠️ 它补的是往返测试的一个盲区：只查往返的测试对线值本身**没有判别力** ——
// `SPEC`、`SPECULATION`、`banana` 都能通过，只要编码解码两边一致。
// 而线值是给**别人**看的，一致性只保证自己人内部说得通。
//
// 实测：交易日 20260908 的成交截面里 hedge_flag 全是 `"SPECULATION"`，
// 417 笔无一例外。原先写的 `"SPEC"` 被这一批证伪。
func TestHedgeDIFFTokensAreMeasuredNotGuessed(t *testing.T) {
	s, ok := Speculation.DIFF()
	if !ok || s != "SPECULATION" {
		t.Errorf("⚠️ 投机的 DIFF 线值应为 \"SPECULATION\"（实测 417/417），得到 %q/%v —— "+
			"这个值是给柜台看的，往返一致证明不了它对", s, ok)
	}
	// ⚠️ 套利与套保**必须缺失**，而不是被照着投机类推出来。
	//
	// `SPECULATION` 不是 `SPEC` 的展开（那一版就是这么猜的，被证伪了），
	// `ARBITRAGE` 也就不必是 `ARBI` 的展开。
	// 缺失是响的：DIFF() 返回 false、HedgeFromDIFF 报错。猜错是哑的：原样发出去。
	for _, h := range []HedgeFlag{Arbitrage, Hedge} {
		if s, ok := h.DIFF(); ok {
			t.Errorf("⚠️ %v 的 DIFF 线值 %q 是从哪来的？至今零观测 —— "+
				"照着投机类推正是上一版被证伪的那种做法", h, s)
		}
	}
	// 反向：CTP 侧三个取值都有出处（ThostFtdcUserApiDataType.h，probes.md §3），
	// 所以那边不该缺。⚠️ 少了这一句，上面那条可以靠「全都缺失」通过。
	for _, h := range []HedgeFlag{Speculation, Arbitrage, Hedge} {
		if _, ok := h.CTP(); !ok {
			t.Errorf("%v 没有 CTP 取值 —— CTP 侧三个都是从头文件清点出来的，不该缺", h)
		}
	}
}

// TestTradingDayOrdering 钉住 Before / After 的方向与**严格性**。
//
// ⚠️ 20260910 补：`types` 自己**一条都没测过**它们。
// 把 After 改成 `d < o` 之后，本包全绿；红的是 account 那三条 ——
// 而它们红在**症状**上（「下一交易日不晚于当前交易日」，结算被拒），
// 不在「After 的方向反了」这件事上。
//
//	⚠️ 一个方向反了的比较函数，报出来的是「结算拒绝了」。
//	顺着那句话去查，人会先怀疑结算，而不是怀疑 `>`。
//
// ⚠️ 而 account.Settle 的守卫正是 `!nextDay.After(day)` —— 方向一反，
// 「不能倒着结算」这条保护就掉个头，变成「只能倒着结算」。
func TestTradingDayOrdering(t *testing.T) {
	a, b := TradingDay(20260909), TradingDay(20260910)
	if !a.Before(b) {
		t.Errorf("⚠️ %d 应当早于 %d —— Before 的方向反了", a, b)
	}
	if !b.After(a) {
		t.Errorf("⚠️ %d 应当晚于 %d —— After 的方向反了", b, a)
	}
	if b.Before(a) {
		t.Errorf("⚠️ %d 不该早于 %d", b, a)
	}
	if a.After(b) {
		t.Errorf("⚠️ %d 不该晚于 %d", a, b)
	}
	// ⚠️ 相等这一格单独钉：两者都必须为 false。
	// 「早于或等于」会让重复结算同一个交易日被放行，而账面看不出异样。
	if a.Before(a) || a.After(a) {
		t.Errorf("⚠️ 同一个交易日既不早于也不晚于自己 —— "+
			"Before=%t / After=%t。放宽成「或等于」会让重复结算同一天被放行",
			a.Before(a), a.After(a))
	}
}

// TestDecadeTieIsRefused 钉住**三位年月恰好等距时拒绝定年代**。
//
// ⚠️ 20260910 补：这条判据此前**全库没有任何东西测它**。
// 把 `if tie` 关掉之后，`go test ./...` 全绿 —— 而它的后果是
// 郑商所的三位合约码在跨十年等距时**静默挑一个年代**。
//
//	MA109 @ 20260910   候选 2021-09 与 2031-09 各距 60 个月
//	                   关掉判据 ⇒ 挑一个 ⇒ **合约年份差十年，而它长得像个正常合约**
//
// ⚠️ 这正是本库反复记的那种形状：错的不是「报错了」，是**得到一个看起来正常的值**。
func TestDecadeTieIsRefused(t *testing.T) {
	if _, err := ParseNative(CZCE, "MA109", 20260910); err == nil {
		t.Fatal("⚠️ MA109 在 20260910 上两个年代恰好等距，本该拒绝定年代 —— " +
			"静默挑一个会得到一个年份差十年、却完全正常的合约")
	}
	// ⚠️ 判别力：不等距的必须照常解析成功。只测拒绝那一侧的话，
	// 一个「永远拒绝」的实现也能过。
	if _, err := ParseNative(CZCE, "MA209", 20260910); err != nil {
		t.Errorf("⚠️ MA209 并不等距，本该解析成功，却报了 %v —— "+
			"上面那条断言可能只是因为它什么都拒绝", err)
	}
}
