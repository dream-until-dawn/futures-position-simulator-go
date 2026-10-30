package ctp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestHandshakeWindowsWiring 是实际Windows源的结构守卫，不代替DLL行为实测。
func TestHandshakeWindowsWiring(t *testing.T) {
	f := &ast.Package{Files: map[string]*ast.File{}}
	for _, path := range []string{"client_windows.go", "handshake_callbacks_windows.go"} {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.Files[path] = file
	}
	wanted := map[string]string{"SetOnRspAuthenticate": handshakeAuth, "SetOnRspUserLogin": handshakeLogin, "SetOnRspSettlementInfoConfirm": handshakeConfirm}
	found := map[string]bool{}
	runner, generic, disconnected, nativeCode := false, false, false, false
	ast.Inspect(f, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == "sendHandshake" {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				r, ok := n.(*ast.ReturnStmt)
				if !ok || len(r.Results) != 1 {
					return true
				}
				x, ok := r.Results[0].(*ast.CallExpr)
				if !ok || len(x.Args) != 1 {
					return true
				}
				name, nok := x.Fun.(*ast.Ident)
				value, vok := x.Args[0].(*ast.Ident)
				if nok && vok && name.Name == "int32" && value.Name == "result" {
					nativeCode = true
				}
				return true
			})
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "run" {
			runner = true
		}
		if sel.Sel.Name == "generalError" && len(call.Args) == 2 {
			id, ok := call.Args[0].(*ast.Ident)
			generic = ok && id.Name == "id"
		}
		if sel.Sel.Name != "on" || len(call.Args) != 2 {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		name, _ := strconv.Unquote(literal.Value)
		if name == "SetOnFrontDisconnected" {
			if fn, ok := call.Args[1].(*ast.FuncLit); ok {
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					x, ok := n.(*ast.CallExpr)
					if !ok || len(x.Args) != 2 {
						return true
					}
					s, ok := x.Fun.(*ast.SelectorExpr)
					if !ok || s.Sel.Name != "abort" {
						return true
					}
					v, ok := x.Args[0].(*ast.BasicLit)
					if !ok {
						return true
					}
					kind, _ := strconv.Unquote(v.Value)
					disconnected = kind == "disconnected"
					return true
				})
			}
		}
		stage, needed := wanted[name]
		if !needed {
			return true
		}
		fn, ok := call.Args[1].(*ast.FuncLit)
		if !ok {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			x, ok := n.(*ast.CallExpr)
			if !ok || len(x.Args) != 5 {
				return true
			}
			s, ok := x.Fun.(*ast.SelectorExpr)
			if !ok || s.Sel.Name != "reply" {
				return true
			}
			a, aok := x.Args[0].(*ast.Ident)
			id, iok := x.Args[1].(*ast.Ident)
			last, lok := x.Args[4].(*ast.Ident)
			constants := map[string]string{"handshakeAuth": handshakeAuth, "handshakeLogin": handshakeLogin, "handshakeConfirm": handshakeConfirm}
			found[name] = aok && constants[a.Name] == stage && iok && id.Name == "id" && lok && last.Name == "last"
			return true
		})
		return true
	})
	for name := range wanted {
		if !found[name] {
			t.Errorf("握手回调编号或阶段丢失: %s", name)
		}
	}
	if !runner || !generic {
		t.Error("握手执行器或通用错误接线缺失")
	}
	if !disconnected {
		t.Error("前置断开未终止握手")
	}
	if !nativeCode {
		t.Error("握手原生返回码未传播")
	}
}
