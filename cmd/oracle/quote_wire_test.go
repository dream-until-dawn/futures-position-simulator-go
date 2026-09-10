package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestQuoteFailureBlocksTheWrite 钉住一条**只由语句顺序保证**的不变式：
//
//	补行情失败 ⇒ **整份截面不落盘**
//
// # ⚠️ 为什么它必须有守卫
//
// 这条不变式此刻的全部保障，是 `runCTPParams` 里 `AttachQuote` 的 error
// 在 `fx.Write` **之前** return。⚠️ 谁把 `fx.Write` 挪上去一行、
// 或者把那个 `return` 降成一句日志，**不会有任何东西红**。
//
// 而它的失败方式正是错误信息里自己写的那一种：
// **一份缺了 quotes 的夹具与一份本来就不带 quotes 的分不开。**
// ⇒ 一份半截的截面会安静地进 `testdata/`，然后被后来的人当成「那天没抓行情」——
// 而这批夹具是拿去当**证据**的（§6.8 的保证金基准就是从单份夹具上得出的）。
//
// # ⚠️ 这条的形状比 73 再往前一格
//
//	72    验证做过了，只写在注释里    ⇒ 下一个人以为验过了
//	73    盲区写清楚了，没安排动作    ⇒ 下一个人以为被安排了
//	本条  不变式实现对了、理由写在**错误信息**里，没有守卫 ⇒ 读代码的人以为它被保护着
//
// **那句错误信息写得越好，越像已经被保护了。**（评审 20260910 提出，
// 引的是我自己在 73 里写的「文字越好，替代得越彻底」。）
//
// ⚠️ 技术与 `TestSendIsTheOnlyPathToInsert` 同源：读源码断言**顺序**，
// 而不是断言某次运行的结果 —— 结构守卫在「有人写出第二条路径」那一刻就红，
// 不必等那条路径被走到。
func TestQuoteFailureBlocksTheWrite(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := findFunc(f, "runCTPParams")
	if fn == nil {
		t.Fatal("⚠️ main.go 里找不到 runCTPParams —— 改名了？本条会在空集上跑（那是全绿）")
	}

	var quoteIf *ast.IfStmt
	var writePos token.Pos
	calls := 0
	ast.Inspect(fn, func(n ast.Node) bool {
		if ifs, ok := n.(*ast.IfStmt); ok && mentions(ifs.Init, "AttachQuote") {
			quoteIf, calls = ifs, calls+1
		}
		if c, ok := n.(*ast.CallExpr); ok && selName(c.Fun) == "Write" && writePos == token.NoPos {
			writePos = c.Pos()
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("⚠️ runCTPParams 里 `if err := …AttachQuote(…); err != nil` 出现 %d 次（要恰好 1 次）"+
			" —— 形状变了，下面的断言查不到它要查的东西", calls)
	}
	if writePos == token.NoPos {
		t.Fatal("⚠️ runCTPParams 里找不到 `.Write(` —— 落盘那一步改名了？本条不再有意义")
	}

	// ⚠️ 一、顺序：补行情必须在落盘**之前**。
	if quoteIf.Pos() > writePos {
		t.Errorf("⚠️ `AttachQuote` 出现在 `.Write` **之后**（%d > %d）—— "+
			"于是补行情失败时**半份截面已经落盘了**，而它与一份本来就不带 quotes 的分不开",
			quoteIf.Pos(), writePos)
	}
	// ⚠️ 二、失败必须 return，不许降级成日志。
	//    ⚠️ 这一条不能省：顺序对了而错误被吞掉，落盘照样发生，
	//    而**只查顺序的守卫在那种改法下一个字都不会说**。
	if !hasReturn(quoteIf.Body) {
		t.Errorf("⚠️ 补行情失败那一支里**没有 return** —— 错误被吞掉，" +
			"落盘照样发生。⚠️ 一句日志与一次拦截在磁盘上的区别是：前者留下半份证据")
	}
}

// exprName 把方向实参归约成一个名字，**同时认标识符与选择器**。
//
// ⚠️ 只认 `*ast.Ident` 是不够的：破坏 260 把方向换成 `def.THOST_FTDC_D_Buy`
// （一个选择器），于是取不到值、守卫红在**反空转的 Fatal** 上而不是红在断言上
// —— 分层判定当场报了「红错了理由」。**一条红了的破坏，不等于一条红对了的破坏。**
func exprName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.CallExpr:
		// ⚠️ **类型转换要剥掉**：`def.TThostFtdcDirectionType(def.THOST_FTDC_D_Buy)`
		// 在 AST 里是一次调用，而我们要的是里面那个常量名。
		// ⚠️ 这是第二次因为「守卫太窄」而扩它（第一次是选择器，破坏 260）——
		// 判据同 silent-risks 78：**那个写法在真实代码里会不会出现？**
		// 显式类型转换是 Go 里再正常不过的写法 ⇒ 是守卫窄，不是代码偏。
		if len(x.Args) == 1 {
			return exprName(x.Args[0])
		}
	}
	return ""
}

