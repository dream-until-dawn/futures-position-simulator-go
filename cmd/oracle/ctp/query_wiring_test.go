package ctp

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestQueryCallbacksKeepRequestIdentity 检查实际 Windows 回调接线，补足假传输测试不经过注册函数的边界。
// 这里只核对源码的编号、错误与批次归属，不能证明 DLL/柜台实际如何回报。
func TestQueryCallbacksKeepRequestIdentity(t *testing.T) {
	fields := map[string]string{
		"SetOnRspQryTradingAccount":           "accountRows",
		"SetOnRspQryInstrumentCommissionRate": "commissionRows",
		"SetOnRspQryInstrument":               "instrumentRows",
		"SetOnRspQryDepthMarketData":          "marketRows",
		"SetOnRspQryBrokerTradingParams":      "brokerRows",
		"SetOnRspQryInvestorPosition":         "positionRows",
		"SetOnRspQryOrder":                    "orderRows",
		"SetOnRspQryTrade":                    "tradeRows",
		"SetOnRspQrySettlementInfo":           "settlementRows",
	}
	callbacks := make(map[string]*ast.FuncLit)
	set := token.NewFileSet()
	for _, path := range []string{"client_windows.go", "handshake_callbacks_windows.go", "query_windows.go", "marketdata_windows.go"} {
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "on" {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			callback, ok := call.Args[1].(*ast.FuncLit)
			if !ok {
				return true
			}
			if _, exists := callbacks[name]; exists {
				t.Fatalf("回调重复注册：%s", name)
			}
			callbacks[name] = callback
			return true
		})
	}
	expression := func(expr ast.Node) string {
		var out bytes.Buffer
		if err := format.Node(&out, set, expr); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	hasReceive := func(callback *ast.FuncLit, field string, want []string) bool {
		found := false
		if callback == nil {
			return false
		}
		ast.Inspect(callback.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != len(want) || expression(call.Fun) != "c."+field+".receive" {
				return true
			}
			for i, arg := range call.Args {
				if expression(arg) != want[i] {
					return true
				}
			}
			found = true
			return true
		})
		return found
	}
	for name, field := range fields {
		if !hasReceive(callbacks[name], field, []string{"id", "row", "errOf(info)", "last"}) {
			t.Errorf("查询回调丢失编号、错误或批次接线：%s → %s", name, field)
		}
		if !hasReceive(callbacks["SetOnRspError"], field, []string{"id", "nil", "err", "true"}) {
			t.Errorf("通用错误遗漏查询批次：%s", field)
		}
	}
}
