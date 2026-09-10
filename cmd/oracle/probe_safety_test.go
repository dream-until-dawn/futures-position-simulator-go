package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// safeSide 是「挂得上、成不了」的那一端：买单用跌停、卖单用涨停。
//
// ⚠️ 判据不是「用了涨跌停」，是**用对了那一端**：
//
//	买 @ 跌停附近   没人肯那么便宜卖 ⇒ 挂着不成交
//	卖 @ 涨停附近   没人肯那么贵买   ⇒ 挂着不成交
//	⚠️ 反过来会**当场成交** —— 而一个只想被拒/只想挂一下的探针
//	   变成一次真实开仓，**在日志里与成功的探针长得一模一样**。
var safeSide = map[string]string{
	"THOST_FTDC_D_Buy":  "LowerLimitPrice",
	"THOST_FTDC_D_Sell": "UpperLimitPrice",
}

// TestRejectCasesAreNonExecutable 钉住 `rejectCases` 每一条都**成不了交**。
//
// ⚠️ 这些用例是**数据不是代码**，于是 `TestRestingPriceMatchesDirection`
// （它查的是 `runCTPOrder` 里的赋值语句）看不到它们 ——
// **一条判据不会自动跟着它的对象搬家。**
//
// ⚠️ 而这里的代价是真实的：清单里任何一条写反了方向，
// 那笔单会当场成交，账上多出一手敞口，而命令照样打印「跑完 N 条」。
func TestRejectCasesAreNonExecutable(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "reject.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	ast.Inspect(f, func(nd ast.Node) bool {
		cl, ok := nd.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var dir, price string
		for _, e := range cl.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			k, _ := kv.Key.(*ast.Ident)
			if k == nil {
				continue
			}
			switch k.Name {
			case "Dir":
				dir = exprName(kv.Value)
			case "Price":
				// 取整个函数体的源码文本，看它引用了哪一端。
				var sb strings.Builder
				ast.Inspect(kv.Value, func(x ast.Node) bool {
					if id, ok := x.(*ast.Ident); ok {
						sb.WriteString(id.Name + " ")
					}
					if se, ok := x.(*ast.SelectorExpr); ok {
						sb.WriteString(se.Sel.Name + " ")
					}
					return true
				})
				price = sb.String()
			}
		}
		if dir == "" || price == "" {
			return true
		}
		n++
		want, ok := safeSide[dir]
		if !ok {
			t.Errorf("⚠️ 第 %d 条用例的方向是 %q —— 不在已识别的两种里", n, dir)
			return true
		}
		if !strings.Contains(price, want) {
			t.Errorf("⚠️ 第 %d 条用例：方向 %s 的安全端是 %s，而它的价格函数引用的是 %q —— "+
				"**方向与挂价端不配对**。买要挂跌停那端、卖要挂涨停那端；"+
				"反过来这笔单会**当场成交**，而命令照样打印「跑完 N 条」",
				n, dir, want, strings.TrimSpace(price))
		}
		return true
	})
	// ⚠️ 判别力：没认出用例时上面一条都不会跑，而它照样绿。
	if n < 4 {
		t.Fatalf("⚠️ 只认出 %d 条用例（清单里至少 4 条）—— 形状变了，本条在空转", n)
	}
}

// TestFeeProbeOnlyBuysAtTheLowEnd 钉住 `runCTPFee` 只在**跌停那一端**挂买单。
//
// ⚠️ 它比 rejectCases 更需要看着：那些用例**指望被拒**，而这个探针
// **指望挂上** —— 挂上而挂错了端，就是一次真实成交。
func TestFeeProbeOnlyBuysAtTheLowEnd(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fee.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := findFunc(f, "runCTPFee")
	if fn == nil {
		t.Fatal("⚠️ fee.go 里找不到 runCTPFee —— 改名了？本条会在空集上跑")
	}
	dirs, sawLower, sawUpper := 0, false, false
	ast.Inspect(fn, func(nd ast.Node) bool {
		if kv, ok := nd.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Direction" {
				dirs++
				if got := exprName(kv.Value); got != "THOST_FTDC_D_Buy" {
					t.Errorf("⚠️ 第 %d 处 Direction 是 %q —— 本探针**只许买**。"+
						"卖单挂在低价那端会当场成交，而它指望的是挂着", dirs, got)
				}
			}
		}
		if se, ok := nd.(*ast.SelectorExpr); ok {
			switch se.Sel.Name {
			case "LowerLimitPrice":
				sawLower = true
			case "UpperLimitPrice":
				sawUpper = true
			}
		}
		return true
	})
	if dirs == 0 {
		t.Fatal("⚠️ 一处 Direction 都没认出来 —— 形状变了，本条在空转")
	}
	if !sawLower {
		t.Error("⚠️ 价位没有从 LowerLimitPrice 起算 —— 买单必须挂在跌停那一端")
	}
	// ⚠️ 允许出现 UpperLimitPrice（只是打印行情），但不许它进价格计算。
	// 这里只能看到「有没有出现」，所以退一格：出现了就要人确认它没进价格。
	if sawUpper {
		t.Logf("ⓘ 源码里出现了 UpperLimitPrice —— 本条**看不出它有没有进价格计算**。" +
			"⚠️ 这是本条声明的盲区：AST 这一层分不开「打印行情」与「参与算价」")
	}
}
