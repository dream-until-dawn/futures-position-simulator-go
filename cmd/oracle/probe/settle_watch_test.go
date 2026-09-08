package probe

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestNumOrDashDistinguishesFourStates 断言四种状态互不混淆。
//
// ⚠️ 上一版用 kq.MustNum，四种里有三种都变成 "0.0000"：
// 缺字段、柜台说无值、柜台说 0 —— 全都长得一样。
// 结算发生时「0 → 3160」照样触发，所以**探测**没坏；
// 坏的是留在夹具里的那句话：柜台说的是「无值 → 3160」。
// 事后读证据的人分不出柜台当时报的是 0 还是没有。
func TestNumOrDashDistinguishesFourStates(t *testing.T) {
	m := map[string]any{
		"settlement":     "-",    // 柜台明确说无值
		"pre_settlement": 3158.0, // 有值
		"zero":           0.0,    // 真的是 0
		"weird":          true,   // 不认识的类型
	}
	cases := []struct{ key, want string }{
		{"pre_settlement", "3158.0000"},
		{"settlement", "-"},
		{"zero", "0.0000"},
		{"missing", "(缺字段)"},
		{"weird", "(bool)"},
	}
	if len(cases) != 5 {
		t.Fatalf("用例 %d 条，应为 5 —— 增删了就同步改这个数", len(cases))
	}
	// ⚠️ 判别力：五个用例必须给出五个**互不相同**的结果。
	// 只要有两个相同，那两种状态在夹具里就分不开了 —— 而那正是要修的病。
	seen := map[string]string{}
	for _, c := range cases {
		got := numOrDash(m, c.key)
		if got != c.want {
			t.Errorf("⚠️ %s：得到 %q，应为 %q", c.key, got, c.want)
		}
		if prev, dup := seen[got]; dup {
			t.Errorf("⚠️ %s 与 %s 渲染成同一个 %q —— 两种状态在夹具里分不开了",
				c.key, prev, got)
		}
		seen[got] = c.key
	}
	if got := numOrDash(nil, "x"); got != "(无截面)" {
		t.Errorf("空截面应渲染成 (无截面)，得到 %q", got)
	}
}

// TestWatcherUsesNumOrDash 断言采集器**真的走**这个函数。
//
// ⚠️ 与 classifyFeeMode 那次同一个教训（方法论第 28 条）：
// 把渲染抽成函数、写好穷举测试，却忘了在采集路径上用它 ——
// 那时穷举测试测的是一段死代码，而夹具里照样写着 0.0000。
//
// 用 AST 查而不是 grep：注释里到处都是这个函数名。
func TestWatcherUsesNumOrDash(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "exp_settle_watch.go", nil, 0) // 0 = 丢掉注释
	if err != nil {
		t.Fatal(err)
	}
	calls, mustNum := 0, 0
	ast.Inspect(f, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := ce.Fun.(type) {
		case *ast.Ident:
			if fn.Name == "numOrDash" {
				calls++
			}
		case *ast.SelectorExpr:
			// ⚠️ 反向也要查：旧写法留一处，那一处的字段就还是错的，
			// 而其余字段是对的 —— 混着的证据比全错的更难发现。
			if fn.Sel.Name == "MustNum" {
				mustNum++
			}
		}
		return true
	})
	// ⚠️ 「调用次数 > 0」不够：采集器有**三处**要渲染（账户 / 持仓 / 行情），
	// 只查大于零的话，三处里退回去一处是查不出来的 ——
	// 而**混着两种写法的证据比全错的更难发现**：大部分字段是对的，个别不是。
	//
	// 这一条是破坏验证逼出来的：原来那个破坏之所以能红，
	// 靠的是它同时引入了 kq.MustNum（被下面那条抓到），
	// 而不是靠这条计数。把 kq.MustNum 换成一个不需要 import 的写法之后，
	// 计数从 3 掉到 2，**这条照样绿**。
	const wantCalls = 3
	if calls < wantCalls {
		t.Errorf("⚠️ numOrDash 在采集器里只被调用了 %d 次，应当至少 %d 次"+
			"（账户 / 持仓 / 行情各一处）—— "+
			"少一处就是那一类字段还在用旧写法，而混着的证据最难发现", calls, wantCalls)
	}
	if mustNum != 0 {
		t.Errorf("⚠️ 采集器里还有 %d 处 MustNum —— "+
			"混着两种写法的证据比全错的更难发现：大部分字段是对的，个别不是", mustNum)
	}
}
