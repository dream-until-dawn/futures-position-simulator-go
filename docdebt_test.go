package futsim

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docDebtRow 取 docs/state.md 里 `doc_debt` 表的第一列（反引号里的标识符）。
var docDebtRow = regexp.MustCompile("^\\| `([A-Za-z_][A-Za-z0-9_.]*)` \\|")

// exportedNamesInModule 收集主模块（不含 cmd/oracle 嵌套模块与测试文件）里全部**已经存在**的导出标识符：
// 顶层的 type / func / const / var、方法名、结构体字段名。
func exportedNamesInModule(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	add := func(name, where string) {
		if name == "" || !ast.IsExported(name) {
			return
		}
		if _, ok := out[name]; !ok {
			out[name] = where
		}
	}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", ".git", "oracle": // oracle 是嵌套模块，不算本模块的导出面
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		for _, decl := range f.Decls {
			switch dd := decl.(type) {
			case *ast.FuncDecl:
				add(dd.Name.Name, path)
			case *ast.GenDecl:
				for _, spec := range dd.Specs {
					switch sp := spec.(type) {
					case *ast.TypeSpec:
						add(sp.Name.Name, path)
						if st, ok := sp.Type.(*ast.StructType); ok {
							for _, fld := range st.Fields.List {
								for _, n := range fld.Names {
									add(n.Name, path)
								}
							}
						}
					case *ast.ValueSpec:
						for _, n := range sp.Names {
							add(n.Name, path)
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 50 {
		t.Fatalf("⚠️ 只收集到 %d 个导出标识符 —— 遍历写错了，本条在空转", len(out))
	}
	return out
}

// TestDocDebtIdentifiersAreStillMissing：`doc_debt` 表里的标识符必须**确实还不存在**于代码里。
//
// ⚠️ 这张表此前没有任何机械核对：`Restore` 那一行 20260915 随 F5 落地、写着「v0.9.0 未落地」挂了两天，
// 撞见它的是人不是守卫（state.md 的 `doc_debt` 段落自己写了这件事，并登记「另做」）。
// F9b 落地时 `Bar` / `Advance` 两行同样会过期 ⇒ 这一条补上，以后落地即红。
func TestDocDebtIdentifiersAreStillMissing(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("docs", "state.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "## `doc_debt`") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("⚠️ state.md 里找不到 doc_debt 那一节 —— 改名或被挪走，本条失效")
	}
	var names []string
	inTable := false
	for _, l := range lines[start:] {
		if strings.HasPrefix(l, "## ") && !strings.HasPrefix(l, "## `doc_debt`") {
			break
		}
		if strings.HasPrefix(l, "| 标识符 ") {
			inTable = true
			continue
		}
		if !inTable {
			continue
		}
		if m := docDebtRow.FindStringSubmatch(l); m != nil {
			names = append(names, m[1])
		}
	}
	have := exportedNamesInModule(t)
	for _, n := range names {
		base := n
		if i := strings.LastIndex(base, "."); i >= 0 {
			base = base[i+1:]
		}
		if where, ok := have[base]; ok {
			t.Errorf("⚠️ `doc_debt` 里写着 %s 还不存在，而它已经在 %s 里了 —— 那一行过期了，删掉它（文档欠的债还了就要销账）", n, where)
		}
	}
	t.Logf("ⓘ doc_debt 当前 %d 项：%v", len(names), names)
}
