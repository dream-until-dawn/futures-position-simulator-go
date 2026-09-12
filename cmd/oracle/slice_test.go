package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"testing"
)

// TestSliceCostCandidateIsAveragePriceTimesMultiplier 钉住「均价那一片的成本」
// 这个**只会被打印、不会被比对**的数：它进不了任何断言，
// 算错了的表现是三个候选里有一个站错了位置，而「命中哪一个」照样打印。
//
// ⚠️ **这条测试的名字改过一次，而改名的理由要留在这里。**
// 它原名 `一个以 `MultiplyNotDivide` 结尾的名字（⚠️ 它**从没进过任何一次提交**，只在工作树里活了二十分钟，所以这里不写它的全名：一个指向不存在的守卫的引用，与一个指错了的引用在读者那里同形）`，注释里写着它来自
// 「写这个探针时当场犯的一次」：`(p1+p2)/2**mult` 被 Go 解析成
// `(p1+p2) / (2 * *mult)`。**那件事没有发生过** —— `/` 与 `*` 同优先级
// 左结合，两种写法是同一个表达式（实测 31010 == 31010），原代码本来就对。
//
//	⚠️ 于是它当时是这样一条测试：**性质是真的，叙述是假的。**
//	而两者在绿的时候长得一模一样 —— 后来的人会照着那段叙述
//	去防一个不存在的坑，并信任一条「有事故背书」的守卫。
//
// ⚠️ 拆穿它的是**设计破坏验证**：要让它红就得把算式改错，
// 而我以为的那个「错写法」编译成同一棵树 ⇒ **零层在事前就不成立**。
// 见 silent-risks 方法论 85。
func TestSliceCostCandidateIsAveragePriceTimesMultiplier(t *testing.T) {
	const p1, p2, mult = 3100, 3102, 10.0
	first, second, avg := sliceCostCandidates(p1, p2, mult)

	// ⚠️ **结构性质先断言，具体数值最后。**顺序不是风格问题：
	// 三条断言都用 `Fatalf`，于是**排在前面那条决定了红在哪一行** ——
	// 而破坏验证的第三层查的正是「红在被测性质上」。
	// 第一版把数值那条放在最前，于是每一条破坏都红在同一句
	// 「均价那一片的成本 = … 要的是 …」上，**三条破坏在输出里分不开**，
	// breakcheck 一口气报了三个「红错了理由」。
	//
	//	⚠️ 三条断言全在、全会红、而它们**测的是不是三件事，从绿的那一侧看不出来**。

	// 一、三个候选要**两两分得开**，否则「命中哪一个」这句话没有意义。
	if first == second || first == avg || second == avg {
		t.Fatalf("⚠️ 三个成本候选没分开：%.4f / %.4f / %.4f", first, second, avg)
	}
	// 二、均价那一个必须**夹在**两片之间 —— 这是它作为「均价」的定义性质，
	//     也是唯一一条不依赖具体数字的断言。
	if !(first < avg && avg < second) {
		t.Fatalf("⚠️ 均价 %.4f 没有夹在两片 %.4f / %.4f 之间", avg, first, second)
	}
	// 三、量级：夹在中间也可能整体错一个乘数。
	const want = 31010.0 // ((3100+3102)/2) × 10
	if math.Abs(avg-want) > 1e-9 {
		t.Fatalf("均价那一片的成本 = %.4f，要的是 %.4f", avg, want)
	}
}

// TestSliceCandidatesSeparateOnlyWhenPricesDiffer 把探针里那条前提
// 「p1 == p2 ⇒ 本轮没有判别力」变成**被测的性质**，而不是一句注释。
//
// ⚠️ 两个方向都断言：价不同要两两分得开，价相同要三者同值。
// 只断言前者的话，「什么时候该拒绝这个样本」仍然没人验过。
func TestSliceCandidatesSeparateOnlyWhenPricesDiffer(t *testing.T) {
	const q, mult = 3105, 10.0

	fifo, lifo, avg := sliceProfitCandidates(q, 3100, 3102, mult)
	for _, pair := range [][2]float64{{fifo, lifo}, {fifo, avg}, {lifo, avg}} {
		if math.Abs(pair[0]-pair[1]) < 1e-9 {
			t.Errorf("⚠️ p1≠p2 时两个候选撞上了：%.4f vs %.4f —— 样本没有判别力，"+
				"而探针会照常打印「命中」", pair[0], pair[1])
		}
	}

	sFifo, sLifo, sAvg := sliceProfitCandidates(q, 3100, 3100, mult)
	if math.Abs(sFifo-sLifo) > 1e-9 || math.Abs(sFifo-sAvg) > 1e-9 {
		t.Errorf("⚠️ p1==p2 时三个候选竟然分得开（%.4f/%.4f/%.4f）—— "+
			"那条「两片同价就没有判别力」的拒绝条件就不成立了，"+
			"而探针里正是靠它决定要不要退出", sFifo, sLifo, sAvg)
	}
	// ⚠️ 同值不等于「这测试有意义」：再核一次它们确实被算过、不是恒零。
	if sFifo == 0 {
		t.Fatal("⚠️ 三个候选同为 0 —— 本条在空值上跑")
	}
}

