package probe

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// TestDecideFallbackNeverRisksYesterday 穷举平仓回退判定，钉住三条不变式。
//
// ⚠️ 最要紧的一条是：**只要账上有昨仓，任何路径都不得回退到 CLOSE。**
// 原实现的失败方向朝着「悄悄平了别的仓」，而日志只会说「换一种开平标志再试」——
// 它不是没平掉，它是**平了别的**，而那批昨仓是六条实验共用、当晚不可再生的。
func TestDecideFallbackNeverRisksYesterday(t *testing.T) {
	// 构造一份只含昨仓字段的持仓截面。
	pos := func(longHis, shortHis float64) map[string]any {
		return map[string]any{
			"volume_long_his": longHis, "volume_short_his": shortHis,
			"volume_long_today": 1.0, "volume_short_today": 1.0,
		}
	}
	cases := []struct {
		name       string
		volHis     float64
		done       bool
		volumeLeft int
		want       fallbackDecision
	}{
		{"全成", 0, true, 0, fallbackDone},
		{"全成·有昨仓", 2, true, 0, fallbackDone},
		{"超时·无昨仓", 0, false, 1, fallbackStopTimeout},
		{"超时·有昨仓", 2, false, 1, fallbackStopTimeout},
		{"被拒·无昨仓", 0, true, 1, fallbackAllowed},
		{"被拒·有昨仓", 2, true, 1, fallbackForbiddenYesterday},
		{"被拒·昨仓半手", 0.5, true, 1, fallbackForbiddenYesterday},
	}
	if len(cases) != 7 {
		t.Fatalf("用例有 %d 条，应为 7 —— 增删了就同步改这个数", len(cases))
	}
	for _, c := range cases {
		if got := decideFallback(pos(c.volHis, 0), kq.Buy, c.done, c.volumeLeft); got != c.want {
			t.Errorf("%s：decideFallback(%v,%v,%d) = %v，应为 %v",
				c.name, c.volHis, c.done, c.volumeLeft, got, c.want)
		}
	}

	// —— 不变式一：有昨仓时，**没有任何输入**能得到「允许回退」——
	//
	// ⚠️ 这条是绝对断言，与上面的用例表分开写：用例表是我列出来的组合，
	// 而这一条穷举了输入空间。**列表会漏，穷举不会。**
	for _, done := range []bool{true, false} {
		for left := 0; left <= 3; left++ {
			for _, his := range []float64{0.001, 1, 2, 100} {
				if decideFallback(pos(his, 0), kq.Buy, done, left) == fallbackAllowed {
					t.Errorf("⚠️ 昨仓 %v、done=%v、left=%d 时竟然允许回退 CLOSE —— "+
						"实测 CLOSE 在 UseHistory 交易所上被解释为平昨，这会平掉昨仓",
						his, done, left)
				}
			}
		}
	}

	// —— 不变式三：方向 → 昨仓字段的映射不得写反 ——
	//
	// ⚠️ 这个映射此前在调用点上，测不到。挪进被测函数之后它才成为可断言的东西，
	// 而它正是那种「写反了也照样跑、只在特定方向上错」的映射。
	if decideFallback(pos(2, 0), kq.Buy, true, 1) != fallbackForbiddenYesterday {
		t.Error("⚠️ 多头方向没有读 volume_long_his —— 映射写反了")
	}
	if decideFallback(pos(0, 2), kq.Sell, true, 1) != fallbackForbiddenYesterday {
		t.Error("⚠️ 空头方向没有读 volume_short_his —— 映射写反了")
	}
	if decideFallback(pos(0, 2), kq.Buy, true, 1) != fallbackAllowed {
		t.Error("⚠️ 多头方向读到了**空头**的昨仓 —— 映射串了，会在没有昨仓时误拦")
	}
	if decideFallback(pos(2, 0), kq.Sell, true, 1) != fallbackAllowed {
		t.Error("⚠️ 空头方向读到了**多头**的昨仓 —— 映射串了")
	}

	// —— 不变式二：超时**永远**不回退，与昨仓无关 ——
	//
	// 超时不等于被拒。混为一谈时，「限价没被打到」也会触发回退。
	for _, his := range []float64{0, 1, 5} {
		for left := 1; left <= 3; left++ {
			if d := decideFallback(pos(his, 0), kq.Buy, false, left); d != fallbackStopTimeout {
				t.Errorf("⚠️ 超时（昨仓 %v，left=%d）得到 %v，应为「撤单并停止」—— "+
					"超时不等于被拒", his, left, d)
			}
		}
	}
}

