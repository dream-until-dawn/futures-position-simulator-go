package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
)

// parsePkgMain 解析本目录下全部非测试 .go 文件。
func parsePkgMain(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*ast.File{}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files[n] = f
	}
	if len(files) < 10 {
		t.Fatalf("⚠️ 只解析到 %d 个生产文件 —— 目录不对，下面在空集上跑", len(files))
	}
	return fset, files
}

// TestCTPValveCallSitesCarryProtection 钉住**每一个 CTP 命令的安全阀都带着受保护腿**，
// 唯一的例外是 ctp-closeorder，而且例外必须显式写出来。
//
// # ⚠️ 它来自 20260913 评审第一节
//
// `safety.ProtectedLeg` 早就有、`valve_wire_test.go` 也证明它在 CTP 阀门上能拦 ——
// **而 11 个生产调用点全部传 nil**。接线测试注入了腿，于是它测的是「能接」，不是「接了」。
// 周一操作单上保护种子的，只剩两句提醒。
//
// ⇒ 改成「默认带保护」：`ctpValve(env)` 不再收 legs；要不带，只能调 `ctpValveExempt` 并写理由。
// 本条守的是这个形状不被悄悄退回去：
//
//	每一处 `X.Valve = …` 的右边，只能是 ctpValve(…) 或 ctpValveExempt(…)
//	ctpValveExempt 恰好一处：closeorder.go 的 runCTPCloseOrder，理由是非空字面量
//	ctpValveWith 只在 ctpValve / ctpValveExempt 两个函数体里
//	包内没有别处直接写 `safety.Valve{…}`
func TestCTPValveCallSitesCarryProtection(t *testing.T) {
	fset, files := parsePkgMain(t)
	calleeName := func(e ast.Expr) string {
		if c, ok := e.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok {
				return id.Name
			}
		}
		return ""
	}
	protected, exempt, with, assigns := 0, 0, 0, 0
	for name, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.AssignStmt:
					for i, lhs := range v.Lhs {
						sel, ok := lhs.(*ast.SelectorExpr)
						if !ok {
							continue
						}
						// ⚠️ `c.Valve.Protected = nil` 这一种：右边的构造器对了，事后把腿摘掉。
						if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Valve" {
							t.Errorf("⚠️ %s：事后改写了 `.Valve.%s` —— 构造器带上的保护会被这一行摘掉",
								fset.Position(v.Pos()), sel.Sel.Name)
							continue
						}
						if sel.Sel.Name != "Valve" || i >= len(v.Rhs) {
							continue
						}
						assigns++
						switch calleeName(v.Rhs[i]) {
						case "ctpValve", "ctpValveExempt":
						default:
							t.Errorf("⚠️ %s：`.Valve = …` 的右边不是 ctpValve / ctpValveExempt —— "+
								"绕过了受保护腿", fset.Position(v.Pos()))
						}
					}
				case *ast.CallExpr:
					switch calleeName(v) {
					case "ctpValve":
						protected++
					case "ctpValveExempt":
						exempt++
						pos := fset.Position(v.Pos())
						if name != "closeorder.go" || fn.Name.Name != "runCTPCloseOrder" {
							t.Errorf("⚠️ %s（%s）调了 ctpValveExempt —— **只许 ctp-closeorder 豁免**，"+
								"理由是受保护腿不分今昨、会拦掉 #4 的通用平仓；别的命令没有这个理由",
								pos, fn.Name.Name)
						}
						if len(v.Args) < 2 {
							break
						}
						lit, ok := v.Args[1].(*ast.BasicLit)
						if !ok || lit.Kind != token.STRING {
							t.Errorf("⚠️ %s：豁免理由不是字符串字面量 —— 理由要在调用点就读得到", pos)
						} else if s, _ := strconv.Unquote(lit.Value); len(s) < 20 {
							t.Errorf("⚠️ %s：豁免理由太短 %q", pos, s)
						}
					case "ctpValveWith":
						with++
						if name != "main.go" || (fn.Name.Name != "ctpValve" && fn.Name.Name != "ctpValveExempt") {
							t.Errorf("⚠️ %s（%s）直接调了 ctpValveWith —— 那等于自己挑腿，"+
								"「默认带保护」就又变回了每个调用点各自的选择", fset.Position(v.Pos()), fn.Name.Name)
						}
					}
				case *ast.CompositeLit:
					if sel, ok := v.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Valve" && fn.Name.Name != "ctpValveWith" {
						t.Errorf("⚠️ %s（%s）直接构造了 safety.Valve —— CTP 侧只许 ctpValveWith 构造",
							fset.Position(v.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if exempt != 1 {
		t.Errorf("⚠️ ctpValveExempt 被调了 %d 次，要恰好 1 次（ctp-closeorder）", exempt)
	}
	if with != 2 {
		t.Errorf("⚠️ ctpValveWith 在生产代码里被调了 %d 次，要恰好 2 次（ctpValve 与 ctpValveExempt 各一）", with)
	}
	// ⚠️ 反空转：20260913 时 10 个受保护的调用点 + 1 个豁免。少于这个数说明扫描范围不对，
	// 或者有命令不再设 Valve（零值 AllowOrder=false 会拦一切，失败方向是对的，但那不该静默发生）。
	if protected < 10 || assigns != protected+exempt {
		t.Errorf("⚠️ 受保护调用点 %d 处（下界 10），`.Valve =` 赋值 %d 处，豁免 %d 处 —— 对不上", protected, assigns, exempt)
	}
	t.Logf("ⓘ `.Valve =` %d 处：受保护 %d、豁免 %d", assigns, protected, exempt)
}

// TestCTPProtectedLegsBlockTheSeed 钉住**表里的每一条腿在生产的 ctpValve 上真的拦得住**，
// 而且过了交易日就不拦。
//
// ⚠️ 与 TestCTPValveCarriesEnv 的分工：那一条**注入**腿，证明「能接」；
// 这一条从 `ctpValve(env)` 出发、用的是 `ctpProtectedLegs` 本身，证明「接的是这张表」。
//
// ⚠️ 它**查不到**方向写反：表里写成空头，本条照样通过（腿与测试用的是同一个方向）。
// 种子的真实方向只有账户知道 —— 那一格登记在破坏清单里，不假装守住了。
func TestCTPProtectedLegsBlockTheSeed(t *testing.T) {
	env := probe.Env{AllowOrder: true, MaxVolume: 1}
	day := regexp.MustCompile(`^20\d{6}$`)
	for i, l := range ctpProtectedLegs {
		switch {
		case !strings.Contains(l.Symbol, "."):
			t.Errorf("第 %d 条的 Symbol %q 不是 交易所.合约 形状 —— 与 OrderReq.Symbol() 永远对不上", i, l.Symbol)
		case l.Side != safety.Long && l.Side != safety.Short:
			t.Errorf("⚠️ 第 %d 条（%s）的 Side 是零值 —— 它谁也拦不住，而声明看起来是写了的", i, l.Symbol)
		case !day.MatchString(l.TradingDay):
			t.Errorf("⚠️ 第 %d 条（%s）的 TradingDay %q 不是 8 位交易日 —— "+
				"空串是永久保护，写错格式则**永远不生效**", i, l.Symbol, l.TradingDay)
		case len(l.Why) < 20:
			t.Errorf("⚠️ 第 %d 条（%s）的 Why 太短 %q", i, l.Symbol, l.Why)
		case l.Volume < 1:
			t.Errorf("⚠️ 第 %d 条（%s）没有声明 Volume —— ctp-flatten 的收尾判定会把那条腿上的**每一手**都报成多出，"+
				"而真正遗留的今仓与种子就分不开了", i, l.Symbol)
		}

		v := ctpValve(env)
		in := safety.Intent{Symbol: l.Symbol, Closing: true, ClosesSide: l.Side,
			Volume: 1, LimitPrice: 1, Desc: "seed test"}
		v.TradingDay = l.TradingDay
		if err := v.Check(in); err == nil || !strings.Contains(err.Error(), "受保护的持仓腿") {
			t.Errorf("⚠️ 第 %d 条（%s %s）在它自己的交易日上没有被拦：%v", i, l.Symbol, l.TradingDay, err)
		}
		// ⚠️ 反向：过了交易日必须放行 —— 否则一个「凡是这个合约都拦」的实现也能过上面那条，
		// 而它会在种子用完之后继续拦住正当的收尾。
		v.TradingDay = "20991231"
		if err := v.Check(in); err != nil {
			t.Errorf("⚠️ 第 %d 条（%s）过了交易日还在拦：%v", i, l.Symbol, err)
		}
	}

	// ⚠️ 端到端：从一笔真实形状的 CTP 委托出发（卖出平昨），经 Client.Check 走到拒绝。
	// 未连接的客户端 TradingDay() 是空串 ⇒ 「还不知道今天是哪天」⇒ 照拦。
	if len(ctpProtectedLegs) > 0 {
		l := ctpProtectedLegs[0]
		ex, inst := ctp.SplitSymbol(l.Symbol)
		var dir def.TThostFtdcDirectionType = def.THOST_FTDC_D_Sell
		if l.Side == safety.Short {
			dir = def.THOST_FTDC_D_Buy
		}
		c := ctp.New(ctp.Credentials{BrokerID: "9999", UserID: "x"}, nil)
		c.Valve = ctpValve(env)
		if err := c.Check(ctp.OrderReq{Exchange: ex, Instrument: inst, Direction: dir,
			Offset: def.THOST_FTDC_OF_CloseYesterday, Volume: 1, LimitPrice: 3000}); err == nil {
			t.Errorf("⚠️ 一笔平 %s 的委托经 Client.Check 放行了 —— 生产的 ctpValve 没带上表", l.Symbol)
		}
	}
}

// TestCloseOrderRoutesPremiseThroughVerdict 钉住 runCTPCloseOrder 在「昨 0」那一支
// **经 closeOrderVerdict 判**，而不是自己直接报结论。
//
// ⚠️ 上一版就是自己报的：`closeOrderDescribe(coNoYesterday, s0, s0)` —— 于是种子不在（今 0 昨 0）
// 或还没跨过结算（今天开的今仓）时，它照样打印「这是一条结论，不是失败」，
// 而操作单写着「那就是 #4 的结论，别当失败重跑」。
func TestCloseOrderRoutesPremiseThroughVerdict(t *testing.T) {
	_, files := parsePkgMain(t)
	fn := findFunc(files["closeorder.go"], "runCTPCloseOrder")
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPCloseOrder")
	}
	verdicts, direct := 0, 0
	ast.Inspect(fn, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := c.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch id.Name {
		case "closeOrderVerdict":
			verdicts++
		case "closeOrderDescribe":
			if len(c.Args) > 0 {
				if k, ok := c.Args[0].(*ast.Ident); ok && strings.HasPrefix(k.Name, "co") {
					direct++
				}
			}
		}
		return true
	})
	if verdicts < 2 {
		t.Errorf("⚠️ runCTPCloseOrder 只调了 %d 次 closeOrderVerdict，要 ≥2（前提那一支 + 最终判定）", verdicts)
	}
	if direct != 0 {
		t.Errorf("⚠️ runCTPCloseOrder 里有 %d 处把判定常量**直接**交给 closeOrderDescribe —— "+
			"那是在绕过判定自己下结论", direct)
	}
}