// TestSlicePreconditionCheckedBeforeAnyOrder 断言**前提检查排在第一笔委托之前**。
//
// ⚠️ 这条守的是 20260911 夜盘 `ctp-hold` 那次假结论的同一个洞：
// 判据本身没写错，错在前提被违反，而它不会说「我的前提不成立了」。
// 本探针靠 OpenCost 的**增量**反解片价 —— 账上先有别人的片，
// p1/p2 全错而数值完全正常。检查必须在下单之前，
// **下单之后再查就已经晚了**：那时账上已经多了一手，前提是被自己破坏的。
func TestSlicePreconditionCheckedBeforeAnyOrder(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "slice.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "runCTPSlices" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPSlices —— 它改名了，本条守卫失效")
	}
	checkPos, orderPos := token.NoPos, token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING && checkPos == token.NoPos &&
				containsAll(v.Value, "前提不成立", "不跑") {
				checkPos = v.Pos()
			}
		case *ast.CallExpr:
			// 第一笔真实委托：直接 c.Insert，或经 openOneLot 下的单。
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Insert" &&
				orderPos == token.NoPos {
				orderPos = v.Pos()
			}
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "openOneLot" &&
				orderPos == token.NoPos {
				orderPos = v.Pos()
			}
		}
		return true
	})
	if checkPos == token.NoPos {
		t.Fatal("⚠️ runCTPSlices 里找不到那句「前提不成立，不跑」—— " +
			"要么它被删了，要么措辞改了而本条守卫在空转")
	}
	if orderPos == token.NoPos {
		t.Fatal("⚠️ runCTPSlices 里一笔委托都找不到 —— 本条在空集上跑")
	}
	if checkPos > orderPos {
		t.Fatalf("⚠️ **前提检查排在第一笔委托之后**（检查 %s，下单 %s）—— "+
			"那时账上已经多了一手，检查的是被自己破坏过的前提",
			fset.Position(checkPos), fset.Position(orderPos))
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestSideWantedRefusesUnknownAndActuallySeparates 守的是一个
// **拼错了不会报错**的开关。
//
// ⚠️ -second 存在的理由是拆混淆：20260911 夜盘第一个样本里
// p1 > p2，于是「先开的那片」与「价高的那片」是同一片。
// 若这个开关把认不得的取值默成 any，**它会拍回一个同侧样本**，
// 而同侧样本与拆开了混淆的样本在输出里长得一模一样 ——
// 两边都印着「✅ 命中 FIFO」。
func TestSideWantedRefusesUnknownAndActuallySeparates(t *testing.T) {
	if _, err := sideWanted("higer"); err == nil {
		t.Fatal("⚠️ 拼错的 -second 被默受了 —— 那就会静静地拍回同侧样本")
	}
	higher, err := sideWanted("higher")
	if err != nil {
		t.Fatal(err)
	}
	lower, err := sideWanted("lower")
	if err != nil {
		t.Fatal(err)
	}
	any, err := sideWanted("any")
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 两个方向都要断言：只测「高的放行」的话，
	// 一个恒真的判据也能全部通过。
	if !higher(3105, 3104) || higher(3103, 3104) || higher(3104, 3104) {
		t.Error("higher 不只放行更高的一侧")
	}
	if !lower(3103, 3104) || lower(3105, 3104) || lower(3104, 3104) {
		t.Error("lower 不只放行更低的一侧")
	}
	if !any(3103, 3104) || !any(3105, 3104) {
		t.Error("any 应该两侧都放行")
	}
}

// TestSliceDumpsPinDownBothOrderAndConsumption 钉住**三份落盘**各自定死的那一半。
//
// # ⚠️ 它改过一次名，而改名的理由就是这条守卫自己漏掉的那一半
//
// 原名 `TestDiscriminatingCloseIsBracketedByTwoDumps`，只钉「两份夹住那次平仓」。
// 20260912 评审**第二次打回**，理由是：那两份给出的是
//
//	(p1+p2) ← 前一份的 OpenCost      s ← 后一份的 OpenCost      c = (p1+p2) − s
//
// ⇒ 逐片 vs 均价**够了**；而 FIFO ⟺ `c = p1`，
// 两份只给出**集合** `{c, s} = {p1, p2}` —— **次序不在里面**。
//
//	⚠️ **「夹住那一次平仓」是必要条件，不是充分条件。**
//	而原来那个名字听起来像充分 —— 名字替断言把话说满了。
//
// ⚠️ 而「靠 `-restfirst` 保证 p2 > p1、于是次序能从价推出来」不成立：
// 它是**默认 false 的开关**，20260911 那一轮实际是 p1 > p2、方向恰好相反。
// **那等于把判别性的事实放回运行配置里，而那正是 #13 栽过的地方。**
//
// ⇒ 三份，各定死一半：
//
//	① 腿1 成交后、腿2 之前   Position=1、OpenCost = p1×乘数 ⇒ **谁先开**
//	② 判别性平仓之前         OpenCost = (p1+p2)×乘数
//	③ 判别性平仓之后         ⇒ ②③ 之差给出**消耗了哪一片**
//
// ⚠️ 而三份都带 `trades`（逐笔价 + 时刻 + SequenceNo）—— 那是次序的**直接**证据，
// 与 ① 的反解互相核得动。两条都留着才有交叉。
func TestSliceDumpsPinDownBothOrderAndConsumption(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "slice.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "runCTPSlices" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPSlices —— 改名了，本条守卫失效")
	}
	var dumps, opens []token.Pos
	var closePos token.Pos
	ast.Inspect(fn, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch v := c.Fun.(type) {
		case *ast.Ident:
			switch v.Name {
			case "dumpSlices":
				dumps = append(dumps, c.Pos())
			case "openOneLot", "openOneLotResting":
				opens = append(opens, c.Pos())
			}
		case *ast.SelectorExpr:
			if v.Sel.Name == "Insert" && closePos == token.NoPos &&
				containsAll(sourceOf(fset, c), "OF_CloseToday") {
				closePos = c.Pos()
			}
		}
		return true
	})
	if len(dumps) != 3 {
		t.Fatalf("⚠️ dumpSlices 被调用 %d 次（要恰好 3 次）—— "+
			"少了「腿1 之后、腿2 之前」那一份，**次序就只能从运行开关推**，"+
			"而 #13 正是栽在那上面", len(dumps))
	}
	// 腿 1 的两个分支 + 腿 2，恰好三处开仓调用。
	if len(opens) != 3 {
		t.Fatalf("⚠️ 开仓调用 %d 处（要恰好 3 处：腿1 的两个分支 + 腿2）—— "+
			"形状变了，下面按位置分腿的判据就不成立了", len(opens))
	}
	leg1End, leg2 := opens[1], opens[2]
	if opens[0] > leg1End {
		leg1End = opens[0]
	}
	if closePos == token.NoPos {
		t.Fatal("⚠️ 找不到那笔平今委托 —— 本条在空集上跑")
	}
	pos := func(p token.Pos) string { return fset.Position(p).String() }
	if !(dumps[0] > leg1End && dumps[0] < leg2) {
		t.Errorf("⚠️ 第一份落盘没有落在**腿1 之后、腿2 之前**（落盘 %s，腿1 %s，腿2 %s）—— "+
			"它是「谁先开」的唯一夹具来源", pos(dumps[0]), pos(leg1End), pos(leg2))
	}
	if !(dumps[1] > leg2 && dumps[1] < closePos) {
		t.Errorf("⚠️ 第二份落盘没有落在**腿2 之后、平仓之前**（%s）", pos(dumps[1]))
	}
	if !(dumps[2] > closePos) {
		t.Errorf("⚠️ 第三份落盘没有落在**平仓之后**（%s）—— "+
			"夹不住那次平仓就只剩当日累计，而累计对撮合顺序恒等", pos(dumps[2]))
	}
}

