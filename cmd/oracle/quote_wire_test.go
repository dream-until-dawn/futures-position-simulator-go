package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestQuoteFailureBlocksTheWrite 钉住一条**只由语句顺序保证**的不变式：
//
//	补行情失败 ⇒ **整份截面不落盘**
//
// # ⚠️ 为什么它必须有守卫
//
// 这条不变式此刻的全部保障，是 `runCTPParams` 里 `AttachQuote` 的 error
// 在 `fx.Write` **之前** return。⚠️ 谁把 `fx.Write` 挪上去一行、
// 或者把那个 `return` 降成一句日志，**不会有任何东西红**。
//
// 而它的失败方式正是错误信息里自己写的那一种：
// **一份缺了 quotes 的夹具与一份本来就不带 quotes 的分不开。**
// ⇒ 一份半截的截面会安静地进 `testdata/`，然后被后来的人当成「那天没抓行情」——
// 而这批夹具是拿去当**证据**的（§6.8 的保证金基准就是从单份夹具上得出的）。
//
// # ⚠️ 这条的形状比 73 再往前一格
//
//	72    验证做过了，只写在注释里    ⇒ 下一个人以为验过了
//	73    盲区写清楚了，没安排动作    ⇒ 下一个人以为被安排了
//	本条  不变式实现对了、理由写在**错误信息**里，没有守卫 ⇒ 读代码的人以为它被保护着
//
// **那句错误信息写得越好，越像已经被保护了。**（评审 20260910 提出，
// 引的是我自己在 73 里写的「文字越好，替代得越彻底」。）
//
// ⚠️ 技术与 `TestSendIsTheOnlyPathToInsert` 同源：读源码断言**顺序**，
// 而不是断言某次运行的结果 —— 结构守卫在「有人写出第二条路径」那一刻就红，
// 不必等那条路径被走到。
func TestQuoteFailureBlocksTheWrite(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := findFunc(f, "runCTPParams")
	if fn == nil {
		t.Fatal("⚠️ main.go 里找不到 runCTPParams —— 改名了？本条会在空集上跑（那是全绿）")
	}

	var quoteIf *ast.IfStmt
	var writePos token.Pos
	calls := 0
	ast.Inspect(fn, func(n ast.Node) bool {
		if ifs, ok := n.(*ast.IfStmt); ok && mentions(ifs.Init, "AttachQuote") {
			quoteIf, calls = ifs, calls+1
		}
		if c, ok := n.(*ast.CallExpr); ok && selName(c.Fun) == "Write" && writePos == token.NoPos {
			writePos = c.Pos()
		}
		return true
	})
	if calls != 1 {
		t.Fatalf("⚠️ runCTPParams 里 `if err := …AttachQuote(…); err != nil` 出现 %d 次（要恰好 1 次）"+
			" —— 形状变了，下面的断言查不到它要查的东西", calls)
	}
	if writePos == token.NoPos {
		t.Fatal("⚠️ runCTPParams 里找不到 `.Write(` —— 落盘那一步改名了？本条不再有意义")
	}

	// ⚠️ 一、顺序：补行情必须在落盘**之前**。
	if quoteIf.Pos() > writePos {
		t.Errorf("⚠️ `AttachQuote` 出现在 `.Write` **之后**（%d > %d）—— "+
			"于是补行情失败时**半份截面已经落盘了**，而它与一份本来就不带 quotes 的分不开",
			quoteIf.Pos(), writePos)
	}
	// ⚠️ 二、失败必须 return，不许降级成日志。
	//    ⚠️ 这一条不能省：顺序对了而错误被吞掉，落盘照样发生，
	//    而**只查顺序的守卫在那种改法下一个字都不会说**。
	if !hasReturn(quoteIf.Body) {
		t.Errorf("⚠️ 补行情失败那一支里**没有 return** —— 错误被吞掉，"+
			"落盘照样发生。⚠️ 一句日志与一次拦截在磁盘上的区别是：前者留下半份证据")
	}
}

func findFunc(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name.Name == name {
			return fd
		}
	}
	return nil
}

func mentions(n ast.Node, name string) bool {
	if n == nil {
		return false
	}
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

func selName(e ast.Expr) string {
	if s, ok := e.(*ast.SelectorExpr); ok {
		return s.Sel.Name
	}
	return ""
}

func hasReturn(b *ast.BlockStmt) bool {
	if b == nil {
		return false
	}
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		if _, ok := n.(*ast.ReturnStmt); ok {
			found = true
		}
		return !found
	})
	return found
}

// ⚠️ 顺带：`AttachQuote` 在全包只该被调用一次。
// 第二处调用会绕开上面那两条断言 —— 与 `TestSendIsTheOnlyPathToInsert`
// 防的是同一件事：**不给第二条到达路径**。
func TestAttachQuoteHasOneCallSite(t *testing.T) {
	fset := token.NewFileSet()
	n := 0
	files := goFilesHere(t)
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(x ast.Node) bool {
			if c, ok := x.(*ast.CallExpr); ok && selName(c.Fun) == "AttachQuote" {
				n++
			}
			return true
		})
	}
	if len(files) < 2 {
		t.Fatalf("⚠️ 只扫到 %d 个源文件 —— 本条在空转", len(files))
	}
	if n != 1 {
		t.Errorf("⚠️ `AttachQuote` 在本包被调用 %d 次（要恰好 1 次）—— "+
			"第二条调用路径会绕开「失败则不落盘」那两条断言", n)
	}
}

func goFilesHere(t *testing.T) []string {
	t.Helper()
	all, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range all {
		if !strings.HasSuffix(n, "_test.go") {
			out = append(out, n)
		}
	}
	return out
}
