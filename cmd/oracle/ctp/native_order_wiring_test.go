package ctp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNativeOrderCallPaths 补强实际派发调用图；方法名标签本身不能证明下单经过安全阀。
func TestNativeOrderCallPaths(t *testing.T) {
	paths, _ := filepath.Glob("*.go")
	counts := map[string]int{}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			calleeRefs := map[token.Pos]bool{}
			var check token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := ""
				var pos token.Pos
				if id, ok := call.Fun.(*ast.Ident); ok {
					name = id.Name
					pos = id.Pos()
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					name = sel.Sel.Name
					pos = sel.Sel.Pos()
					if name == "Check" {
						check = call.Pos()
					}
				}
				switch name {
				case "invokeNativeRequest":
					if !(path == "native_query_windows.go" && fn.Name.Name == "invokeNativeQuery") && !(path == "native_order_windows.go" && fn.Name.Name == "invokeNativeOrder") {
						t.Error("原生内核出现白名单外调用者", path, fn.Name.Name)
					}
				case "invokeNativeOrder":
					if path != "native_order_windows.go" || fn.Name.Name != "sendNativeOrder" {
						t.Error("原生订单出现派发外调用者", path, fn.Name.Name)
					}
				case "sendNativeOrder":
					want := "nativeOrderCancel"
					if fn.Name.Name == "send" {
						want = "nativeOrderInsert"
						if check == 0 || check >= call.Pos() {
							t.Error("类型化报单未先经过安全阀")
						}
					}
					arg, ok := call.Args[0].(*ast.Ident)
					if path != "send_windows.go" || (fn.Name.Name != "send" && fn.Name.Name != "Cancel" && fn.Name.Name != "CancelByOrder") || !ok || arg.Name != want {
						t.Error("订单派发出现未知来源或动态方法", path, fn.Name.Name)
					}
				default:
					return true
				}
				counts[name]++
				calleeRefs[pos] = true
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok {
					return true
				}
				if (id.Name == "invokeNativeRequest" || id.Name == "invokeNativeOrder" || id.Name == "sendNativeOrder") && !calleeRefs[id.Pos()] {
					t.Error("原生派发被作为函数值旁路", path, fn.Name.Name)
				}
				return true
			})
			// 通用导出仅用于初始化/释放，不能绕回固定返回0的请求包装。
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "call" {
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok {
					t.Error("通用DLL调用方法不是固定字面量")
					return true
				}
				name, _ := strconv.Unquote(lit.Value)
				if !map[string]bool{"CreateApi": true, "CreateSpi": true, "RegisterSpi": true, "RegisterFront": true, "SubscribePrivateTopic": true, "SubscribePublicTopic": true, "Init": true, "Release": true}[name] {
					t.Error("通用DLL导出绕过原生请求边界", name)
				}
				return true
			})
		}
	}
	if counts["invokeNativeRequest"] != 2 || counts["invokeNativeOrder"] != 1 || counts["sendNativeOrder"] != 3 {
		t.Fatal("原生订单调用图缺项或多路径", counts)
	}
}