// TestAttachTradesHasOneCallSite 与 `TestAttachQuoteHasOneCallSite` 同理：
// 「补不上成交明细就整份不落盘」这条不变式只能有一个实现。
//
// ⚠️ 而它比行情那条更要紧：成交明细是这一批**判别力的来源** ——
// 没有它这一轮只答得出「逐片还是均价」，答不出 FIFO 还是 LIFO。
// 一条绕开它的落盘路径会产出**看起来齐全、而少了次序证据**的夹具。
func TestAttachTradesHasOneCallSite(t *testing.T) {
	fset := token.NewFileSet()
	n, inDump := 0, 0
	files := goFilesHere(t)
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(x ast.Node) bool {
			if c, ok := x.(*ast.CallExpr); ok && selName(c.Fun) == "AttachTrades" {
				n++
			}
			return true
		})
		if fd := findFunc(f, "dumpSlices"); fd != nil {
			ast.Inspect(fd, func(x ast.Node) bool {
				if c, ok := x.(*ast.CallExpr); ok && selName(c.Fun) == "AttachTrades" {
					inDump++
				}
				return true
			})
		}
	}
	if len(files) < 2 {
		t.Fatalf("⚠️ 只扫到 %d 个源文件 —— 本条在空转", len(files))
	}
	if n != 1 {
		t.Errorf("⚠️ `AttachTrades` 在本包被调用 %d 次（要恰好 1 次）—— "+
			"第二条路径会绕开「补不上就整份不落盘」", n)
	}
	// ⚠️ 光数「一次」不够：挪出 dumpSlices 之后计数照样是 1，
	// 而三份落盘里就会有几份不带成交明细。
	if inDump != 1 {
		t.Errorf("⚠️ `AttachTrades` 在 dumpSlices 里出现 %d 次（要 1 次）—— "+
			"它被挪出去了，于是不是每一份落盘都带成交明细", inDump)
	}
}

