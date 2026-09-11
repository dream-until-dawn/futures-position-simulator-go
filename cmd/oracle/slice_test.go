package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"math"
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
