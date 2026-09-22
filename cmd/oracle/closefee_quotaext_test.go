package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/shopspring/decimal"
)

// TestExtReadingsMatchPreRegistration：extReadings 与 state.md 登记块（「事前登记：F11 两条外推」）的两张表逐行一致。
//
// 登记表按声明费率展开：大商所 平今 0.1 / 平昨 0.2，郑商所 平今 6 / 平昨 2。
func TestExtReadingsMatchPreRegistration(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "state.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	i := strings.Index(doc, "事前登记：F11 两条外推")
	if i < 0 {
		t.Fatal("⚠️ state.md 里找不到「事前登记：F11 两条外推」登记块 —— 登记块改名或被挪走，本条失效")
	}
	block := doc[i:]
	if j := strings.Index(block[1:], "\n## "); j >= 0 {
		block = block[:j+1]
	}
	rate := map[feeTier][2]string{tierToday: {"0.1", "6"}, tierYd: {"0.2", "2"}}
	n := 0
	for _, c := range []extCase{extExplicit, extOpposite} {
		for _, r := range extReadings(c) {
			var row string
			for _, line := range strings.Split(block, "\n") {
				if strings.HasPrefix(line, "| **"+r.Name+"**") {
					row = line
				}
			}
			if row == "" {
				t.Errorf("⚠️ 登记块里没有读法 %s 那一行", r.Name)
				continue
			}
			rt := rate[r.Tier]
			if want := "| " + r.Tier.String() + " | " + rt[0] + " | " + rt[1] + " |"; !strings.HasSuffix(row, want) {
				t.Errorf("⚠️ %v 读法 %s：登记行 %q，代码预言 %s（应以 %q 结尾）", c, r.Name, row, r.Tier, want)
			}
			n++
		}
	}
	if n != 4 {
		t.Fatalf("⚠️ 比了 %d 种读法，应为 4（B、C 各两种）", n)
	}
}

func TestExtVerdict(t *testing.T) {
	d := decimal.RequireFromString
	for _, c := range []struct {
		cs            extCase
		delta, td, yd string
		want          string
	}{
		{extExplicit, "0.2", "0.1", "0.2", "扣"},
		{extExplicit, "0.1", "0.1", "0.2", "不扣"},
		{extExplicit, "2", "6", "2", "扣"},
		{extExplicit, "6", "6", "2", "不扣"},
		{extOpposite, "0.2", "0.1", "0.2", "分方向"},
		{extOpposite, "0.1", "0.1", "0.2", "不分方向"},
		{extOpposite, "6", "6", "2", "不分方向"},
		{extOpposite, "0.3", "0.1", "0.2", ""},
	} {
		alive, err := extVerdict(c.cs, d(c.delta), d(c.td), d(c.yd))
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(alive, ",")
		if got != c.want {
			t.Errorf("%v 收 %s（今 %s / 昨 %s）：活着 %q，应为 %q", c.cs, c.delta, c.td, c.yd, got, c.want)
		}
	}
	if _, err := extVerdict(extExplicit, d("0.1"), d("0.1"), d("0.1")); err == nil {
		t.Error("⚠️ 平今平昨同费率时应拒判")
	}
	if _, err := extVerdict(extCase(0), d("0.1"), d("0.1"), d("0.2")); err == nil {
		t.Error("⚠️ 没登记的外推应拒判")
	}
}

func TestExtPremise(t *testing.T) {
	ok := longSides{Yd: 1}
	if err := extPremise(ok, longSides{}); err != nil {
		t.Errorf("多头 今 0 昨 1 开过 0、空头 0 应通过：%v", err)
	}
	for name, c := range map[string][2]longSides{
		"多头有今仓":  {{Today: 1, Yd: 1}, {}},
		"多头昨 2":  {{Yd: 2}, {}},
		"多头当日开过": {{Yd: 1, OpenedToday: 1}, {}},
		"多头没有昨仓": {{}, {}},
		"空头有今仓":  {ok, {Today: 1}},
		"空头有昨仓":  {ok, {Yd: 1}},
		"空头当日开过": {ok, {OpenedToday: 1}},
	} {
		if err := extPremise(c[0], c[1]); err == nil {
			t.Errorf("⚠️ %s：前提应不成立", name)
		}
	}
}

