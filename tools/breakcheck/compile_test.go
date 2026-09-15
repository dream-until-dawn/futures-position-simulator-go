package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCompileFailureIsItsOwnVerdict 钉住「破坏编译不过」是单独的判定：期望红、期望绿两种破坏都走它，-compile 模式也报它。
//
// ⚠️ 此前编译失败在期望红时判「红错了理由」（与守卫红在别处混在一起），在期望绿时判「⚠️ 竟然红了」——
// 后者读起来像盲区消失了，是个假好消息。四次同形：455 / 502 / 522 / 550。
func TestCompileFailureIsItsOwnVerdict(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("go.mod", "module tmpprobe\n\ngo 1.21\n")
	target := write("x.go", "package tmpprobe\n\nfunc F() int {\n\tv := 1\n\treturn v\n}\n")
	write("x_test.go", "package tmpprobe\n\nimport \"testing\"\n\n"+
		"func TestReal(t *testing.T) {\n\tif F() != 1 {\n\t\tt.Error(\"F 不是 1\")\n\t}\n}\n")

	// 替换后 v 没用上 ⇒ 编译失败 —— 正是那四次的形状
	unusedVar := Break{Name: "编译不过", Dir: dir, File: target, Pkg: "./", Test: "TestReal",
		Old: "\treturn v", New: "\treturn 0", Want: "F 不是 1"}
	// 仍然用到 v ⇒ 编译得过、断言失败
	realBreak := unusedVar
	realBreak.Name, realBreak.New = "编译得过", "\treturn v + 1"

	for _, c := range []struct {
		name string
		b    Break
	}{
		{"期望红", unusedVar},
		{"期望绿", func() Break { g := unusedVar; g.Expect, g.Why, g.Want = "green", "演示", ""; return g }()},
	} {
		if c.b.Expect == "" {
			c.b.Expect = "red"
		}
		if v, d := run(c.b); v != "破坏编译不过" {
			t.Errorf("⚠️ %s 的破坏编译不过，判定却是 %q（%s）", c.name, v, d)
		}
	}
	realBreak.Expect = "red"
	if v, d := run(realBreak); v != "红对了" {
		t.Errorf("反向：编译得过、断言失败的破坏要判红对了，得到 %q（%s）", v, d)
	}

	if v, d := tryCompile(unusedVar); v != "破坏编译不过" || !strings.Contains(d, "改 new") {
		t.Errorf("⚠️ -compile 模式没把编译不过的破坏报出来：%q（%s）", v, d)
	}
	if v, d := tryCompile(realBreak); v != "编译通过" {
		t.Errorf("反向：-compile 模式对编译得过的破坏要报编译通过，得到 %q（%s）", v, d)
	}
	// ⚠️ 被测测试的日志里引用了别的 go test 输出（带缩进）⇒ 不是编译失败（567 第一次跑撞上的误报）
	quoted := `=== RUN   TestX
    x_test.go:9: 内层输出：
        FAIL	tmpprobe [build failed]
--- FAIL: TestX (0.01s)
FAIL
FAIL	some/pkg	0.3s
`
	if buildFailed(quoted) {
		t.Error("⚠️ 日志里缩进引用的 [build failed] 被当成了编译失败")
	}
	if !buildFailed(`# some/pkg [some/pkg.test]
.\x.go:4:2: declared and not used: v
FAIL	some/pkg [build failed]
FAIL
`) {
		t.Error("反向：顶格的 FAIL<Tab>包 [build failed] 要认出来")
	}

	// 还原：两次都要把 x.go 放回原样
	if b, _ := os.ReadFile(target); !strings.Contains(string(b), "\treturn v\n") {
		t.Errorf("⚠️ 跑完之后 x.go 没还原：%q", b)
	}
}
