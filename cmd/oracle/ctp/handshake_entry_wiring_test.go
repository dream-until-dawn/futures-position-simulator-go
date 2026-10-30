package ctp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestHandshakeEntryProductionWiring 辅助函数被测试不等于实际初始化使用它。
func TestHandshakeEntryProductionWiring(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "client_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	registered, published := 0, 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "registerHandshakeCallbacks" && fn.Name.Name == "initializeHandshake" {
				registered++
			}
			if sel.Sel.Name == "logEntries" && fn.Name.Name == "Connect" {
				published++
			}
			return true
		})
	}
	if registered != 1 || published != 1 {
		t.Fatal("入口日志未在真实初始化和Connect返回接线", registered, published)
	}
	f, err = parser.ParseFile(token.NewFileSet(), "handshake_callbacks_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	wants := map[string]string{"SetOnFrontConnected": "handshakeFront", "SetOnRspAuthenticate": "handshakeAuth", "SetOnRspUserLogin": "handshakeLogin", "SetOnRspSettlementInfoConfirm": "handshakeConfirm", "SetOnRspError": "general-error", "SetOnFrontDisconnected": "disconnected"}
	seen := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "on" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		name, _ := strconv.Unquote(lit.Value)
		want, ok := wants[name]
		if !ok {
			return true
		}
		fn, ok := call.Args[1].(*ast.FuncLit)
		if !ok || len(fn.Body.List) == 0 {
			t.Error("生产回调缺失入口日志", name)
			return true
		}
		statement, ok := fn.Body.List[0].(*ast.ExprStmt)
		if !ok {
			t.Error("生产回调首步不是入口日志", name)
			return true
		}
		record, ok := statement.X.(*ast.CallExpr)
		if !ok || len(record.Args) != 5 {
			t.Error("生产回调首步不是入口日志", name)
			return true
		}
		method, ok := record.Fun.(*ast.SelectorExpr)
		if !ok || method.Sel.Name != "recordEntry" {
			t.Error("生产回调首步不是入口日志", name)
			return true
		}
		stage := ""
		switch value := record.Args[0].(type) {
		case *ast.Ident:
			stage = value.Name
		case *ast.BasicLit:
			stage, _ = strconv.Unquote(value.Value)
		}
		if stage != want {
			t.Error("生产入口阶段串位", name, stage, want)
		}
		seen[name] = true
		return true
	})
	if len(seen) != len(wants) {
		t.Fatal("生产回调入口日志缺项", seen)
	}
}
