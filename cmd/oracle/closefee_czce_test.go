package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// TestCZCECandidatesMatchPreRegistration 把代码里的郑商所候选表钉成与**事前登记**那张表逐格相同。
//
// ⚠️ 登记在提交 43101f7（20260917 09:14），早于任何观测。这条测试存在是为了让
// 「看到结果之后改预言」在 diff 里红出来 —— 改代码里的表、或改 state.md 里的表，任何一边动了都会红。
//
// ⚠️ 比的是**渲染出来的格子文本**（按声明费率 平昨 2 / 平今 6 代入），不是另抄一份期望 map：
// 另抄一份的话，两份一起改就一起绿。
func TestCZCECandidatesMatchPreRegistration(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "state.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	if !strings.Contains(doc, "开仓 **2** / 平昨 **2** / 平今 **6**") {
		t.Fatal("⚠️ state.md 里找不到事前登记的声明费率「开仓 2 / 平昨 2 / 平今 6」—— 登记被改了，或本条在空转")
	}
	rateToday, rateYd := decimal.NewFromInt(6), decimal.NewFromInt(2)
	cell := func(tr feeTier, bold bool) string {
		v := rateYd
		if tr == tierToday {
			v = rateToday
		}
		if bold {
			return "**" + v.String() + "**"
		}
		return v.String()
	}
	// 登记表里「与 (a) 不同的格子加粗」：X1 行 b 加粗；X2 行 a、b 加粗。
	render := func(pred func(feeCandidate) feeTier, boldNames map[string]bool) string {
		var cells []string
		for _, c := range czceFeeCandidates {
			cells = append(cells, cell(pred(c), boldNames[c.Name]))
		}
		return "| " + strings.Join(cells, " | ") + " |"
	}
	x1 := render(func(c feeCandidate) feeTier { return c.X1 }, map[string]bool{"b": true})
	x2 := render(func(c feeCandidate) feeTier { return c.X2(true) }, map[string]bool{"a": true, "b": true})
	// X0 是观测之前补登记的那一行（43101f7 之后）：(e) 加粗
	x0 := render(func(c feeCandidate) feeTier { return c.X0 }, map[string]bool{"e": true})
	for name, row := range map[string]string{"X1": x1, "X2（消耗昨仓）": x2, "X0": x0} {
		if !strings.Contains(doc, row) {
			t.Errorf("⚠️ 代码里的 %s 预言渲染成「%s」，而 state.md 事前登记的那一行里没有这一串 —— "+
				"代码与登记分岔了。若是看到结果之后改的预言，那不是修正，是改答案", name, row)
		}
	}
	// 候选的名字与顺序也是登记的一部分
	var names []string
	for _, c := range czceFeeCandidates {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "a,b,d,e" {
		t.Errorf("⚠️ 候选是 %s，登记的是 a,b,d,e", got)
	}
	// X2 消耗今仓时 (d) 改预言平今 —— 登记里单独写了这一句
	for _, c := range czceFeeCandidates {
		if c.Name == "d" && c.X2(false) != tierToday {
			t.Error("⚠️ 登记写明「消耗今仓时 (d) 改预言 6」，代码里不是")
		}
	}
}

// TestCZCEExperimentsDiscriminate 钉住 X1 + X2 + X0 合起来**唯一**，以及落在两档之外时一个都不活。
//
// ⚠️ 这条测试第一版只比 X1 + X2，并且**红了**：X1、X2（消耗昨仓）都收平昨时 (d) 与 (e) 同时活着 ——
// 它抓到的是 43101f7 事前登记里那句「两个合起来唯一」是错的，发生在任何观测之前。X0 就是为此补的。
func TestCZCEExperimentsDiscriminate(t *testing.T) {
	today, yd := decimal.NewFromInt(6), decimal.NewFromInt(2)
	outcomes := []decimal.Decimal{today, yd}
	for _, o1 := range outcomes {
		a1, _, err := czceX1(o1, today, yd)
		if err != nil {
			t.Fatal(err)
		}
		for _, o2 := range outcomes {
			a2, _, err := czceX2(true, o2, today, yd)
			if err != nil {
				t.Fatal(err)
			}
			for _, o0 := range outcomes {
				a0, _, err := czceX0(o0, today, yd)
				if err != nil {
					t.Fatal(err)
				}
				if all := intersect(intersect(a1, a2), a0); len(all) > 1 {
					t.Errorf("⚠️ X1 收 %s、X2（消耗昨仓）收 %s、X0 收 %s 时仍有 %v 同时活着 —— 三个实验合起来没有唯一",
						o1, o2, o0, all)
				}
			}
		}
	}
	// 反向：只有 X1 + X2 时 (d)(e) 分不开 —— 否则 X0 就是多余的，更正登记那一段就说错了
	a1, _, _ := czceX1(yd, today, yd)
	a2, _, _ := czceX2(true, yd, today, yd)
	if both := intersect(a1, a2); len(both) != 2 || both[0] != "d" || both[1] != "e" {
		t.Errorf("X1、X2 都收平昨档时应恰好剩 [d e]（这正是要补 X0 的理由），得到 %v", both)
	}
	// 反向：单看 X1 分不开 a/d/e —— 否则 X2 就是多余的，登记里「两个合起来唯一」那句就说错了
	if a1, _, _ := czceX1(yd, today, yd); len(a1) != 3 {
		t.Errorf("X1 收平昨档时应有 a、d、e 三个活着，得到 %v", a1)
	}
	// 落在两档之外 ⇒ 一个都不活，并且说出来
	if a, why, _ := czceX1(decimal.NewFromInt(4), today, yd); len(a) != 0 || !strings.Contains(why, "谁都没预言到") {
		t.Errorf("⚠️ 收了 4（两档之外）却活着 %v，或没说「谁都没预言到」：%s", a, why)
	}
	// 两档同费率 ⇒ 拒判
	if _, _, err := czceX1(yd, yd, yd); err == nil {
		t.Error("⚠️ 平今档 = 平昨档时应当拒判 —— 每个候选都会活，看起来像全部一致")
	}
}

func intersect(a, b []string) []string {
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	var out []string
	for _, x := range b {
		if m[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// TestCloseFeeRegisteredBeforeTrading 钉住没有事前登记候选的交易所 / 实验，在下单之前就拒跑。
func TestCloseFeeRegisteredBeforeTrading(t *testing.T) {
	for _, c := range []struct {
		ex   string
		m    closeFeeMode
		want bool
	}{
		{"DCE", closeFeeBare, true}, {"DCE", closeFeeYd, true},
		{"CZCE", closeFeeBare, true}, {"CZCE", closeFeeYd, false},
		{"GFEX", closeFeeBare, false}, {"SHFE", closeFeeBare, false},
	} {
		err := closeFeeRegistered(c.ex, c.m)
		if (err == nil) != c.want {
			t.Errorf("⚠️ %s %s：放行=%v，应为 %v（%v）", c.ex, c.m, err == nil, c.want, err)
		}
	}
}
