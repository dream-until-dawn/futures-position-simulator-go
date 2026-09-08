package probe

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// TestHistoryOnlySide 穷举前提判定的每一种输入。
//
// ⚠️ 它是本实验最容易空转的地方：一个在错误前提下跑出来的「平今被接受」，
// 与真正的「柜台不区分今昨」**长得一模一样**。
func TestHistoryOnlySide(t *testing.T) {
	pos := func(lt, lh, st, sh any) map[string]any {
		m := map[string]any{}
		set := func(k string, v any) {
			if v != nil {
				m[k] = v
			}
		}
		set("volume_long_today", lt)
		set("volume_long_his", lh)
		set("volume_short_today", st)
		set("volume_short_his", sh)
		return m
	}
	cases := []struct {
		name string
		p    map[string]any
		side string // "" 表示前提不成立
	}{
		{"多头只有昨仓", pos(0.0, 3.0, 0.0, 0.0), "long"},
		{"空头只有昨仓", pos(0.0, 0.0, 0.0, 2.0), "short"},
		{"⚠️ 多头今昨都有 —— 前提不成立", pos(1.0, 3.0, 0.0, 0.0), ""},
		{"⚠️ 只有今仓 —— 前提不成立", pos(3.0, 0.0, 0.0, 0.0), ""},
		{"两边都空 —— 前提不成立", pos(0.0, 0.0, 0.0, 0.0), ""},
		{"⚠️ 字段读不到 —— 不当成零，前提不成立", pos(nil, 3.0, nil, nil), ""},
		{"⚠️ 字段是字符串 \"-\" —— 同样读不到", pos("-", 3.0, "-", "-"), ""},
		{"两边都只有昨仓 —— 取多头", pos(0.0, 2.0, 0.0, 2.0), "long"},
	}
	if len(cases) != 8 {
		t.Fatalf("用例 %d 条，应为 8 —— 增删了就同步改这个数", len(cases))
	}
	// ⚠️ 判别力：成立与不成立两侧都要有样本。
	yes, no := 0, 0
	for _, c := range cases {
		dir, side, ok := historyOnlySide(c.p)
		if c.side == "" {
			if ok {
				t.Errorf("⚠️ %s：判成前提成立（%s）—— "+
					"在这个前提上跑出来的「平今被接受」，与真正的「不区分今昨」长得一模一样",
					c.name, side)
			}
			no++
			continue
		}
		yes++
		if !ok {
			t.Errorf("⚠️ %s：判成前提不成立 —— 那会让实验白跑一次", c.name)
			continue
		}
		if side != c.side {
			t.Errorf("%s：选了 %s，应为 %s", c.name, side, c.side)
		}
		wantDir := kq.Buy
		if c.side == "short" {
			wantDir = kq.Sell
		}
		if dir != wantDir {
			t.Errorf("%s：方向 %v，应为 %v —— "+
				"⚠️ 方向搞反会在双向持仓上**平掉另一边**且不报错", c.name, dir, wantDir)
		}
	}
	if yes == 0 || no == 0 {
		t.Fatalf("⚠️ 用例只覆盖一侧（成立 %d / 不成立 %d）", yes, no)
	}
}

// TestPositionDateTypeUsesTheGuard 断言前提判定**真的在实验路径上**。
//
// ⚠️ 方法论第 28 条那个教训的第三次应用：把判定抽成纯函数、穷举它，
// 却忘了在生产路径上调用 —— 那时穷举测的是一段死代码。
func TestPositionDateTypeUsesTheGuard(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "exp_position_date_type.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls, inExp := 0, false
	ast.Inspect(f, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok {
			inExp = fd.Name.Name == "expPositionDateType"
			return true
		}
		if !inExp {
			return true
		}
		if ce, ok := n.(*ast.CallExpr); ok {
			if id, ok := ce.Fun.(*ast.Ident); ok && id.Name == "historyOnlySide" {
				calls++
			}
		}
		return true
	})
	if calls == 0 {
		t.Error("⚠️ historyOnlySide 在实验里一次都没被调用 —— " +
			"前提判定抽出来了却没接回去，穷举测试测的是一段死代码")
	}
}

// TestPositionDateTypeUsesFarPrice 断言用的是**挂不上的**限价。
//
// ⚠️ 用对手价的话，被接受的那一支会当场成交并吃掉种子 ——
// 而种子是后面几个实验的输入。这条守的是「实验不该有副作用」。
func TestPositionDateTypeUsesFarPrice(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "exp_position_date_type.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	far, aggressive := 0, 0
	ast.Inspect(f, func(n ast.Node) bool {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if se, ok := ce.Fun.(*ast.SelectorExpr); ok {
			switch se.Sel.Name {
			case "FarPrice":
				far++
			case "AggressivePrice":
				aggressive++
			}
		}
		return true
	})
	if far == 0 {
		t.Error("⚠️ 没用 FarPrice —— 被接受的那一支会当场成交并吃掉种子")
	}
	if aggressive != 0 {
		t.Errorf("⚠️ 用了 %d 处 AggressivePrice —— 那是「几乎必成」的价，"+
			"本实验只要知道委托被不被接受，不要它成交", aggressive)
	}
}
