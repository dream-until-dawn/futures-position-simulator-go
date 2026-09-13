package futsim

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNobodyParsesFixtureNotes 断言**没有代码拿夹具的 `note` 内容做判断**。
//
// # ⚠️ 它来自评审 20260912 顺带提的一句约束，而那句约束当时没有守卫
//
// 评审给 `ctp-slices` 三份落盘的注记盲区（破坏 406）找减轻时指出：
// 三份在**数据**上两两分得开（Position / OpenVolume / CloseVolume），
// 「只信 `note` 的人会被骗，核数据的人骗不了」——
//
//	⇒ **`note` 是自由文本，出现在夹具里就该当成注释而不是字段**；
//	将来读夹具的代码不该去 parse `note`。
//
// 我当时登记为「可以有守卫，而这一批没写」。20260913 无盘这一天补上。
//
// # ⚠️ 判据：什么算「拿它做判断」
//
// 允许：声明字段、透传赋值（`Note: raw.Note`）、打印（fmt / t.Log / logf）。
// 不允许，因为那是在**用自由文本的内容决定行为**：
//
//	`.Note` 作 `==` / `!=` 的操作数，或作 switch 的 tag
//	`.Note` 作 strings / regexp / strconv / bytes / json 包里函数的实参
//	`m["note"]` 这种按键取值（从通用 map 里把注记当数据读出来）
//
// ⚠️ 它是**近似**判据：把 `.Note` 先赋给一个局部变量再去比，本条抓不到。
// 那一格登记在此，不假装守住了。
func TestNobodyParsesFixtureNotes(t *testing.T) {
	// ⚠️ **先证明探测器抓得住**，再去扫仓库。
	// 否则「整个仓库零命中」有两种读法 —— 没人 parse，或者探测器从来不会响 ——
	// 而两者在全绿时长得一模一样。
	const synthetic = `package x
import "strings"
type F struct{ Note string }
func a(f F, m map[string]any) bool {
	_ = m["note"]
	switch f.Note { case "x": }
	return strings.Contains(f.Note, "①") || f.Note == "y"
}`
	if got := noteParses(t, "synthetic.go", synthetic); len(got) != 4 {
		t.Fatalf("⚠️ 探测器在合成代码上应抓到 4 处（m[\"note\"] / switch / strings.Contains / ==），"+
			"实际 %d 处：%v —— **探测器坏了，下面扫仓库的结果不可信**", len(got), got)
	}

	scanned, selectors := 0, 0
	var hits []string
	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		selectors += strings.Count(string(b), ".Note")
		// ⚠️ 本文件自己的合成代码是**刻意**违规的样本，跳过它。
		if filepath.Base(path) == "note_parse_test.go" {
			return nil
		}
		hits = append(hits, noteParses(t, path, string(b))...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 反空转：扫到的文件太少，或者一个 `.Note` 都没见过，都说明走路的范围不对。
	if scanned < 50 {
		t.Fatalf("⚠️ 只扫到 %d 个 .go 文件（下界 50）—— 目录范围不对，本条在空集上跑", scanned)
	}
	if selectors == 0 {
		t.Fatal("⚠️ 全仓库一个 `.Note` 都没见过 —— 字段改名了？本条守的东西已经不存在")
	}
	for _, h := range hits {
		t.Errorf("⚠️ %s —— **拿夹具注记的内容做了判断**。`note` 是自由文本，"+
			"阶段/身份请从数据判（例如 ctp-slices 三份靠 Position/OpenVolume/CloseVolume 就分得开）", h)
	}
	t.Logf("ⓘ 扫了 %d 个 .go 文件，见到 %d 处 `.Note`，违规 %d 处", scanned, selectors, len(hits))
}

// noteParses 在一份源码里找「拿 note 内容做判断」的地方。
func noteParses(t *testing.T, name, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		// ⚠️ 解析不了的文件（生成的、带 build tag 的实验文件）不算违规，
		// 但**要说出来** —— 静默跳过会让「没扫到」伪装成「扫过了」。
		t.Logf("ⓘ 解析不了 %s，跳过：%v", name, err)
		return nil
	}
	isNote := func(e ast.Expr) bool {
		sel, ok := e.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Note"
	}
	parsePkgs := map[string]bool{"strings": true, "regexp": true, "strconv": true, "bytes": true, "json": true}
	var out []string
	at := func(n ast.Node, what string) {
		out = append(out, fset.Position(n.Pos()).String()+"："+what)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.BinaryExpr:
			if (v.Op == token.EQL || v.Op == token.NEQ) && (isNote(v.X) || isNote(v.Y)) {
				at(v, "`.Note` 作比较操作数")
			}
		case *ast.SwitchStmt:
			if v.Tag != nil && isNote(v.Tag) {
				at(v, "`.Note` 作 switch 的 tag")
			}
		case *ast.CallExpr:
			sel, ok := v.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || !parsePkgs[pkg.Name] {
				return true
			}
			for _, a := range v.Args {
				if isNote(a) {
					at(v, "`.Note` 作 "+pkg.Name+"."+sel.Sel.Name+" 的实参")
				}
			}
		case *ast.IndexExpr:
			if lit, ok := v.Index.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil && s == "note" {
					at(v, "按键 \"note\" 取值")
				}
			}
		}
		return true
	})
	return out
}
