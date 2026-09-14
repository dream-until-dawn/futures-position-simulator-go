package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// TestLongSidesReadsBothRepresentations 钉住「今/昨」在**两种持仓表示**下都算得对，
// 并且**不信 `YdPosition`**。
//
// ⚠️ 这一条是在写周一操作单时撞出来的：CTP 对大商所可能把今昨**合成一条**记录，
// 而 `YdPosition` 是日初值、平昨之后可能不减 —— 拿它当「此刻还剩几手昨仓」，
// 平仓前后会读出同一个数，**而 #4 要分的正是这件事**。
func TestLongSidesReadsBothRepresentations(t *testing.T) {
	rec := func(inst string, date byte, position, today, ydField int) *def.CThostFtdcInvestorPositionField {
		p := &def.CThostFtdcInvestorPositionField{
			PosiDirection: def.THOST_FTDC_PD_Long,
			PositionDate:  def.TThostFtdcPositionDateType(date),
			Position:      def.TThostFtdcVolumeType(position),
			TodayPosition: def.TThostFtdcVolumeType(today),
			YdPosition:    def.TThostFtdcVolumeType(ydField),
		}
		copy(p.InstrumentID[:], inst)
		return p
	}
	for _, c := range []struct {
		name string
		pos  map[string]*def.CThostFtdcInvestorPositionField
		want longSides
	}{
		{"合成一条（大商所式）：今 1 昨 1",
			map[string]*def.CThostFtdcInvestorPositionField{
				"a": rec("m2701", def.THOST_FTDC_PSD_Today, 2, 1, 1)},
			longSides{Today: 1, Yd: 1, YdField: 1, Records: 1}},
		{"分开两条（上期所式）：今 1 昨 1",
			map[string]*def.CThostFtdcInvestorPositionField{
				"a": rec("m2701", def.THOST_FTDC_PSD_Today, 1, 1, 0),
				"b": rec("m2701", def.THOST_FTDC_PSD_History, 1, 0, 1)},
			longSides{Today: 1, Yd: 1, YdField: 1, Records: 2}},
		{"OpenVolume 按和读（种子 1 手合成记作今仓 + 今天开过 1）",
			map[string]*def.CThostFtdcInvestorPositionField{
				"a": func() *def.CThostFtdcInvestorPositionField {
					p := rec("m2701", def.THOST_FTDC_PSD_Today, 2, 2, 0)
					p.OpenVolume = 1
					return p
				}()},
			longSides{Today: 2, Yd: 0, YdField: 0, Records: 1, OpenedToday: 1}},
		// ⚠️ 这一格是「不信 YdPosition」的判别样本：昨仓已被平掉，
		// 而日初的 YdPosition 仍报 1。若按字段读，会误判「昨还在」。
		{"⚠️ 昨仓已平而 YdPosition 仍是日初值 1",
			map[string]*def.CThostFtdcInvestorPositionField{
				"a": rec("m2701", def.THOST_FTDC_PSD_Today, 1, 1, 1)},
			longSides{Today: 1, Yd: 0, YdField: 1, Records: 1}},
		{"别的合约与空头不算",
			map[string]*def.CThostFtdcInvestorPositionField{
				"a": rec("y2701", def.THOST_FTDC_PSD_Today, 3, 3, 0),
				"b": func() *def.CThostFtdcInvestorPositionField {
					p := rec("m2701", def.THOST_FTDC_PSD_Today, 5, 5, 0)
					p.PosiDirection = def.THOST_FTDC_PD_Short
					return p
				}()},
			longSides{}},
	} {
		if got := longSidesOf(c.pos, "m2701"); got != c.want {
			t.Errorf("%s：得到 %+v，要 %+v", c.name, got, c.want)
		}
	}
}

