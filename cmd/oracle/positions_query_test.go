package main

import (
	"errors"
	"go/ast"
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// TestFlattenStepNeverReportsClearedOnQueryError 钉住收尾在**持仓查询失败**时报错，不报「已清空」。
//
// ⚠️ 20260914 夜盘真撞上过：查询 40s 超时，上一版把失败当成「没有持仓」，打印「今仓已清空」并正常返回。
func TestFlattenStepNeverReportsClearedOnQueryError(t *testing.T) {
	timeout := errors.New("40s 内没有等到持仓查询的最后一条")
	if done, err := flattenStep(nil, timeout); err == nil || done {
		t.Errorf("⚠️ 查询失败时 done=%v err=%v —— 要报错、不许判清空", done, err)
	} else if !strings.Contains(err.Error(), "不知道今仓清没清") {
		t.Errorf("报错里要说清「不知道清没清」：%v", err)
	}
	// ⚠️ 反向：查询成功时照常判，否则一个「一律报错」的实现也能过上面那条。
	held := &def.CThostFtdcInvestorPositionField{Position: 1}
	if done, err := flattenStep(held, nil); err != nil || done {
		t.Errorf("有 1 手时 done=%v err=%v，要继续平", done, err)
	}
	if done, err := flattenStep(nil, nil); err != nil || !done {
		t.Errorf("查询成功而没有记录时 done=%v err=%v，要判清空", done, err)
	}
	if done, err := flattenStep(&def.CThostFtdcInvestorPositionField{Position: 0}, nil); err != nil || !done {
		t.Errorf("记录在而 Position=0 时 done=%v err=%v，要判清空", done, err)
	}
}

// TestOpenCostFromRefusesZeroOnQueryError 钉住开仓前的 OpenCost 查询失败时报错，不当成 0。
func TestOpenCostFromRefusesZeroOnQueryError(t *testing.T) {
	if v, err := openCostFrom(nil, errors.New("超时")); err == nil {
		t.Errorf("⚠️ 查询失败却返回了 %v —— 当成 0 会反解出两片之和那么大的价", v)
	}
	if v, err := openCostFrom(nil, nil); err != nil || v != 0 {
		t.Errorf("查询成功、没有今仓时要返回 0：得到 %v %v", v, err)
	}
	if v, err := openCostFrom(&def.CThostFtdcInvestorPositionField{OpenCost: 231855}, nil); err != nil || v != 231855 {
		t.Errorf("有今仓时要返回它的 OpenCost：得到 %v %v", v, err)
	}
}

// TestNoPositionQueryErrorIsSwallowed 钉住本包里**持仓查询的错误不被吞掉**：
//
//	不许再出现 mustPositions（把错误变成 nil 的那个辅助）
//	openCostOf / c.Positions 的错误返回值不许赋给 `_`
//	flattenLongToday 经 flattenStep 判
func TestNoPositionQueryErrorIsSwallowed(t *testing.T) {
	_, files := parsePkgMain(t)
	calls := 0
	for name, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.Ident:
				if v.Name == "mustPositions" {
					t.Errorf("⚠️ %s 里又出现了 mustPositions —— 它把查询失败变成 nil", name)
				}
			case *ast.AssignStmt:
				if len(v.Rhs) != 1 || len(v.Lhs) < 2 {
					return true
				}
				call, ok := v.Rhs[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				fn := ""
				switch x := call.Fun.(type) {
				case *ast.Ident:
					fn = x.Name
				case *ast.SelectorExpr:
					fn = x.Sel.Name
				}
				if fn != "openCostOf" && fn != "Positions" {
					return true
				}
				calls++
				if id, ok := v.Lhs[len(v.Lhs)-1].(*ast.Ident); ok && id.Name == "_" {
					t.Errorf("⚠️ %s：%s 的错误返回值被赋给了 _ —— 查询失败会被当成「没有持仓」", name, fn)
				}
			}
			return true
		})
	}
	if calls < 5 {
		t.Errorf("⚠️ 只找到 %d 处 openCostOf / Positions 的赋值（下界 5）—— 扫描范围不对", calls)
	}
	fn := findFunc(files["slice.go"], "flattenLongToday")
	used := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "flattenStep" {
				used = true
			}
		}
		return true
	})
	if !used {
		t.Error("⚠️ flattenLongToday 不经 flattenStep 判 —— 查询失败时的「不报清空」没接上")
	}
}
