package ctp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestNativeHandshakeLoadGuard 核验生产初始化确实先查ABI再加载/创建原生对象，不能只测孤立校验函数。
func TestNativeHandshakeLoadGuard(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "client_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var guard, load, create token.Pos
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
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "validateNativeHandshakeDLLs" {
				guard = call.Pos()
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
	if guard == 0 || load == 0 || create == 0 || guard >= load || load >= create {
		t.Fatal("原生握手ABI校验未在加载和创建之前接线")
	}
}