// TestCloseOrderVerdictRefusesBeforeItConcludes 钉住 #4 判定的**六种**取值与优先级。
//
// ⚠️ 与 holdVerdict 同一条纪律：「不该下结论」的几种情形要排在真结论之前，
// 否则它们会各自得到一个看起来完全正常的结论。
// ⚠️ 而 coNoYesterday 排在最前，理由不同：它**本身是一条结论**（大商所在 CTP 上
// 也不显示昨仓 ⇒ #4 结构性测不出），不该被「前提不成立」吞掉。
func TestCloseOrderVerdictRefusesBeforeItConcludes(t *testing.T) {
	s := func(today, yd int) longSides { return longSides{Today: today, Yd: yd} }
	// so 带上「本交易日开过几手」。
	so := func(today, yd, opened int) longSides { return longSides{Today: today, Yd: yd, OpenedToday: opened} }
	cases := []struct {
		name          string
		before, after longSides
		want          closeOrderKind
	}{
		{"没有昨仓、今仓不是今天开的 ⇒ 结论：结构性测不出（排最前）", s(1, 0), s(0, 0), coNoYesterday},
		{"没有昨仓、今仓也不是 1、都不是今天开的 ⇒ 仍然先报没有昨仓", s(3, 0), s(3, 0), coNoYesterday},
		// ⚠️ 下面三格是 20260913 补的：上一版把前两格也判成 coNoYesterday（一条结论）。
		{"⚠️ 今 0 昨 0：种子不在 ⇒ 不是结论", s(0, 0), s(0, 0), coNoSeed},
		{"⚠️ 今 1 昨 0 而今天开过 1：种子还没跨过结算 ⇒ 不是结论", so(1, 0, 1), so(1, 0, 1), coNoSeed},
		{"今 2 昨 0 而今天只开过 1：有一手不是今天开的 ⇒ 结论", so(2, 0, 1), so(2, 0, 1), coNoYesterday},
		{"今仓不是 1 ⇒ 前提不成立", s(2, 1), s(1, 1), coNoPremise},
		{"今仓是 0 ⇒ 前提不成立", s(0, 1), s(0, 0), coNoPremise},
		{"没平掉 ⇒ 不判", s(1, 1), s(1, 1), coNotOneLot},
		{"平了两手 ⇒ 不判", s(1, 2), s(0, 1), coNotOneLot},
		{"消耗今仓", s(1, 1), s(0, 1), coConsumedToday},
		{"消耗昨仓", s(1, 1), s(1, 0), coConsumedYesterday},
		{"昨仓多于一手时消耗昨仓", s(1, 3), s(1, 2), coConsumedYesterday},
	}
	seen := map[closeOrderKind]bool{}
	for _, c := range cases {
		got := closeOrderVerdict(c.before, c.after)
		seen[got] = true
		if got != c.want {
			t.Errorf("%s：得到 %d，要 %d", c.name, got, c.want)
		}
	}
	// ⚠️ 反空转：六种取值每一种都要被走到。
	for _, k := range []closeOrderKind{coNoPremise, coNoYesterday, coNotOneLot, coConsumedToday, coConsumedYesterday, coNoSeed} {
		if !seen[k] {
			t.Errorf("⚠️ 判定 %d 一条用例都没走到 —— 那一支从没被测过", k)
		}
	}
	// ⚠️ 两个真结论必须**互不相同**地被判出来 —— 否则一个恒返回 coConsumedToday 的实现
	// 在只有「消耗今仓」用例的表上也能全绿。
	if closeOrderVerdict(s(1, 1), s(0, 1)) == closeOrderVerdict(s(1, 1), s(1, 0)) {
		t.Fatal("⚠️ 消耗今仓与消耗昨仓被判成同一个结论 —— 判定没有判别力")
	}
}

// TestGenericCloseUsesTheGenericFlag 钉住 #4 那一笔判别性委托的开平标志是**通用平仓**。
//
// ⚠️⚠️ 用 `CloseToday` / `CloseYesterday` 就是**替柜台指定了答案**：
// 跑出来的「消耗了今仓」只是复述了自己发的标志，整轮实验无效 ——
// **而输出看起来完全正常**。这是本轮最容易「修」错的一处：
// 谁看见 DCE 上平今平昨都被接受（#16），都可能顺手把它改成「更明确」的那个。
func TestGenericCloseUsesTheGenericFlag(t *testing.T) {
	if r := genericCloseReq("DCE", "m2701", 3000); r.Offset != def.THOST_FTDC_OF_Close {
		t.Fatalf("⚠️ 判别性委托的开平标志是 %q，要通用平仓 %q —— **替柜台指定了答案**",
			string(r.Offset), string(def.THOST_FTDC_OF_Close))
	}
	// ⚠️ 光看值不够：得确认 runCTPCloseOrder **真的用了**这个构造器发单，
	// 而不是另写了一笔 OrderReq —— 否则本条测的是一个没人调用的函数。
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "closeorder.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := findFunc(f, "runCTPCloseOrder")
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPCloseOrder —— 本条后半在空集上跑")
	}
	used, explicit := false, 0
	ast.Inspect(fn, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "genericCloseReq" {
				used = true
			}
		}
		if sel, ok := n.(*ast.SelectorExpr); ok &&
			(sel.Sel.Name == "THOST_FTDC_OF_Close" || sel.Sel.Name == "THOST_FTDC_OF_CloseYesterday") {
			explicit++
		}
		return true
	})
	if !used {
		t.Error("⚠️ runCTPCloseOrder 没有调用 genericCloseReq —— 判别性委托是另写的，本条前半测了个没人用的函数")
	}
	// ⚠️ 收尾只许用 CloseToday 平**今**仓（为了不碰昨仓那手种子）；
	// 通用 Close 与 CloseYesterday 在这个函数体里一次都不该**直接**出现。
	if explicit != 0 {
		t.Errorf("⚠️ runCTPCloseOrder 里直接写了 %d 处 OF_Close / OF_CloseYesterday —— "+
			"判别性委托必须只经 genericCloseReq 构造，收尾只许 CloseToday", explicit)
	}
}