func findFunc(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
			return fd
		}
	}
	return nil
}

func mentions(n ast.Node, name string) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func selName(e ast.Expr) string {
	if s, ok := e.(*ast.SelectorExpr); ok {
		return s.Sel.Name
	}
	return ""
}

func hasReturn(b *ast.BlockStmt) bool {
	if b == nil {
		return false
	}
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			found = true
		}
		return !found
	})
	return found
}

// ⚠️ 顺带：`AttachQuote` 在全包只该被调用一次。
// 第二处调用会绕开上面那两条断言 —— 与 `TestSendIsTheOnlyPathToInsert`
// 防的是同一件事：**不给第二条到达路径**。
func TestAttachQuoteHasOneCallSite(t *testing.T) {
	fset := token.NewFileSet()
	n := 0
	files := goFilesHere(t)
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(x ast.Node) bool {
			if c, ok := x.(*ast.CallExpr); ok && selName(c.Fun) == "AttachQuote" {
				n++
			}
			return true
		})
	}
	if len(files) < 2 {
		t.Fatalf("⚠️ 只扫到 %d 个源文件 —— 本条在空转", len(files))
	}
	if n != 1 {
		t.Errorf("⚠️ `AttachQuote` 在本包被调用 %d 次（要恰好 1 次）—— "+
			"第二条调用路径会绕开「失败则不落盘」那两条断言", n)
	}
}

func goFilesHere(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range all {
		if !strings.HasSuffix(n, "_test.go") {
			out = append(out, n)
		}
	}
	return out
}

// TestRestingPriceMatchesDirection 钉住 `ctp-order` 的核心不变式：
//
//	挂价必须由**同一个方向**算出来
//
// # ⚠️ 它防的是什么
//
// 这个命令的全部意义是「挂得上、成不了」—— 买开挂跌停、卖平挂涨停。
// `FarPrice(md, dir)` 给出的正是与 `dir` 配套的那一端。
// ⚠️ 若有人把 `Direction` 与 `FarPrice` 的方向拆开设（比如卖平却挂跌停），
// 这笔单会**当场成交** —— 于是一个只该挂一下的探针变成了一次真实的开/平仓，
// **而它在日志里长得和成功的探针一模一样**（都有回报、都有状态）。
//
// ⚠️ 20260910 加 `-close` 时引入了这条不变式，而当时**破坏数没变（259→259）**——
// 按 silent-risks.md 74 的那条检查：**加了代码而破坏数没变，本身就该是一个问句。**
// 这条守卫是那个问句的答案。
func TestRestingPriceMatchesDirection(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := findFunc(f, "runCTPOrder")
	if fn == nil {
		t.Fatal("⚠️ main.go 里找不到 runCTPOrder —— 改名了？本条会在空集上跑")
	}
	var dirIdent, farIdent string
	ast.Inspect(fn, func(n ast.Node) bool {
		if kv, ok := n.(*ast.KeyValueExpr); ok {
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Direction" {
				dirIdent = exprName(kv.Value)
			}
		}
		if c, ok := n.(*ast.CallExpr); ok && selName(c.Fun) == "FarPrice" && len(c.Args) == 2 {
			farIdent = exprName(c.Args[1])
		}
		return true
	})
	if dirIdent == "" || farIdent == "" {
		t.Fatalf("⚠️ 没能同时取到 Direction 的取值（%q）与 FarPrice 的方向实参（%q）—— "+
			"形状变了，本条查不到它要查的东西", dirIdent, farIdent)
	}
	if dirIdent != farIdent {
		t.Errorf("⚠️ 委托方向用的是 %q，而挂价用的是 FarPrice(…, %q) —— **两者必须是同一个**。"+
			"拆开之后这笔单会**当场成交**，而一个只该挂一下的探针会变成一次真实的开/平仓，"+
			"**在日志里与成功的探针长得一模一样**", dirIdent, farIdent)
	}
}

