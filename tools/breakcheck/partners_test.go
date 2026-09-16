package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// testNamesOf 把 test 字段按 `|` 拆成集合（breakcheck 会把整串包进 `^(…)$`）。
func testNamesOf(b Break) map[string]bool {
	out := map[string]bool{}
	for _, n := range strings.Split(b.Test, "|") {
		if n = strings.TrimSpace(n); n != "" {
			out[n] = true
		}
	}
	return out
}

// pairs 判两条破坏算不算一对：同一个包（dir + pkg），且点名的测试有交集。
//
// ⚠️ 判据里**必须**带 dir + pkg：只按测试名相等配过一次，
// 结果 131 配在了一个「在那个包里并不存在」的名字上 —— 配对本身是空的（state.md 20260910 更正）。
func pairs(green, red Break) bool {
	// ⚠️ pkg 在清单里有 "." 与 "./" 两种写法，指的是同一个包
	// （breakcheck 自己也是 filepath.Clean 之后再拼路径的）。
	// 不归一化的话，两条配得上的破坏会因为写法不同被判成孤儿 —— 而那是一句假的告警。
	if filepath.Clean(green.Dir) != filepath.Clean(red.Dir) ||
		filepath.Clean(green.Pkg) != filepath.Clean(red.Pkg) {
		return false
	}
	g := testNamesOf(green)
	for n := range testNamesOf(red) {
		if g[n] {
			return true
		}
	}
	return false
}

// greenOrphans 列出**没有红搭档**的 expect=green 破坏。
func greenOrphans(breaks []Break) []Break {
	var out []Break
	for _, g := range breaks {
		if g.Expect != "green" {
			continue
		}
		found := false
		for _, r := range breaks {
			if r.Expect == "red" && pairs(g, r) {
				found = true
				break
			}
		}
		if !found {
			out = append(out, g)
		}
	}
	return out
}

// TestEveryGreenBreakHasARedPartner 钉住**每一条 `expect=green` 都有一条红搭档**。
//
// # 为什么这件事必须有守卫
//
// `expect=green` 说的是「这一处破坏了也**应该**绿」—— 它是一句**关于盲区的断言**，
// 而一条盲区声明只有在「同一处别的破坏确实会红」时才有意义：
// 否则「破坏了还绿」与「这个包里的测试根本不测这块」在结果上完全同形，
// ⚠️ 而后者会让一整片没有覆盖的代码，看起来像是一片被仔细论证过的盲区。
//
// # ⚠️ 它是被「一个数不被核，就会一直被引用」逼出来的
//
// 20260910 手数过一次：24 条 green、24 条有搭档、0 条孤儿，而那个 24 **此前还是错的**
// （按 test 字段字符串相等数的，其中 131 配在一个那个包里并不存在的名字上，配对是空的）。
// 数对了之后这件事就**没有再被数过** —— 到 20260916 已经有 **3 条**新的孤儿
// （393 / 409 / 527）悄悄进来，而每一次全量扫描都报「37 条如预期仍然绿」，一次都没喊过。
//
//	一次人手数出来的零，与一条永远为零的守卫，在报告里长得一模一样。
//
// # 判据
//
// 同一个包（dir + pkg）里、点名的测试有交集，就算一对。⚠️ 不要求是**同一处锚点**的正反面：
// 红搭档要证明的是「这条测试在这个包里确实会红」，不是「这一行的反面」。
func TestEveryGreenBreakHasARedPartner(t *testing.T) {
	// ⚠️ 先证明判据本身有判别力：一个恒返回空的 greenOrphans 会让下面整份清单全绿。
	for _, c := range []struct {
		name   string
		in     []Break
		orphan int
	}{
		{"同包同测试名 ⇒ 成对", []Break{
			{Name: "g", Dir: "", Pkg: "./", Test: "TestA", Expect: "green"},
			{Name: "r", Dir: "", Pkg: "./", Test: "TestA", Expect: "red"},
		}, 0},
		{"包不同 ⇒ 不成对", []Break{
			{Name: "g", Dir: "", Pkg: "./view/", Test: "TestA", Expect: "green"},
			{Name: "r", Dir: "", Pkg: "./", Test: "TestA", Expect: "red"},
		}, 1},
		{"dir 不同 ⇒ 不成对（cmd/oracle 是嵌套模块）", []Break{
			{Name: "g", Dir: "cmd/oracle", Pkg: "./", Test: "TestA", Expect: "green"},
			{Name: "r", Dir: "", Pkg: "./", Test: "TestA", Expect: "red"},
		}, 1},
		{"测试名不相交 ⇒ 不成对", []Break{
			{Name: "g", Dir: "", Pkg: "./", Test: "TestA", Expect: "green"},
			{Name: "r", Dir: "", Pkg: "./", Test: "TestB", Expect: "red"},
		}, 1},
		{"pkg 写成 . 与 ./ 是同一个包 ⇒ 成对", []Break{
			{Name: "g", Dir: "cmd/oracle", Pkg: ".", Test: "TestA", Expect: "green"},
			{Name: "r", Dir: "cmd/oracle", Pkg: "./", Test: "TestA", Expect: "red"},
		}, 0},
		{"多名字里有一个对上 ⇒ 成对", []Break{
			{Name: "g", Dir: "", Pkg: "./", Test: "TestA|TestB", Expect: "green"},
			{Name: "r", Dir: "", Pkg: "./", Test: "TestB", Expect: "red"},
		}, 0},
		{"搭档也是 green ⇒ 不算", []Break{
			{Name: "g", Dir: "", Pkg: "./", Test: "TestA", Expect: "green"},
			{Name: "g2", Dir: "", Pkg: "./", Test: "TestA", Expect: "green"},
		}, 2},
	} {
		if n := len(greenOrphans(c.in)); n != c.orphan {
			t.Errorf("⚠️ %s：孤儿数 %d，应为 %d", c.name, n, c.orphan)
		}
	}

	var breaks []Break
	if err := json.Unmarshal(registryJSON, &breaks); err != nil {
		t.Fatalf("⚠️ 读不了破坏清单：%v", err)
	}
	greens := 0
	for _, b := range breaks {
		if b.Expect == "green" {
			greens++
		}
	}
	// ⚠️ 没有 green 时上面的循环一条也不跑，而那同样是一片全绿。
	if greens == 0 {
		t.Fatal("⚠️ 清单里一条 expect=green 都没有 —— 本条此刻什么都没在守")
	}
	orphans := greenOrphans(breaks)
	for _, g := range orphans {
		t.Errorf("⚠️ %q 是一条没有红搭档的 expect=green（%s 包 %s，点名 %s）——\n"+
			"    「破坏了还绿」与「这个包里的测试根本不测这块」在结果上同形，\n"+
			"    要么在同一个包里给它配一条会红的破坏，要么这条 green 本身该改成 red",
			g.Name, g.Dir, g.Pkg, g.Test)
	}
	t.Logf("ⓘ expect=green %d 条，孤儿 %d 条（共 %d 条破坏）", greens, len(orphans), len(breaks))
}
