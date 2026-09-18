package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// pastUndatedClose 是 §13 #21 / #23 已有的全部裸平观测（事前登记之前就有的；新候选必须先解释它们）。
var pastUndatedClose = []struct {
	name string
	step quotaStep
	got  feeTier
}{
	{"DCE 20260915 今1昨1 裸平1 消耗昨仓", quotaStep{TodayBefore: 1, YdBefore: 1, OpenedToday: 1, ConsumedYd: true}, tierToday},
	{"DCE 20260917 E1 今0昨2 裸平1", quotaStep{TodayBefore: 0, YdBefore: 2, ConsumedYd: true}, tierYd},
	{"DCE 20260917 E2 今0昨1 显式平昨被改写成平仓", quotaStep{TodayBefore: 0, YdBefore: 1, ConsumedYd: true}, tierYd},
	{"CZCE 20260918 X1 今0昨2 裸平1", quotaStep{TodayBefore: 0, YdBefore: 2, ConsumedYd: true}, tierYd},
	{"CZCE 20260918 X2 今1昨1 裸平1 消耗昨仓", quotaStep{TodayBefore: 1, YdBefore: 1, OpenedToday: 1, ConsumedYd: true}, tierToday},
	{"CZCE 20260918 X0 今1昨0 裸平1 消耗今仓", quotaStep{TodayBefore: 1, YdBefore: 0, OpenedToday: 1, TodayCharged: 1}, tierYd},
}

// TestQuotaCandidatesRetrodictPastObservations：登记的候选要先对得上已有观测，否则登记的是一个早就死了的候选。
// (a) 只准死在郑商所 X0 那一笔（它是大商所收敛的结论，在郑商所上被否）；(f)、(k) 一笔都不准错。
func TestQuotaCandidatesRetrodictPastObservations(t *testing.T) {
	wantMiss := map[string][]string{"a": {"CZCE 20260918 X0 今1昨0 裸平1 消耗今仓"}, "f": nil, "k": nil}
	for _, c := range quotaCandidates {
		var miss []string
		for _, o := range pastUndatedClose {
			if c.Tier(o.step) != o.got {
				miss = append(miss, o.name)
			}
		}
		want, ok := wantMiss[c.Name]
		if !ok {
			t.Errorf("⚠️ 候选 (%s) 没在本条的期望表里 —— 新候选要先写明它在已有观测上错在哪", c.Name)
			continue
		}
		if !reflect.DeepEqual(miss, want) {
			t.Errorf("(%s) 在已有观测上错的是 %v，期望 %v", c.Name, miss, want)
		}
	}
	if len(pastUndatedClose) < 6 {
		t.Fatal("⚠️ 已有观测少于 6 笔 —— 表被删了，本条在空转")
	}
}

// TestUnflooredGIsRefutedByX2 钉住划掉 (g) 的理由：「当日开仓量 − 当日已平仓量（不论哪档）」不设下限时，
// 郑商所 X1 平在开仓**之前**（成交序号 10543 < 10968），X2 时额度 = 1 − 1 = 0 ⇒ 预言平昨，实测平今。
// 设下限 0 时每一笔的扣减都等于 min(平仓量, 额度)，与 (f) 处处同值 —— 不是另一个候选。
func TestUnflooredGIsRefutedByX2(t *testing.T) {
	g := func(opened, closed int) feeTier {
		if opened-closed > 0 {
			return tierToday
		}
		return tierYd
	}
	// X2 之前：当日开 1、当日已平 1（X1）
	if g(1, 1) != tierYd {
		t.Fatal("本条的 (g) 写错了")
	}
	if tierToday != pastUndatedClose[4].got {
		t.Fatal("⚠️ 表里 X2 不再是平今档 —— 划掉 (g) 的理由要重看")
	}
	// 有下限的 (g) 与 (f) 在任意序列上同值：逐笔扣 min(1, 额度) 与「按平今档收过几手」是同一个数。
	for opened := 0; opened <= 3; opened++ {
		quota, charged := opened, 0
		for i := 0; i < 5; i++ {
			fT := quotaCandidates[1].Tier(quotaStep{OpenedToday: opened, TodayCharged: charged})
			gT := tierYd
			if quota > 0 {
				gT = tierToday
			}
			if fT != gT {
				t.Fatalf("开 %d 第 %d 笔：(f) %s ≠ 有下限的 (g) %s", opened, i+1, fT, gT)
			}
			if fT == tierToday {
				charged++
			}
			quota = max(0, quota-1)
		}
	}
}

// TestQuotaExperimentDiscriminates：预期消耗顺序（昨、今、今）下三个候选的三笔预言两两不同。
func TestQuotaExperimentDiscriminates(t *testing.T) {
	steps := quotaPlan(quotaExpectedConsumption)
	seen := map[[3]feeTier]string{}
	for _, c := range quotaCandidates {
		p := quotaPredict(c, steps)
		if other, dup := seen[p]; dup {
			t.Errorf("⚠️ (%s) 与 (%s) 预言相同 %v —— 这个实验分不开它们", c.Name, other, p)
		}
		seen[p] = c.Name
	}
	if len(seen) < 3 {
		t.Fatalf("⚠️ 只有 %d 种预言", len(seen))
	}
}

