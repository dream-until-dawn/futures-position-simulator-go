package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// TestNeedsCancel：报错（含超时）或一手没成交 ⇒ 走撤单分支；成交了才不撤。
func TestNeedsCancel(t *testing.T) {
	for _, c := range []struct {
		name string
		st   ctp.OrderState
		err  error
		want bool
	}{
		{"挂上了没成交（NoTradeQueueing）", ctp.OrderState{Status: def.THOST_FTDC_OST_NoTradeQueueing}, nil, true},
		{"超时拿不到结论", ctp.OrderState{}, errors.New("超时"), true},
		{"被拒", ctp.OrderState{Status: def.THOST_FTDC_OST_Canceled}, nil, true},
		{"全部成交", ctp.OrderState{Status: def.THOST_FTDC_OST_AllTraded, VolumeTraded: 1}, nil, false},
	} {
		if got := needsCancel(c.st, 1, c.err); got != c.want {
			t.Errorf("⚠️ %s：needsCancel = %v，应为 %v", c.name, got, c.want)
		}
	}
}

// TestNeedsCancelPartialFill：多手单部分成交（余量还在队列）也要撤。
func TestNeedsCancelPartialFill(t *testing.T) {
	if !needsCancel(ctp.OrderState{Status: def.THOST_FTDC_OST_PartTradedQueueing, VolumeTraded: 1}, 3, nil) {
		t.Error("⚠️ 3 手单只成交 1 手，余量还挂着 —— 应走撤单分支")
	}
	if needsCancel(ctp.OrderState{Status: def.THOST_FTDC_OST_AllTraded, VolumeTraded: 3}, 3, nil) {
		t.Error("⚠️ 3 手全成交不该撤")
	}
}

func TestLiveOnFilters(t *testing.T) {
	mk := func(inst string) *def.CThostFtdcOrderField {
		o := &def.CThostFtdcOrderField{}
		copy(o.InstrumentID[:], inst)
		return o
	}
	got := liveOn([]*def.CThostFtdcOrderField{mk("m2705"), mk("m2701"), nil, mk("m2705")}, "m2705")
	if len(got) != 2 {
		t.Errorf("⚠️ 挑出 %d 笔 m2705 的活委托，应为 2", len(got))
	}
}

// callsIn 列出某个函数里（含闭包）调用到的函数名 / 方法名。
func callsIn(t *testing.T, file, fn string) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn {
			continue
		}
		out := map[string]bool{}
		ast.Inspect(fd, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				switch f := call.Fun.(type) {
				case *ast.Ident:
					out[f.Name] = true
				case *ast.SelectorExpr:
					out[f.Sel.Name] = true
				}
			}
			return true
		})
		return out
	}
	t.Fatalf("⚠️ %s 里找不到 %s —— 改名了，本条守卫失效", file, fn)
	return nil
}

// TestImmediateFillPathsCancelOnMiss：要求立刻成交的几条路径不许直接调 Insert —— 一律走 insertOrCancel（没成交就撤、撤干净再返回）；
// insertOrCancel 自己必须扫一遍活委托。（评审 20260922：此前没成交的单会留在柜台上。）
func TestImmediateFillPathsCancelOnMiss(t *testing.T) {
	for _, c := range []struct{ file, fn string }{
		{"closefee_quotaext.go", "runCTPQuotaExt"},
		{"closefee_quotaext.go", "closeShortTodayOnly"},
		{"closeorder.go", "closeTodayOnly"},
	} {
		calls := callsIn(t, c.file, c.fn)
		if calls["Insert"] {
			t.Errorf("⚠️ %s 直接调了 Insert —— 没成交时那张单会留在柜台上，改走 insertOrCancel", c.fn)
		}
		if !calls["insertOrCancel"] {
			t.Errorf("⚠️ %s 没有走 insertOrCancel", c.fn)
		}
	}
	if !callsIn(t, "closefee_quotaext.go", "runCTPQuotaExt")["sweepLive"] {
		t.Error("⚠️ runCTPQuotaExt 的收尾没有扫活委托")
	}
	if !callsIn(t, "insertcancel.go", "insertOrCancel")["sweepLive"] {
		t.Error("⚠️ insertOrCancel 没成交时不扫活委托 —— 超时拿不到 ref 的那一笔撤不掉")
	}
}