// legalLegs 是 `ctp-order` 允许的**方向与开平的配对**，⚠️ 只有这两种。
//
//	买 + 开      挂跌停 —— 挂得上、成不了
//	卖 + 平今    挂涨停 —— 挂得上、成不了
var legalLegs = map[[2]string]bool{
	{"THOST_FTDC_D_Buy", "THOST_FTDC_OF_Open"}:        true,
	{"THOST_FTDC_D_Sell", "THOST_FTDC_OF_CloseToday"}: true,
}

// TestDirectionAndOffsetAreSetTogether 钉住 `runCTPOrder` 的**第二条**不变式：
//
//	方向与开平必须**成对**赋值，且配对只有两种
//
// # ⚠️ 它为什么比「挂价方向」那条更隐蔽
//
// 若拆成 `dir = Sell` / `off = **Open**`：
//
//	FarPrice(md, Sell) = 涨停      ⇒ 卖单挂涨停，**挂得上、成不了** —— 表面完全正常
//	Offset = Open                  ⇒ 这是一笔**开仓**单，不是平仓单
//	安全阀                         ⇒ `intentOf` 判 Closing=false，**开仓是合法的，阀不拦**
//	⚠️ 而**开仓挂单会冻保证金**
//
// ⇒ 量出来的会是 `FrozenMargin ≈ 5000` 而不是 0，
// 于是 `kq_facts` 42（平仓挂单不冻保证金）的结论**整个翻过来**。
//
//	第一条不变式坏掉  探针会**成交** —— 至少留下一个成交回报
//	本条坏掉          ⚠️ 探针照样挂得上、照样撤得掉、日志一切正常，**只有那个数变了**
//	                  而那个数**正是结论本身**
//
// ⚠️ **它不改变任何行为，只改变测到的量** —— 这是 20260910 评审抓到的，
// 而我用第 74 条那一问只问出了第一条就停了。**「找到了一个」和「找完了」是两件事。**
func TestDirectionAndOffsetAreSetTogether(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := findFunc(f, "runCTPOrder")
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPOrder —— 改名了？本条会在空集上跑")
	}
	n := 0
	ast.Inspect(fn, func(x ast.Node) bool {
		as, ok := x.(*ast.AssignStmt)
		if !ok {
			return true
		}
		var names []string
		for _, l := range as.Lhs {
			names = append(names, exprName(l))
		}
		touches := has2(names, "dir") || has2(names, "off")
		if !touches {
			return true
		}
		n++
		// ⚠️ 一、必须**同时**赋两者。分开赋值时「只改了一行」在源码里看不出异样。
		if !has2(names, "dir") || !has2(names, "off") {
			t.Errorf("⚠️ 第 %d 处赋值只给了 %v —— **方向与开平必须成对赋值**。"+
				"拆开之后可以配出「卖+开」：挂涨停照样挂得上、阀不拦（开仓合法），"+
				"⚠️ **而开仓挂单会冻保证金 ⇒ 量出来的数把 kq_facts 42 整个翻过来**", n, names)
			return true
		}
		if len(as.Rhs) != 2 {
			t.Errorf("⚠️ 第 %d 处赋值左边是 dir/off，右边却有 %d 个值 —— 形状变了", n, len(as.Rhs))
			return true
		}
		// ⚠️ 二、配对只能是那两种。
		di, oi := indexOf(names, "dir"), indexOf(names, "off")
		pair := [2]string{exprName(as.Rhs[di]), exprName(as.Rhs[oi])}
		if !legalLegs[pair] {
			t.Errorf("⚠️ 第 %d 处的配对是 %v —— **开平与方向不配对**。"+
				"只允许「买+开」与「卖+平今」，两者都是「挂得上、成不了」的那一端", n, pair)
		}
		return true
	})
	if n < 2 {
		t.Fatalf("⚠️ 只扫到 %d 处 dir/off 赋值（要至少 2 处：默认与 -close 分支）—— 本条在空转", n)
	}
}

