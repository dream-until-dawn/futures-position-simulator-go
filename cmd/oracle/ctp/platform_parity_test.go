package ctp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestNonWindowsStubsCoverEveryMethod 钉住 roadmap 那句
// 「非 Windows 有**同 API** 的桩」。
//
// ⚠️ 它与 `TestCrossCompiles` 查的**不是同一件事**，两条都要：
//
//	TestCrossCompiles      cmd/oracle **用到**的东西在别的平台上齐不齐
//	本条                   `ctp` 这个包**声称**的 API 在别的平台上齐不齐
//
// 一个只被包外某个将来的调用方用到的方法，缺了桩时前者**一个字都不会说** ——
// 因为 cmd/oracle 没调它，编译当然过。而 roadmap 那句是关于**包**的承诺。
//
// ⚠️ 20260910 之前 `MarketData` / `SplitSymbol` / `FarPrice` 三个都没有桩，
// 而文档里「有同 API 的桩」照写不误。**一句关于覆盖面的话，要有人去数才成立。**
func TestNonWindowsStubsCoverEveryMethod(t *testing.T) {
	win := clientMethods(t, windowsFiles(t))
	other := clientMethods(t, []string{"client_other.go"})
	if len(win) == 0 {
		t.Fatal("⚠️ 从 `_windows.go` 里一个 Client 方法都没取到 —— " +
			"取法坏了，而下面的比较会在空集上通过（那是全绿）")
	}
	var missing []string
	for _, m := range win {
		if !contains(other, m) {
			missing = append(missing, m)
		}
	}
	if len(missing) > 0 {
		t.Errorf("⚠️ 这些方法只有 Windows 有、非 Windows 没有桩：%s\n"+
			"⚠️ 后果不是编译错误 —— 包外的调用方在别的平台上会**整个包用不了**，"+
			"而 roadmap 里「有同 API 的桩」那句会变成假话（它已经当过一次假话）",
			strings.Join(missing, " "))
	}
	// ⚠️ 反向也要查：桩多出来的方法意味着 Windows 那侧**删掉了实现**而桩没跟上，
	// 于是「这个平台没实现」的错误会盖住「这个方法已经不存在了」。
	for _, m := range other {
		if !contains(win, m) {
			t.Errorf("⚠️ 桩里有 %s 而 Windows 侧没有 —— 实现被删了？桩没跟上", m)
		}
	}
}

func windowsFiles(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, "_windows.go") &&
			!strings.HasSuffix(n, "_test.go") {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		t.Fatal("⚠️ 一个 `_windows.go` 都没找到 —— 文件命名变了？本条会在空集上通过")
	}
	return out
}

// clientMethods 用 AST 取 `func (c *Client) Xxx` 的导出方法名。
//
// ⚠️ 不用 grep：签名换行、注释里出现同样的字样都会让正则多数或少数，
// 而多数会让这条守卫**永远红**，少数会让它**静默漏**。
func clientMethods(t *testing.T, files []string) []string {
	t.Helper()
	var out []string
	fset := token.NewFileSet()
	for _, f := range files {
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s：%v", f, err)
		}
		for _, d := range af.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || len(fd.Recv.List) != 1 {
				continue
			}
			star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			id, ok := star.X.(*ast.Ident)
			if !ok || id.Name != "Client" || !fd.Name.IsExported() {
				continue
			}
			out = append(out, fd.Name.Name)
		}
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