// TestQuotaCandidatesMatchPreRegistration 把渲染出的预言表与 state.md 的事前登记逐行比 —— 看到结果之后改预言会红。
func TestQuotaCandidatesMatchPreRegistration(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "state.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	if !strings.Contains(doc, "事前登记：§13 #23") {
		t.Fatal("⚠️ 交接页里找不到 #23 的事前登记 —— 本条在空转")
	}
	rows := quotaRegistrationRows("0.1", "0.2", "6", "2")
	if len(rows) != len(quotaCandidates) || len(rows) < 3 {
		t.Fatalf("渲染出 %d 行", len(rows))
	}
	for _, r := range rows {
		if !strings.Contains(doc, r) {
			t.Errorf("⚠️ state.md 的登记表里没有这一行：\n%s", r)
		}
	}
}

// TestQuotaVerdict：按某个候选的预言喂进去只活它；落在两档之外谁都不活；两档同价拒判。
func TestQuotaVerdict(t *testing.T) {
	rt, ry := decimal.RequireFromString("6"), decimal.RequireFromString("2")
	steps := quotaPlan(quotaExpectedConsumption)
	for _, c := range quotaCandidates {
		p := quotaPredict(c, steps)
		var deltas []decimal.Decimal
		for _, x := range p {
			if x == tierToday {
				deltas = append(deltas, rt)
			} else {
				deltas = append(deltas, ry)
			}
		}
		alive, _, err := quotaVerdict(steps[:], deltas, rt, ry)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(alive, []string{c.Name}) {
			t.Errorf("喂 (%s) 的预言 %v，活下来的是 %v", c.Name, p, alive)
		}
	}
	alive, lines, _ := quotaVerdict(steps[:], []decimal.Decimal{rt, rt, decimal.RequireFromString("4")}, rt, ry)
	if len(alive) != 0 || !strings.Contains(strings.Join(lines, "\n"), "谁都没预言到") {
		t.Errorf("两档之外的增量：活 %v", alive)
	}
	if _, _, err := quotaVerdict(steps[:], []decimal.Decimal{rt, rt, rt}, rt, rt); err == nil {
		t.Error("两档同价应当拒判")
	}
	if _, _, err := quotaVerdict(steps[:2], []decimal.Decimal{rt}, rt, ry); err == nil {
		t.Error("笔数与增量数对不上应当拒判")
	}
}

// TestQuotaVerdictUsesObservedCharges：第二笔起的「已收平今」按实测累计，不按候选自己的预言累计。
// 构造：第一笔实测收平昨（三个候选都预言平今 ⇒ 都错），此后 (f) 在实测状态下（已收平今 0）第三笔仍预言平今。
func TestQuotaVerdictUsesObservedCharges(t *testing.T) {
	rt, ry := decimal.RequireFromString("6"), decimal.RequireFromString("2")
	steps := quotaPlan(quotaExpectedConsumption)
	_, lines, err := quotaVerdict(steps[:], []decimal.Decimal{ry, rt, rt}, rt, ry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lines[2], "已收平今1") {
		t.Errorf("第三笔之前实测只收过 1 笔平今，行里是：%s", lines[2])
	}
	if !strings.Contains(lines[2], "f→平今档") {
		t.Errorf("实测状态下 (f) 第三笔额度 2−1=1 应预言平今：%s", lines[2])
	}
}

func TestQuotaPremise(t *testing.T) {
	if err := quotaPremise(longSides{Yd: 1}); err != nil {
		t.Errorf("今0昨1未开应当放行：%v", err)
	}
	for _, s := range []longSides{{Yd: 2}, {Yd: 0}, {Today: 1, Yd: 1}, {Yd: 1, OpenedToday: 1}} {
		if quotaPremise(s) == nil {
			t.Errorf("%+v 应当拒跑", s)
		}
	}
	for _, ex := range []string{"SHFE", "INE", "GFEX", "CFFEX"} {
		if quotaRegistered(ex) == nil {
			t.Errorf("%s 没登记，应当拒跑", ex)
		}
	}
}

// TestQuotaRegisteredRunsBeforeConnect：登记检查排在连柜台之前（与 TestCloseFeeRegisteredRunsBeforeConnect 同理）。
func TestQuotaRegisteredRunsBeforeConnect(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "closefee_quota.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "runCTPQuota" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPQuota —— 它改名了，本条守卫失效")
	}
	guard, connect := token.NoPos, token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == "quotaRegistered" && guard == token.NoPos {
				guard = call.Pos()
			}
		case *ast.SelectorExpr:
			if fun.Sel.Name == "Connect" && connect == token.NoPos {
				connect = call.Pos()
			}
		}
		return true
	})
	if guard == token.NoPos || connect == token.NoPos {
		t.Fatalf("⚠️ 找不到 quotaRegistered（%v）或 Connect（%v）", guard, connect)
	}
	if guard > connect {
		t.Errorf("⚠️ quotaRegistered 排在 Connect 之后 —— 连上柜台之后再拒，已经晚了")
	}
}

func TestClosePieceVerdict(t *testing.T) {
	cases := []struct {
		b, a longSides
		want closeOrderKind
	}{
		{longSides{Today: 2, Yd: 1}, longSides{Today: 2, Yd: 0}, coConsumedYesterday},
		{longSides{Today: 2, Yd: 0}, longSides{Today: 1, Yd: 0}, coConsumedToday},
		{longSides{Today: 2, Yd: 1}, longSides{Today: 1, Yd: 0}, coNotOneLot},
		{longSides{Today: 2, Yd: 1}, longSides{Today: 2, Yd: 1}, coNotOneLot},
	}
	for _, c := range cases {
		if got := closeOrderPieceVerdict(c.b, c.a); got != c.want {
			t.Errorf("%+v→%+v：%d，期望 %d", c.b, c.a, got, c.want)
		}
	}
}