// sourceOf 取一个节点的源码文本（用于在 AST 上按内容匹配）。
func sourceOf(fset *token.FileSet, n ast.Node) string {
	b, err := os.ReadFile(fset.Position(n.Pos()).Filename)
	if err != nil {
		return ""
	}
	lo, hi := fset.Position(n.Pos()).Offset, fset.Position(n.End()).Offset
	if lo < 0 || hi > len(b) || lo >= hi {
		return ""
	}
	return string(b[lo:hi])
}

// TestFlattenDeferRegisteredBeforeAnyOrder 断言**收尾平仓注册在第一笔委托之前**。
//
// # ⚠️ 它来自 20260912 评审让我去找「一条能留仓的路径」—— 找到了
//
// `openOneLot` / `openOneLotResting` **在成交之后还会失败**：
// `filledPrice` 查不到持仓就报错。那一刻仓已经在账上，而 `defer` 原先
// 注册在开腿 1 **之后** ⇒ 直接 `return err`，**腿 1 留仓**，
// 且命令以错误退出 —— **看起来像「没开成」，实际是「开成了但没平」**。
//
//	⚠️ 这两件事对人的要求完全相反：前者重跑就行，后者要先去收拾。
//
// ⚠️ `-restfirst` 那条路更宽：先挂单、轮询、成交，中间每一次查询都可能超时。
//
// ⇒ 提前注册。`flattenLongToday` 在无仓时是空操作，所以提前不会误平。
func TestFlattenDeferRegisteredBeforeAnyOrder(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "slice.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "runCTPSlices" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPSlices —— 改名了，本条守卫失效")
	}
	deferPos, orderPos := token.NoPos, token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		if ds, ok := n.(*ast.DeferStmt); ok && deferPos == token.NoPos &&
			containsAll(sourceOf(fset, ds), "flattenLongToday") {
			deferPos = ds.Pos()
		}
		if c, ok := n.(*ast.CallExpr); ok && orderPos == token.NoPos {
			switch v := c.Fun.(type) {
			case *ast.SelectorExpr:
				if v.Sel.Name == "Insert" {
					orderPos = c.Pos()
				}
			case *ast.Ident:
				if v.Name == "openOneLot" || v.Name == "openOneLotResting" {
					orderPos = c.Pos()
				}
			}
		}
		return true
	})
	if deferPos == token.NoPos {
		t.Fatal("⚠️ runCTPSlices 里找不到那条 defer flattenLongToday —— " +
			"要么它被删了，要么改名了而本条守卫在空转")
	}
	if orderPos == token.NoPos {
		t.Fatal("⚠️ 一笔委托都找不到 —— 本条在空集上跑")
	}
	if deferPos > orderPos {
		t.Fatalf("⚠️⚠️ **收尾平仓注册在第一笔委托之后**（defer %s，下单 %s）—— "+
			"成交之后才失败的那条路会**留仓**，而它以错误退出，"+
			"看起来像「没开成」", fset.Position(deferPos), fset.Position(orderPos))
	}
}
