package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestRestingQuoteClockUsesNaturalDate 当前日盘同值，仍核生产入口传自然日而非交易日。
func TestRestingQuoteClockUsesNaturalDate(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "resting_price_rules.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "validateRestingQuote" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok || name.Name != "parseRestingQuoteClock" {
				return true
			}
			found++
			if len(call.Args) != 3 {
				t.Error("行情时刻未使用登记自然日")
				return true
			}
			natural, nok := call.Args[0].(*ast.Ident)
			day, dok := call.Args[1].(*ast.Ident)
			if !nok || natural.Name != "restingProbeDate" || !dok || day.Name != "day" {
				t.Error("行情时刻未使用登记自然日")
			}
			return true
		})
	}
	if found != 1 {
		t.Fatal("行情时刻解析未在生产入口接线", found)
	}
}
