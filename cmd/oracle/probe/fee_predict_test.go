package probe

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func mk(pairs ...[2]float64) []row {
	var rs []row
	for _, p := range pairs {
		fee, pre := p[0], p[1]
		rs = append(rs, row{fee: fee, pre: pre, rate: fee / pre})
	}
	return rs
}

// TestClassifyFeeMode 穷举收法分类的每一条分支。
//
// ⚠️ 这条判据此前只活在实验的运行时路径上 —— 要连网、要有持仓、
// 要在交易时段跑，也就是它**几乎不会被执行到**，更不会被穷举。
// 一条几乎不被执行的判据，写错了不会有任何动静。
func TestClassifyFeeMode(t *testing.T) {
	cases := []struct {
		name string
		rs   []row
		want feeMode
	}{
		{
			// 实测 m：三个月份费额都是 1.5，昨结 3404/3338/3019 各不相同。
			"按手（实测 m）",
			mk([2]float64{1.5, 3404}, [2]float64{1.5, 3338}, [2]float64{1.5, 3019}),
			feeByLot,
		},
		{
			// 实测 rb：费额 0.31/0.3158/0.3189，昨结 3100/3158/3189，商恒为 0.0001。
			"按额（实测 rb）",
			mk([2]float64{0.31, 3100}, [2]float64{0.3158, 3158}, [2]float64{0.3189, 3189}),
			feeByMoney,
		},
		{"只有一个月份", mk([2]float64{1.5, 3404}), feeUndecidable},
		{
			// ⚠️ 昨结相同时两类**都**成立 —— 那等于没分开，必须判「判不了」。
			// 这一条是本测试里最要紧的：它是唯一一个「两个分支都会命中」的输入，
			// 而先判哪一支纯属实现顺序，静默取其一就会得到一个看起来确定的答案。
			"昨结相同：两类都成立",
			mk([2]float64{1.5, 3404}, [2]float64{1.5, 3404}),
			feeUndecidable,
		},
		{
			// 费额不同、商也不同 —— 两类都不成立。
			"异常：两类都不成立",
			mk([2]float64{1.5, 3404}, [2]float64{2.0, 3338}),
			feeAnomalous,
		},
		{
			// 浮点噪声不该把「相同」判成「不同」。
			"按额，带 float64 噪声",
			mk([2]float64{0.31, 3100}, [2]float64{0.3158 + 1e-13, 3158}),
			feeByMoney,
		},
	}
	if len(cases) != 6 {
		t.Fatalf("用例 %d 条，应为 6 —— 增删了就同步改这个数", len(cases))
	}
	// ⚠️ 判别力守卫：四个结果每一个都要至少出现一次，
	// 否则「分类器能分类」这句话在本测试里没被完整考验过。
	seen := map[feeMode]int{}
	for _, c := range cases {
		got, why := classifyFeeMode(c.rs)
		if got != c.want {
			t.Errorf("⚠️ %s：判为 %d，应为 %d（理由：%s）", c.name, got, c.want, why)
		}
		if why == "" {
			t.Errorf("%s：分类没给理由", c.name)
		}
		seen[c.want]++
	}
	for _, m := range []feeMode{feeUndecidable, feeByLot, feeByMoney, feeAnomalous} {
		if seen[m] == 0 {
			t.Errorf("⚠️ 结果 %d 一次都没出现 —— 分类器的这一支没被考验过", m)
		}
	}
}

// TestFeeUndecidableIsTheZeroValue 断言「判不了」是零值。
//
// ⚠️ 一个默认落进「按额」或「按手」的零值，会让判不了的品种被静默归类，
// 而归类结果看起来和真判出来的一模一样。
func TestFeeUndecidableIsTheZeroValue(t *testing.T) {
	var m feeMode
	if m != feeUndecidable {
		t.Errorf("⚠️ feeMode 的零值不是「判不了」")
	}
}

// TestClassifierIsWiredToProduction 断言纯函数**真的被生产路径调用**。
//
// ⚠️ 这条不是形式主义。本仓库踩过一次：把判定抽成纯函数、写好穷举测试，
// 却忘了把它接回去 —— 评审在生产那条 if 上加 `if false` 都是绿的，
// 因为生产路径根本没走这个函数（方法论第 28 条）。
//
// 用 AST 查而不是 grep：grep 会被注释里的函数名骗过去，
// 而注释里恰恰到处都是这个名字。
func TestClassifierIsWiredToProduction(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "exp_fee_predict.go", nil, 0) // 0 = 丢掉注释
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	inExperiment := false
	ast.Inspect(f, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok {
			inExperiment = fd.Name.Name == "expFeePredict"
			return true
		}
		if !inExperiment {
			return true
		}
		if ce, ok := n.(*ast.CallExpr); ok {
			if id, ok := ce.Fun.(*ast.Ident); ok && id.Name == "classifyFeeMode" {
				calls++
			}
		}
		return true
	})
	if calls == 0 {
		t.Errorf("⚠️ classifyFeeMode 在 expFeePredict 里一次都没被调用 —— " +
			"判定抽出来了却没接回去，穷举测试测的是一段死代码")
	}
}

// TestSpreadOfIsOrderIndependent 断言极差不依赖输入顺序。
func TestSpreadOfIsOrderIndependent(t *testing.T) {
	pick := func(rw row) float64 { return rw.fee }
	a := spreadOf(mk([2]float64{1, 10}, [2]float64{5, 10}, [2]float64{3, 10}), pick)
	b := spreadOf(mk([2]float64{5, 10}, [2]float64{3, 10}, [2]float64{1, 10}), pick)
	if a != b || a != 4 {
		t.Errorf("极差应为 4 且与顺序无关，得到 %v / %v", a, b)
	}
}

// TestProductOfHandlesCZCE 断言品种切分不假设年月位数。
func TestProductOfHandlesCZCE(t *testing.T) {
	for _, c := range []struct{ in, product, month string }{
		{"rb2701", "rb", "2701"},
		{"AP610", "AP", "610"}, // 郑商所三位年月
		{"cs2701", "cs", "2701"},
		{"m2701", "m", "2701"},
	} {
		p, m := productOf(c.in)
		if p != c.product || m != c.month {
			t.Errorf("%s 切成 %q/%q，应为 %q/%q", c.in, p, m, c.product, c.month)
		}
	}
	// ⚠️ 反例守卫：若按固定四位切，AP610 会被切成 "A"/"P610"。
	if p, _ := productOf("AP610"); strings.HasPrefix(p, "A") && p != "AP" {
		t.Errorf("⚠️ 郑商所三位年月被按四位切了：得到品种 %q", p)
	}
}
