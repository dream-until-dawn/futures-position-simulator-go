package ctp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestLoadedModuleGuardBeforeCreateAPI 检查实际加载后守卫接线；静态检查不冒充柜台验证。
func TestLoadedModuleGuardBeforeCreateAPI(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "client_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var load, verify, create token.Pos
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "initializeHandshake" {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "loadedNativeSDK" {
				verify = call.Pos()
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if sel.Sel.Name == "LoadDLL" {
					load = call.Pos()
				}
				if sel.Sel.Name == "call" && len(call.Args) > 0 {
					if lit, ok := call.Args[0].(*ast.BasicLit); ok {
						name, _ := strconv.Unquote(lit.Value)
						if name == "CreateApi" {
							create = call.Pos()
						}
					}
				}
			}
			return true
		})
	}
	if load == 0 || verify == 0 || create == 0 || load >= verify || verify >= create {
		t.Fatal("实际加载模块守卫未在CreateApi之前接线")
	}
}