func has2(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func indexOf(xs []string, s string) int {
	for i, x := range xs {
		if x == s {
			return i
		}
	}
	return -1
}

// TestHeldCaptureHappensBeforeCancel 钉住 `runCTPOrder` 的**第三、第四**条不变式：
//
//	三  「挂着时」的截面必须在 `Cancel` **之前**拍
//	四  拍失败**不许提前返回** —— 那笔单还挂在柜台上
//
// # ⚠️ 三：撤单之后拍，量到的是零，而零正是结论本身
//
// 撤单会把 `FrozenMargin` / `FrozenCommission` 释放回零。
// ⚠️ **一份撤单后拍的截面，与一份「本来就不冻」的截面长得一模一样** ——
// 而「平仓挂单不冻保证金」（`kq_facts` 42）这条结论，量的正是这个零。
//
//	⇒ 把拍挪到撤单之后，会得到一份**看起来完美支持结论**的夹具，
//	  而它其实什么都没测。**比拍不到更坏。**
//
// # ⚠️ 四：拍失败提前返回，会把一笔活单留在柜台上
//
// 这是安全性质，不是正确性性质：`Cancel` 之前的任何 `return`
// 都意味着**那笔单还挂着而进程走了**。
//
// ⚠️ 本条与 `TestQuoteFailureBlocksTheWrite` 的方向**恰好相反**，值得对照：
//
//	落盘那边   补行情失败 ⇒ **必须**提前 return（不许落半份证据）
//	这边       拍截面失败 ⇒ **不许**提前 return（不许留一笔活单）
//
// **同样是「失败了怎么办」，答案由「返回之后留下什么」决定，不由一致性决定。**
func TestHeldCaptureHappensBeforeCancel(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := findFunc(f, "runCTPOrder")
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPOrder —— 改名了？本条会在空集上跑")
	}
	var capPos, lastCapPos, cancelPos token.Pos
	ast.Inspect(fn, func(x ast.Node) bool {
		c, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch selName(c.Fun) {
		case "Capture":
			if capPos == token.NoPos {
				capPos = c.Pos()
			}
			lastCapPos = c.Pos()
		case "Cancel":
			if cancelPos == token.NoPos {
				cancelPos = c.Pos()
			}
		}
		return true
	})
	if capPos == token.NoPos || cancelPos == token.NoPos {
		t.Fatalf("⚠️ 没能同时取到 Capture（%d）与 Cancel（%d）的位置 —— "+
			"形状变了，本条查不到它要查的东西", capPos, cancelPos)
	}
	// ⚠️ 查**最后一处**而不只是第一处：破坏 263 第一版**加了一处**晚拍的
	// `Capture`，而只看第一处的守卫**一个字都不会说** —— 那份晚拍的截面会以
	// 另一个文件名落进 testdata/，冻结字段全是零，**而它看起来完美支持结论**。
	if lastCapPos > cancelPos {
		t.Errorf("⚠️ 有 `Capture` 出现在 `Cancel` **之后** —— 撤单已经把冻结字段释放回零，"+
			"⚠️ **而一份撤单后拍的截面，与一份「本来就不冻」的截面长得一模一样**。"+
			"它会看起来完美支持 kq_facts 42，而其实什么都没测 —— **比拍不到更坏**")
	}
	// ⚠️ 四：Capture 与 Cancel 之间不许有 return。
	//
	// ⚠️ 按**语句下标**取区间，不按 token 位置：包着 `Cancel` 的那个 `if`
	// 起始位置在 `Cancel` 调用**之前**，于是「位置在 cancelPos 之前」会把
	// `if err := c.Cancel(…); err != nil { return err }` 自己算进去 ——
	// 第一版就这么误报了一次。**这不是守卫太窄，是守卫的边界画错了。**
	i, j := -1, -1
	for k, st := range fn.Body.List {
		if st.Pos() <= capPos && capPos <= st.End() {
			i = k
		}
		if st.Pos() <= cancelPos && cancelPos <= st.End() {
			j = k
		}
	}
	if i < 0 || j < 0 {
		t.Fatalf("⚠️ 没能定位到包着 Capture(%d) 与 Cancel(%d) 的顶层语句 —— 本条在空转", i, j)
	}
	for _, st := range fn.Body.List[i+1 : j] {
		if hasReturn(blockOf(st)) {
			t.Errorf("⚠️ `Capture` 与 `Cancel` 之间出现了 return —— "+
				"**那笔单还挂在柜台上，而进程走了**。"+
				"拍截面失败要留到撤完再报（与落盘那边**方向相反**：那边失败必须提前返回）")
		}
	}
}

// blockOf 把一条语句包成一个块，好复用 hasReturn。
func blockOf(s ast.Stmt) *ast.BlockStmt { return &ast.BlockStmt{List: []ast.Stmt{s}} }