// TestFallbackSwitchHandlesEveryDecision 断言 flatten 里那个 switch
// **逐个处理**了 decideFallback 的每一种返回值。
//
// ⚠️ 它补的是上一版留下的第二层缺口。第一层是「判定函数没有调用者」——
// 评审把生产路径上的守卫换成 `if false`，全库仍然全绿，
// 因为那 7 条用例约束的是一个没人调用的平行函数。接线之后那一层堵上了。
//
// 但**还剩一层**：判定接上了，`flatten` 对每个分支怎么反应仍然没有测试。
// 谁把 `fallbackForbiddenYesterday` 那条 case 的动作改成继续循环，
// 单元测试照样全绿——而那正好是会平掉昨仓的那条路径。
//
// ⚠️ 判据走语法树，不扫文本：注释里提到某个常量名不算处理了它。
// 这与凭据读取那条判据同源（silent-risks.md 第 17 条）。
//
// ⚠️ **已知的误报形状**：判据要求 switch **直接**以 `decideFallback(...)` 为 tag。
// 把结果先存进变量再 `switch d {` 是完全正当的重构，而它会红。
// 这是刻意的风格约束，不是 bug：它让判定的调用点**可被机械定位在唯一一处**。
// 若哪天这个约束碍事，正确的动作是让判据跟踪变量，不是删掉判据。
//
// ⚠️ **它仍然不覆盖「某个分支的动作被改成了错的动作」**——
// 那需要一个能被喂假委托状态的 flatten，而 flatten 要连柜台。
// 这一层的缺口留在这里，不拿本条去盖它。
func TestFallbackSwitchHandlesEveryDecision(t *testing.T) {
	want := map[string]bool{
		"fallbackDone":               false,
		"fallbackStopTimeout":        false,
		"fallbackForbiddenYesterday": false,
		"fallbackAllowed":            false,
	}
	if len(want) != 4 {
		t.Fatalf("判定取值有 %d 个，应为 4 —— 增删了就同步改这里", len(want))
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "experiments.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok || sw.Tag == nil {
			return true
		}
		call, ok := sw.Tag.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok || id.Name != "decideFallback" {
			return true
		}
		found++
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			if cc.List == nil {
				t.Error("⚠️ switch 里有 default 分支 —— " +
					"那会把将来新增的判定取值悄悄吞掉，本条也就查不出漏处理")
			}
			for _, e := range cc.List {
				ident, ok := e.(*ast.Ident)
				if !ok {
					continue
				}
				if _, known := want[ident.Name]; !known {
					t.Errorf("switch 处理了未知取值 %q", ident.Name)
				}
				want[ident.Name] = true
				checkCaseTerminates(t, fset, ident.Name, cc)
			}
		}
		return true
	})

	// ⚠️ 迭代次数下界：找不到那个 switch 时下面的检查会「全部未处理」而红，
	// 但错误信息会指向错的方向。这里先把「没找到」和「漏处理」分开。
	if found != 1 {
		t.Fatalf("在 experiments.go 里找到 %d 个 switch decideFallback(...)，应为 1 —— "+
			"是判定被挪走了、被复制了，还是解析规则失效了？", found)
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("⚠️ flatten 的 switch **没有处理** %s —— "+
				"漏处理的分支会静默走到 switch 之后，而那里是「平仓失败」的兜底返回；"+
				"若漏的是 fallbackForbiddenYesterday，昨仓保护就没了", name)
		}
	}
}

// mustReturn 列出**必须以 return 中止**的判定分支，以及它们的返回是不是错误。
//
// ⚠️ 这张表是「分支的动作」这一层唯一被机械守住的部分，其余仍然靠人。
// 评审实测过：分支保留、把 `return` 抽掉，全库**全绿** ——
// 那不是注释写得谨慎，是那一层真的没有覆盖。本表补的就是这一小块。
var mustReturn = map[string]bool{
	"fallbackDone":               false, // 平成了，return nil
	"fallbackStopTimeout":        true,  // 超时：撤单后必须报错中止
	"fallbackForbiddenYesterday": true,  // 有昨仓：必须报错中止，绝不可继续循环
	// fallbackAllowed 刻意不在表里：它就是要继续循环去试下一个开平标志。
}

// checkCaseTerminates 断言一个 case 分支确实以 return 中止，且错误分支不是 return nil。
//
// ⚠️ **它是部分修法，抓不到的比抓得到的多**：
//
//	抓得到   分支里没有 return（保护还在名单上，但不再中止流程）
//	抓得到   错误分支写成 return nil（静默报告「平成了」——最坏的一种）
//	⚠️ 抓不到 先发一笔单再 return
//	⚠️ 抓不到 return 了一个语义不对的错误
//
// 真正闭环要给下单面抽接口、用假客户端跑 flatten 的行为。**刻意不做**：
// 今晚就是窗口，而 flatten 未必活过 v0.1.0 ——
// 为一个可能被丢掉的形状引一层接口，是把成本花在错的地方。
// **如实记着 + 一条部分守卫**，是这里正确的停手位置（见 silent-risks.md 第 26 条的处理法）。
func checkCaseTerminates(t *testing.T, fset *token.FileSet, name string, cc *ast.CaseClause) {
	t.Helper()
	wantErr, listed := mustReturn[name]
	if !listed {
		// 不在表里的分支（fallbackAllowed）不得 return —— 它必须继续循环。
		for _, st := range cc.Body {
			if _, ok := st.(*ast.ReturnStmt); ok {
				t.Errorf("⚠️ %s 分支里出现了 return —— 它应当继续循环去试下一个开平标志，"+
					"return 会让回退机制整个失效", name)
			}
		}
		return
	}

	var ret *ast.ReturnStmt
	for _, st := range cc.Body {
		if r, ok := st.(*ast.ReturnStmt); ok {
			ret = r
		}
	}
	if ret == nil {
		t.Errorf("⚠️ %s 分支**没有 return** —— 它会静默走到 switch 之后继续循环。"+
			"若这是昨仓保护，保护就等于没有", name)
		return
	}
	if !wantErr {
		return
	}
	// 错误分支：return 必须带值，且不能是裸 nil。
	if len(ret.Results) == 0 {
		t.Errorf("⚠️ %s 分支的 return 没有返回值", name)
		return
	}
	if id, ok := ret.Results[0].(*ast.Ident); ok && id.Name == "nil" {
		t.Errorf("⚠️ %s:%d 分支 return nil —— 那是**静默报告平仓成功**，"+
			"而持仓仍在。这比不 return 更坏：调用方不会重试，也不会告警",
			"flatten", fset.Position(ret.Pos()).Line)
	}
}
