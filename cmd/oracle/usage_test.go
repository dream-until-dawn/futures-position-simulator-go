package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// dispatchNames 从 probe/runner.go 的 `switch exp` 里取出全部实验名。
//
// ⚠️ 用 AST 而不是 grep `case "`：这个包里别处也有字符串 switch，
// 而一个多抓了几个名字的清单会让下面那条守卫**永远红**，
// 永远红的守卫和永远绿的一样会被无视。
func dispatchNames(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "probe/runner.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		// 只认 `switch exp {` 这一个。
		id, ok := sw.Tag.(*ast.Ident)
		if !ok || id.Name != "exp" {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, e := range cc.List {
				bl, ok := e.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				out[strings.Trim(bl.Value, `"`)] = true
			}
		}
		return false
	})
	if len(out) == 0 {
		t.Fatal("⚠️ 从 probe/runner.go 里一个实验名都没取到 —— " +
			"分派表的形状变了，下面每一条断言都在空集上跑（那会全绿）")
	}
	return out
}

// usageSections 把 usage() 里的实验清单按小节拆开。
//
// 返回两张表：能跑的、明说「尚未实现」的。⚠️ 两者必须分开，
// 因为它们的判据是**相反**的：前者必须在分派表里，后者必须不在。
func usageSections(t *testing.T) (runnable, unimplemented map[string]string) {
	t.Helper()
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	// usage() 的正文是一段裸字符串字面量。
	src := string(b)
	i := strings.Index(src, "`oracle —— ")
	if i < 0 {
		t.Fatal("⚠️ 在 main.go 里找不到 usage 的裸字符串 —— 它的写法变了，本条守卫失效")
	}
	j := strings.Index(src[i+1:], "`")
	if j < 0 {
		t.Fatal("⚠️ usage 的裸字符串没有结尾反引号")
	}
	body := src[i+1 : i+1+j]

	runnable, unimplemented = map[string]string{}, map[string]string{}
	// 形如 `  status           连通性自检（……）`：两个空格缩进 + 名字 + 空白 + 说明。
	entry := regexp.MustCompile(`^  ([a-z][a-z0-9-]*)\s{2,}(\S.*)$`)
	cur := runnable
	sawUnimplemented := false
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "尚未实现") {
			cur, sawUnimplemented = unimplemented, true
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), ":") ||
			strings.HasSuffix(strings.TrimSpace(line), "：") {
			// 别的小节标题（「实验名称（当日即可跑）:」「需要昨仓……」）回到能跑的那张表。
			if !sawUnimplemented {
				cur = runnable
			}
			continue
		}
		m := entry.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		cur[m[1]] = m[2]
	}
	if len(runnable) == 0 {
		t.Fatal("⚠️ 从 usage 里一个能跑的实验都没解析出来 —— 解析坏了，本条在空集上跑")
	}
	return runnable, unimplemented
}

// TestUsageListsEveryExperiment 断言 `oracle probe -exp` 的清单与分派表**互相覆盖**。
//
// ⚠️ 这不是文档洁癖。这个清单是**唯一**告诉人「有哪些实验可跑」的地方 ——
// 而实验的价值全在于「跑过没有」。一个存在却没被列出来的实验，
// 与一个不存在的实验，对下一个来跑它的人（包括几周后的我自己）完全一样。
//
// 而反过来那一半更要紧：列在「尚未实现」里却其实已经能跑的名字，
// 会让人以为某块证据还没取，于是**重复去取**，或者更糟 ——
// 在报告里写「此项未测」，而它其实测过了。
func TestUsageListsEveryExperiment(t *testing.T) {
	dispatch := dispatchNames(t)
	runnable, unimplemented := usageSections(t)

	// 别名：同一个实验挂两个名字，只要求其中之一被列出。
	// ⚠️ 逐个写死并注明理由，不做前缀匹配 —— 那会把将来真正的漏网也一起吸收掉。
	aliasOf := map[string]string{
		"profit-price": "margin-price", // 同一次观测的两面，见 expMarginPrice 说明
	}

	var missing []string
	for name := range dispatch {
		if runnable[name] != "" {
			continue
		}
		if a, ok := aliasOf[name]; ok && runnable[a] != "" {
			continue
		}
		if why, ok := unimplemented[name]; ok {
			t.Errorf("⚠️ %q 列在「尚未实现」里（%s），但分派表里**能跑** —— "+
				"这会让人以为这块证据还没取，于是重复去取，或在报告里写「此项未测」", name, why)
			continue
		}
		missing = append(missing, name)
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("⚠️ 这 %d 个实验能跑却**没写进 usage**：%s\n"+
			"   一个存在却没被列出来的实验，与一个不存在的实验，"+
			"对下一个来跑它的人完全一样。", len(missing), strings.Join(missing, " "))
	}

	for name, why := range runnable {
		if dispatch[name] {
			continue
		}
		if a, ok := aliasOf[name]; ok && dispatch[a] {
			continue
		}
		t.Errorf("⚠️ usage 里列着 %q（%s），但分派表里**没有** —— "+
			"照着文档敲会得到「未实现的实验」", name, why)
	}

	for name := range unimplemented {
		if dispatch[name] {
			continue // 上面已经报过
		}
		t.Logf("ⓘ %s：确实尚未实现（%s）", name, unimplemented[name])
	}
}
