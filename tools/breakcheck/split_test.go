package main

import "testing"

// TestSplitByBreakFiles 钉住收尾诊断的**内外之分**。
//
// ⚠️ 它是被一次**说错了原因的诊断**逼出来的：并发编辑一个
// 清单里根本没有的文件时，收尾那一句确信地报「说明有破坏没还原」，
// 并建议 `git checkout --` —— 而那会直接删掉别人正在写的东西。
//
// ⚠️ 更该记的是：那段代码**自己的注释**早就写着「只兜改了一个
// **不在清单里**的已跟踪文件」，而代码从头到尾没有区分过内外。
// 上一次给未跟踪那一支加并发豁免时，注释跟着改了，代码没跟着改。
//
//	注释说对了规则，代码做的是另一件事，而两者在文件里挨着。
func TestSplitByBreakFiles(t *testing.T) {
	set := breakFileSet([]Break{
		{File: "order/order.go"},
		{File: "view/view.go", Also: []AlsoEdit{{File: "view/position.go"}}},
	})
	for _, want := range []string{"order/order.go", "view/view.go", "view/position.go"} {
		if !set[want] {
			t.Errorf("⚠️ 清单文件集里少了 %s —— also 那一支是不是没收进来？", want)
		}
	}
	porcelain := " M order/order.go\n M docs/roadmap.md\n?? testdata/probes/new.json"
	mine, foreign := splitByBreakFiles(porcelain, set)
	if mine != " M order/order.go" {
		t.Errorf("⚠️ 清单内的改动应当只有 order/order.go，得到 %q", mine)
	}
	if foreign != " M docs/roadmap.md" {
		t.Errorf("⚠️ **清单外**的改动应当是 docs/roadmap.md，得到 %q —— "+
			"把它报成「有破坏没还原」就是一句说错了原因的诊断，"+
			"而它连带建议的 git checkout 会删掉别人正在写的东西", foreign)
	}
	// ⚠️ 判别力：两侧都要非空。只测一侧的话，一个「全算清单内」
	// 或「全算清单外」的实现都能过其中一条。
	if mine == "" || foreign == "" {
		t.Fatal("⚠️ 用例只覆盖一侧 —— 恒判内或恒判外的实现都能过")
	}
	// 未跟踪的那一行两侧都不该出现（它由另一支处理）。
	if mine != " M order/order.go" || foreign != " M docs/roadmap.md" {
		t.Error("⚠️ 未跟踪的 ?? 行混进来了")
	}
}

// TestDirtyVerdictIgnoresForeignChanges 钉住**清单外的改动不改变那个数**。
//
// ⚠️ 「未按预期 N 条」是这套体系里被引用最多的一句话。它此前可以被
// 任何一次并发写入污染成失败 —— 20260909 评审方那次的「1 条」，
// 逐条筛过之后没有任何一条破坏落在预期之外：**整个那个 1 就是收尾那一行**。
//
// 打印一句说错原因的话是一回事，**把头条数字改掉是另一回事**。
func TestDirtyVerdictIgnoresForeignChanges(t *testing.T) {
	set := breakFileSet([]Break{{File: "order/order.go"}})
	cases := []struct {
		name       string
		porcelain  string
		wantFailed bool
	}{
		{"只有清单外的并发改动", " M docs/roadmap.md", false},
		{"清单内没还原", " M order/order.go", true},
		{"两者都有", " M order/order.go\n M docs/roadmap.md", true},
		{"只有未跟踪文件", "?? testdata/probes/new.json", false},
	}
	saw := map[bool]int{}
	for _, c := range cases {
		_, _, failed := dirtyVerdict(c.porcelain, set)
		saw[failed]++
		if failed != c.wantFailed {
			t.Errorf("⚠️ 「%s」判成 failed=%v，应为 %v —— "+
				"⚠️ 清单外的改动只能打印，不能改变「未按预期 N 条」那个数："+
				"破坏改不到清单外的文件，所以那种改动只可能来自别的进程",
				c.name, failed, c.wantFailed)
		}
	}
	// ⚠️ 判别力：两种判定都要出现，否则恒真或恒假的实现能过一半用例。
	if saw[true] == 0 || saw[false] == 0 {
		t.Fatalf("⚠️ 用例只覆盖一种判定（真 %d / 假 %d）", saw[true], saw[false])
	}
}