// TestCleanupVerdictRefusesWhenSeedMayBeToday 钉住 `-cleanup` 只在今仓里确定没有种子时才动手。
func TestCleanupVerdictRefusesWhenSeedMayBeToday(t *testing.T) {
	so := func(today, yd, opened int) longSides { return longSides{Today: today, Yd: yd, OpenedToday: opened} }
	cases := []struct {
		name string
		s    longSides
		want string // "go" / "nothing" / "refuse"
	}{
		{"今 0 ⇒ 没有要收的", so(0, 1, 1), "nothing"},
		{"#4 收尾失败：种子作为昨仓可见、今 1 ⇒ 可以", so(1, 1, 1), "go"},
		{"#4 消耗了昨仓、收尾失败：今 1 昨 0、今天开过 1 ⇒ 可以", so(1, 0, 1), "go"},
		{"⚠️ 种子被记作今仓：今 1 昨 0、今天没开过 ⇒ 拒绝", so(1, 0, 0), "refuse"},
		{"⚠️ 种子被记作今仓 + 今天开过 1：今 2 昨 0 ⇒ 拒绝", so(2, 0, 1), "refuse"},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		err := cleanupVerdict(c.s)
		got := "refuse"
		switch {
		case err == nil:
			got = "go"
		case err == errNothingToClean:
			got = "nothing"
		}
		seen[got] = true
		if got != c.want {
			t.Errorf("%s：得到 %s，要 %s（%v）", c.name, got, c.want, err)
		}
	}
	if len(seen) != 3 {
		t.Errorf("⚠️ 三种结果只走到了 %v —— 判定没有判别力", seen)
	}
}

// TestCleanupChecksBeforeClosing 钉住 closeOrderCleanup **先判再平**，且 runCTPCloseOrder 的 -cleanup 分支真的调用它。
func TestCleanupChecksBeforeClosing(t *testing.T) {
	_, files := parsePkgMain(t)
	fn := findFunc(files["closeorder.go"], "closeOrderCleanup")
	run := findFunc(files["closeorder.go"], "runCTPCloseOrder")
	if fn == nil || run == nil {
		t.Fatal("⚠️ 找不到 closeOrderCleanup / runCTPCloseOrder")
	}
	pos := map[string]int{}
	ast.Inspect(fn, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				if _, dup := pos[id.Name]; !dup {
					pos[id.Name] = int(c.Pos())
				}
			}
		}
		return true
	})
	v, okV := pos["cleanupVerdict"]
	cl, okC := pos["closeTodayOnly"]
	if !okV || !okC || v > cl {
		t.Errorf("⚠️ closeOrderCleanup 里 cleanupVerdict@%d（有=%v）、closeTodayOnly@%d（有=%v）—— 要先判再平", v, okV, cl, okC)
	}
	called := false
	ast.Inspect(run, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "closeOrderCleanup" {
				called = true
			}
		}
		return true
	})
	if !called {
		t.Error("⚠️ runCTPCloseOrder 没有调用 closeOrderCleanup —— -cleanup 这个开关是空的")
	}
}

// TestCloseTodayStepStopsWhenYesterdayShrinks 钉住收尾在昨仓变少时**停手并报出来**，而不是接着平。
func TestCloseTodayStepStopsWhenYesterdayShrinks(t *testing.T) {
	so := func(today, yd int) longSides { return longSides{Today: today, Yd: yd} }
	for _, c := range []struct {
		name    string
		yd0     int
		s       longSides
		done    bool
		stopHas string
	}{
		{"今 1 昨 1（刚进收尾）⇒ 接着平", 1, so(1, 1), false, ""},
		{"今 0 昨 1 ⇒ 清空，种子留着", 1, so(0, 1), true, ""},
		{"没有昨仓的收尾：今 1 昨 0 ⇒ 接着平", 0, so(1, 0), false, ""},
		{"⚠️ 平今之后今仍 1、昨 1→0 ⇒ 平今吃掉了种子，停手", 1, so(1, 0), false, "平今单吃掉了昨仓"},
		{"⚠️ 今 0 而昨也少了 ⇒ 仍然停手报出来，不报「已清空」", 1, so(0, 0), false, "#4 的观测"},
	} {
		done, err := closeTodayStep(c.yd0, c.s)
		switch {
		case c.stopHas != "":
			if err == nil || !strings.Contains(err.Error(), c.stopHas) {
				t.Errorf("%s：要停手并报 %q，得到 done=%v err=%v", c.name, c.stopHas, done, err)
			}
		case err != nil || done != c.done:
			t.Errorf("%s：得到 done=%v err=%v，要 done=%v", c.name, done, err, c.done)
		}
	}
	// ⚠️ 接线：closeTodayOnly 真的每轮调用它。
	_, files := parsePkgMain(t)
	fn := findFunc(files["closeorder.go"], "closeTodayOnly")
	called := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "closeTodayStep" {
				called = true
			}
		}
		return true
	})
	if !called {
		t.Error("⚠️ closeTodayOnly 没有调用 closeTodayStep —— 昨仓变少时不会停手")
	}
}
