package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
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
	d := decimal.RequireFromString
	dceT, dceY, czceT, czceY := d("0.1"), d("0.2"), d("6"), d("2")
	n := 0
	for _, c := range []extCase{extExplicit, extOpposite, extMulti} {
		block := block
		if c == extMulti {
			// A 的登记块单独一段（「事前登记：F11 第三条外推 A」）
			k := strings.Index(doc, "事前登记：F11 第三条外推 A")
			if k < 0 {
				t.Fatal("⚠️ state.md 里找不到「事前登记：F11 第三条外推 A」登记块")
			}
			block = doc[k:]
			if j := strings.Index(block[1:], "\n## "); j >= 0 {
				block = block[:j+1]
			}
		}
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
			want := " | " + r.fee(dceT, dceY).String() + " | " + r.fee(czceT, czceY).String() + " |"
			if r.Lots == 1 {
				want = "| " + r.tiers() + want
			}
			if !strings.HasSuffix(row, want) {
				t.Errorf("⚠️ %v 读法 %s：登记行 %q，代码预言 %s（应以 %q 结尾）", c, r.Name, row, r.tiers(), want)
			}
			n++
		}
	}
	if n != 7 {
		t.Fatalf("⚠️ 比了 %d 种读法，应为 7（B、C 各两种，A 三种）", n)
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
		{extMulti, "0.3", "0.1", "0.2", "按额度拆"},
		{extMulti, "0.2", "0.1", "0.2", "全今"},
		{extMulti, "0.4", "0.1", "0.2", "全昨"},
		{extMulti, "8", "6", "2", "按额度拆"},
		{extMulti, "12", "6", "2", "全今"},
		{extMulti, "4", "6", "2", "全昨"},
		{extMulti, "0.1", "0.1", "0.2", ""},
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
		for _, cs := range []extCase{extExplicit, extOpposite, extMulti, extRewrite} {
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
	if err := extRegistered("DCE", extCase(99)); err == nil {
		t.Error("⚠️ 认不得的 -case 应拒跑")
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
	if r := multiCloseReq("DCE", "m2703", 1); r.Direction != def.THOST_FTDC_D_Sell || r.Offset != def.THOST_FTDC_OF_Close || r.Volume != 2 {
		t.Errorf("⚠️ A 的判别那一笔：方向 %c 开平 %c 手数 %d，应为 卖 / 通用平仓 / 2", r.Direction, r.Offset, r.Volume)
	}
	if r := shortOpenReq("DCE", "m2705", 1); r.Direction != def.THOST_FTDC_D_Sell || r.Offset != def.THOST_FTDC_OF_Open || r.Volume != 1 {
		t.Errorf("⚠️ 卖开：方向 %c 开平 %c 手数 %d，应为 卖 / 开 / 1", r.Direction, r.Offset, r.Volume)
	}
	if r := shortCloseTodayReq("DCE", "m2705", 1); r.Direction != def.THOST_FTDC_D_Buy || r.Offset != def.THOST_FTDC_OF_CloseToday || r.Volume != 1 {
		t.Errorf("⚠️ 买平今：方向 %c 开平 %c 手数 %d，应为 买 / 平今 / 1", r.Direction, r.Offset, r.Volume)
	}
}

// TestMultiRecordVerdict：③ 新增的卖平记录要恰好一条 2 手；拆成两条 1 手 ⇒ 不判。
func TestMultiRecordVerdict(t *testing.T) {
	if err := multiRecordVerdict(0, 0, 1, 2); err != nil {
		t.Errorf("一条 2 手应判：%v", err)
	}
	for name, c := range map[string][4]int{
		"拆成两条 1 手": {0, 0, 2, 2},
		"只成 1 手":   {0, 0, 1, 1},
		"没有新记录":    {3, 3, 3, 3},
	} {
		if err := multiRecordVerdict(c[0], c[1], c[2], c[3]); err == nil {
			t.Errorf("⚠️ %s：应判「A 不判」", name)
		}
	}
}

func TestCloseRecordsOn(t *testing.T) {
	mk := func(inst string, dir def.TThostFtdcDirectionType, off def.TThostFtdcOffsetFlagType, vol int) *def.CThostFtdcTradeField {
		tr := &def.CThostFtdcTradeField{Direction: dir, OffsetFlag: off, Volume: def.TThostFtdcVolumeType(vol)}
		copy(tr.InstrumentID[:], inst)
		return tr
	}
	n, lots := closeRecordsOn([]*def.CThostFtdcTradeField{
		mk("m2703", def.THOST_FTDC_D_Sell, def.THOST_FTDC_OF_Close, 2),
		mk("m2703", def.THOST_FTDC_D_Buy, def.THOST_FTDC_OF_Open, 1),   // 开仓不算
		mk("m2703", def.THOST_FTDC_D_Sell, def.THOST_FTDC_OF_Open, 1),  // 卖开不算
		mk("m2701", def.THOST_FTDC_D_Sell, def.THOST_FTDC_OF_Close, 1), // 别的合约不算
		mk("m2703", def.THOST_FTDC_D_Sell, def.THOST_FTDC_OF_CloseToday, 1),
		nil,
	}, "m2703")
	if n != 2 || lots != 3 {
		t.Errorf("⚠️ 数出 %d 条 / %d 手，应为 2 条 / 3 手", n, lots)
	}
}

// TestExtDeferSweepsBeforeClosing：runCTPQuotaExt 的收尾先撤挂单再平仓（挂着的平仓单冻住可平量）。
func TestExtDeferSweepsBeforeClosing(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "closefee_quotaext.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var d *ast.DeferStmt
	ast.Inspect(f, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name != "runCTPQuotaExt" {
			return false
		}
		if ds, ok := n.(*ast.DeferStmt); ok && d == nil {
			if _, ok := ds.Call.Fun.(*ast.FuncLit); ok {
				d = ds
			}
		}
		return true
	})
	if d == nil {
		t.Fatal("⚠️ runCTPQuotaExt 里找不到收尾的 defer func —— 本条守卫失效")
	}
	var order []string
	ast.Inspect(d, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "sweepLive", "closeTodayOnly", "closeShortTodayOnly":
					order = append(order, id.Name)
				}
			}
		}
		return true
	})
	want := []string{"sweepLive", "closeTodayOnly", "closeShortTodayOnly", "sweepLive"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("⚠️ 收尾顺序 %v，应为 %v —— 先撤挂单释放冻结再平，最后再扫一遍", order, want)
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

// TestRewriteVerdict 逐格钉住 §13 #25 复现的判定（登记块「事前登记：§13 #25 复现」在 docs/state.md）。
//
//	③ 消耗昨仓 ⇒ 改写说活；④ 收平昨档 ⇒ 改写说仍活、标志说（预言平今档）被否
//	③ 消耗今仓 ⇒ 标志说活；④ 被拒 ⇒ 标志说仍活（可平今 0）、改写说（预言成交）被否
func TestRewriteVerdict(t *testing.T) {
	d := decimal.RequireFromString
	for _, c := range []struct {
		name          string
		consumedYd    bool
		rejected      bool
		delta, td, yd string
		want          string
	}{
		{"大商所 ③ 昨、④ 平昨档 ⇒ 改写说", true, false, "0.2", "0.1", "0.2", "改写说"},
		{"郑商所 ③ 昨、④ 平昨档 ⇒ 改写说", true, false, "2", "6", "2", "改写说"},
		{"③ 昨、④ 平今档 ⇒ 两说都被否（③ 与 ④ 不同向）", true, false, "0.1", "0.1", "0.2", ""},
		{"③ 今、④ 被拒 ⇒ 标志说", false, true, "0", "0.1", "0.2", "标志说"},
		{"③ 今、④ 平昨档成交 ⇒ 两说都被否", false, false, "0.2", "0.1", "0.2", ""},
		{"③ 昨、④ 被拒 ⇒ 两说都被否", true, true, "0", "0.1", "0.2", ""},
		{"③ 昨、④ 两档之外 ⇒ 两说都被否", true, false, "0.3", "0.1", "0.2", ""},
	} {
		alive, why := rewriteVerdict(c.consumedYd, c.rejected, d(c.delta), d(c.td), d(c.yd))
		if got := strings.Join(alive, ","); got != c.want {
			t.Errorf("⚠️ %s：活着 %q，应为 %q（%s）", c.name, got, c.want, why)
		}
		if why == "" {
			t.Errorf("⚠️ %s：没给出说明", c.name)
		}
	}
}

// TestRewritePreRegistrationNumbers：④ 那张表里的两档数与声明费率一致（大商所 0.2 / 0.1，郑商所 2 / 6），
// 且「改写说 ⇒ 平昨档」「标志说 ⇒ 平今档 / 拒单」两行都在 —— 代码的判定与登记同一套数。
func TestRewritePreRegistrationNumbers(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "state.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	i := strings.Index(doc, "事前登记：§13 #25 复现")
	if i < 0 {
		t.Fatal("⚠️ 找不到登记块「事前登记：§13 #25 复现」—— 改名或被挪走，本条失效")
	}
	block := doc[i:]
	if j := strings.Index(block[1:], "\n## "); j >= 0 {
		block = block[:j+1]
	}
	for _, want := range []string{
		"| 按通用平仓、额度 0 ⇒ **平昨档** | 0.2 | 2 |",
		"| 按标志 ⇒ 平今档 | 0.1 | 6 |",
		"| 可平今 0 ⇒ **柜台拒单**（不成交） | — | — |",
	} {
		if !strings.Contains(block, want) {
			t.Errorf("⚠️ 登记块里没有这一行：%q", want)
		}
	}
	// 判定函数在同一套数上给出登记表说的结论
	d := decimal.RequireFromString
	for _, c := range []struct {
		ex             string
		td, yd, atFour string
	}{{"大商所", "0.1", "0.2", "0.2"}, {"郑商所", "6", "2", "2"}} {
		if alive, _ := rewriteVerdict(true, false, d(c.atFour), d(c.td), d(c.yd)); strings.Join(alive, ",") != "改写说" {
			t.Errorf("⚠️ %s：③ 昨仓 + ④ 平昨档应只剩改写说，得到 %v", c.ex, alive)
		}
	}
}

// TestRejectedByCounter：只有「有数值错误码」才算柜台拒单；超时 / 撤干净了但没成交都是「没有结论」。
func TestRejectedByCounter(t *testing.T) {
	if !rejectedByCounter(ctp.OrderState{Status: def.THOST_FTDC_OST_Canceled, ErrorID: 30, StatusMsg: "CTP:平仓量不足"}, errors.New("没成交")) {
		t.Error("⚠️ 有错误码的拒单应判为柜台拒单")
	}
	if rejectedByCounter(ctp.OrderState{Status: def.THOST_FTDC_OST_Canceled}, errors.New("没成交")) {
		t.Error("⚠️ 没有错误码（例如撤掉了没成交的挂单）不该判为拒单 —— 那是没有结论")
	}
	if rejectedByCounter(ctp.OrderState{ErrorID: 30}, nil) {
		t.Error("⚠️ 成交了就不该判为拒单")
	}
}
