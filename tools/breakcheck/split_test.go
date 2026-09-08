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