// TestFlattenHintsNameASymbol 钉住**生产代码里每一句提到 ctp-flatten 的话都带着 `-symbol`**。
//
// # ⚠️ 它来自 20260913 评审第二节
//
// flattenScope 之后，裸 `ctp-flatten` 会被拒，而拒绝信息给出的另一半是 `-all`。
// 而 ctp-reject / ctp-slices / ctp-fee / ctp-hold 出事时的补救提示**全是裸的**，
// 有的还带着「立刻」—— 操作者被催着，补全最顺手的就是被禁的那条。
// 评审点了 4 处，**我 grep 出来是 9 处**（fee.go 两处、main.go 三处他没列）。
//
// ⚠️ 方法论 92 第二档：错误信息里的判据描述不许有偏差，**因为读它的人手边没有操作单**。
//
// 判据：字符串字面量里每一次出现 `ctp-flatten`，后面（跳过反引号与空格、左括号）必须紧跟 `-symbol`。
// 例外只有**恰好等于** "ctp-flatten" 的字面量（命令名本身：switch 分支、FlagSet 名）。
func TestFlattenHintsNameASymbol(t *testing.T) {
	// ⚠️ 先证明判据有判别力：裸的、只给 -all 的要抓，带 -symbol 的要放。
	for _, c := range []struct {
		lit string
		bad int
	}{
		{"立刻跑 ctp-flatten", 1},
		{"跑 `ctp-flatten` 收拾", 1},
		{"ctp-flatten -all", 1},
		{"跑 `ctp-flatten -symbol %s`", 0},
		{"oracle ctp-flatten (-symbol X | -all)", 0},
		{"ctp-flatten", 0},
	} {
		if got := bareFlattenHints(c.lit); got != c.bad {
			t.Fatalf("⚠️ 判据在 %q 上数出 %d 处，要 %d —— **判据坏了，下面扫描不可信**", c.lit, got, c.bad)
		}
	}
	fset, files := parsePkgMain(t)
	seen := 0
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			seen += strings.Count(s, "ctp-flatten")
			if k := bareFlattenHints(s); k > 0 {
				t.Errorf("⚠️ %s：%q 提到 ctp-flatten 而没带 -symbol —— "+
					"读它的人手边没有操作单，补全最顺手的是 -all（会去平种子）", fset.Position(lit.Pos()), s)
			}
			return true
		})
	}
	if seen < 10 {
		t.Fatalf("⚠️ 生产代码里只见到 %d 处 ctp-flatten（下界 10）—— 扫描范围不对，本条在空集上跑", seen)
	}
}

// bareFlattenHints 数一个字符串里「提到 ctp-flatten 而没紧跟 -symbol」的次数。
func bareFlattenHints(s string) int {
	if s == "ctp-flatten" {
		return 0
	}
	bad := 0
	for rest := s; ; {
		i := strings.Index(rest, "ctp-flatten")
		if i < 0 {
			return bad
		}
		rest = rest[i+len("ctp-flatten"):]
		if !strings.HasPrefix(strings.TrimLeft(rest, "` ("), "-symbol") {
			bad++
		}
	}
}
