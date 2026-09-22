package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
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

// TestAfterCancel：扫单之后按本地簿判「干净 / 有仓 / 没有结论」。停在 'a' 上的那一笔不许判干净。
func TestAfterCancel(t *testing.T) {
	for _, c := range []struct {
		name  string
		st    ctp.OrderState
		known bool
		clean bool
	}{
		{"已撤", ctp.OrderState{Status: def.THOST_FTDC_OST_Canceled}, true, true},
		{"未成交不在队列", ctp.OrderState{Status: def.THOST_FTDC_OST_NoTradeNotQueueing}, true, true},
		{"停在 'a'（已提交）", ctp.OrderState{Status: 'a'}, true, false},
		{"本地簿里没有", ctp.OrderState{}, false, false},
		{"还挂着", ctp.OrderState{Status: def.THOST_FTDC_OST_NoTradeQueueing}, true, false},
		{"超时之后成交了", ctp.OrderState{Status: def.THOST_FTDC_OST_AllTraded, VolumeTraded: 1}, true, false},
		{"撤单前部分成交", ctp.OrderState{Status: def.THOST_FTDC_OST_PartTradedNotQueueing, VolumeTraded: 1}, true, false},
	} {
		clean, why := afterCancel(c.st, c.known)
		if clean != c.clean {
			t.Errorf("⚠️ %s：clean = %v，应为 %v（%s）", c.name, clean, c.clean, why)
		}
		if !clean && why == "" {
			t.Errorf("⚠️ %s：判不干净却没说为什么", c.name)
		}
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
	ic := callsIn(t, "insertcancel.go", "insertOrCancel")
	if !ic["sweepLive"] {
		t.Error("⚠️ insertOrCancel 没成交时不扫活委托 —— 超时拿不到 ref 的那一笔撤不掉")
	}
	if !ic["afterCancel"] {
		t.Error("⚠️ insertOrCancel 扫完不看本地簿 —— 停在 'a' 上的那一笔会被当成干净")
	}
}

// directInsertAllowed 是**允许直接调 c.Insert** 的函数：insertOrCancel 自己，以及有意挂单、自己负责撤单的那几条
// （挂得上成不了、报单被拒、重复报单、冻结往返、价格优先……它们要的正是「挂着」，撤单写在各自的流程里）。
//
// ⚠️ 这张表以外的函数一律走 insertOrCancel —— 要求立刻成交的委托没成交就撤、撤干净再返回（评审 20260922）。
// 新写一个工具直接调 Insert，本条会红：那时要么改走 insertOrCancel，要么把它加进来并写清它自己怎么撤。
var directInsertAllowed = map[string]string{
	"insertOrCancel":      "它就是那道撤单",
	"runControlDeclaring": "controlVerdict 判出要撤时撤",
	"runCTPDup":           "重复报单：格子挂着是判据，收尾撤",
	"runCTPFee":           "冻结手续费：每个价位挂上读完就撤",
	"runCTPOrder":         "P3 冻结往返：挂上、拍截面、撤",
	"runCTPPairs":         "成对报单：挂着是判据，收尾撤",
	"runCTPPriority":      "拒单优先级：盘中休息报不进，被拒或撤",
	"runCTPReject":        "拒单码：被拒是判据，挂上了就撤",
	"openOneLotResting":   "挂低一跳等成交，等不到就撤",
}

// TestDirectInsertOnlyInAllowedFuncs 扫全包：直接调 Insert 的函数只能是白名单里的。
func TestDirectInsertOnlyInAllowedFuncs(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Insert" {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "c" {
						seen[fd.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	var bad []string
	for fn := range seen {
		if _, ok := directInsertAllowed[fn]; !ok {
			bad = append(bad, fn)
		}
	}
	sort.Strings(bad)
	for _, fn := range bad {
		t.Errorf("⚠️ %s 直接调了 c.Insert —— 要求立刻成交的委托要走 insertOrCancel（没成交就撤）；有意挂单的就加进 directInsertAllowed 并写清怎么撤", fn)
	}
	for fn := range directInsertAllowed {
		if !seen[fn] {
			t.Errorf("⚠️ 白名单里的 %s 已经不直接调 Insert 了 —— 删掉那一条，别让豁免烂在原地", fn)
		}
	}
	if len(seen) < 5 {
		t.Fatalf("⚠️ 只扫到 %d 个调 Insert 的函数 —— 扫描写错了，本条在空转", len(seen))
	}
}