func TestExtRegistered(t *testing.T) {
	for _, ex := range []string{"DCE", "CZCE"} {
		for _, cs := range []extCase{extExplicit, extOpposite} {
			if err := extRegistered(ex, cs); err != nil {
				t.Errorf("%s %v 应已登记：%v", ex, cs, err)
			}
		}
	}
	if err := extRegistered("SHFE", extExplicit); err == nil {
		t.Error("⚠️ 上期所没登记，应拒跑")
	}
	if err := extRegistered("GFEX", extOpposite); err == nil {
		t.Error("⚠️ 广期所没登记，应拒跑")
	}
	if err := extRegistered("DCE", extCase(0)); err == nil {
		t.Error("⚠️ 没给 -case 应拒跑")
	}
}

// TestExtRequestFlags：三个新委托的方向与开平标志。判别那一笔（B ④、C ③）用 genericCloseReq —— 它的标志由 TestGenericCloseUsesTheGenericFlag 守。
func TestExtRequestFlags(t *testing.T) {
	if r := explicitCloseTodayReq("DCE", "m2701", 1); r.Direction != def.THOST_FTDC_D_Sell || r.Offset != def.THOST_FTDC_OF_CloseToday || r.Volume != 1 {
		t.Errorf("⚠️ 显式平今：方向 %c 开平 %c 手数 %d，应为 卖 / 平今 / 1", r.Direction, r.Offset, r.Volume)
	}
	if r := shortOpenReq("DCE", "m2705", 1); r.Direction != def.THOST_FTDC_D_Sell || r.Offset != def.THOST_FTDC_OF_Open || r.Volume != 1 {
		t.Errorf("⚠️ 卖开：方向 %c 开平 %c 手数 %d，应为 卖 / 开 / 1", r.Direction, r.Offset, r.Volume)
	}
	if r := shortCloseTodayReq("DCE", "m2705", 1); r.Direction != def.THOST_FTDC_D_Buy || r.Offset != def.THOST_FTDC_OF_CloseToday || r.Volume != 1 {
		t.Errorf("⚠️ 买平今：方向 %c 开平 %c 手数 %d，应为 买 / 平今 / 1", r.Direction, r.Offset, r.Volume)
	}
}

// TestSidesOfSplitsDirections：多头与空头的记录各归各的 —— C 的前提与判定都要分开读两个方向。
func TestSidesOfSplitsDirections(t *testing.T) {
	rec := func(dir def.TThostFtdcPosiDirectionType, position, today, opened int) *def.CThostFtdcInvestorPositionField {
		p := &def.CThostFtdcInvestorPositionField{
			PosiDirection: dir,
			PositionDate:  def.THOST_FTDC_PSD_Today,
			Position:      def.TThostFtdcVolumeType(position),
			TodayPosition: def.TThostFtdcVolumeType(today),
			OpenVolume:    def.TThostFtdcVolumeType(opened),
		}
		copy(p.InstrumentID[:], "m2705")
		return p
	}
	pos := map[string]*def.CThostFtdcInvestorPositionField{
		"l": rec(def.THOST_FTDC_PD_Long, 1, 0, 0),
		"s": rec(def.THOST_FTDC_PD_Short, 1, 1, 1),
	}
	if l := longSidesOf(pos, "m2705"); l.Today != 0 || l.Yd != 1 || l.OpenedToday != 0 {
		t.Errorf("⚠️ 多头读成 今 %d / 昨 %d / 开过 %d，应为 0 / 1 / 0", l.Today, l.Yd, l.OpenedToday)
	}
	if s := shortSidesOf(pos, "m2705"); s.Today != 1 || s.Yd != 0 || s.OpenedToday != 1 {
		t.Errorf("⚠️ 空头读成 今 %d / 昨 %d / 开过 %d，应为 1 / 0 / 1", s.Today, s.Yd, s.OpenedToday)
	}
}

// TestExtRegisteredRunsBeforeConnect：runCTPQuotaExt 里 extRegistered 排在 Connect 之前 —— 没登记的一笔都不许下。
func TestExtRegisteredRunsBeforeConnect(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "closefee_quotaext.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "runCTPQuotaExt" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPQuotaExt —— 它改名了，本条守卫失效")
	}
	guard, connect := token.NoPos, token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name == "extRegistered" && guard == token.NoPos {
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
		t.Fatalf("⚠️ 找不到 extRegistered（%v）或 Connect（%v）调用", guard, connect)
	}
	if guard > connect {
		t.Error("⚠️ extRegistered 排在 Connect 之后 —— 没登记的交易所也会先连柜台")
	}
}
