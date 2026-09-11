package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestRoadmapListsEveryExport 钉住 roadmap 里的导出面清单。
//
// # ⚠️ 它防的是门禁第 ④ 条**自己**失效
//
// 评审门禁查「导出面变更有没有记进 roadmap」。⚠️ 而 20260910 那一批里
// **七项一个都没记**（`Insert` / `Cancel` / `Order` / `MarketData` /
// `SplitSymbol` / `FarPrice` / `OrderState`），清单还停在 P2 的样子。
// 更糟的是我在给评审的回信里说过它「已经改了」——**而我当时没有核过**。
//
// > 一条靠人「记得同步」的清单，会在三个地方同时腐烂：文档、汇报、以及
// > 「我以为我改了」。
//
// ⚠️ 还有一个更安静的形态：`SeedOrderSeq` 在 `08b4cf2..0b98aee` 之间
// **存在过又被删掉，从未进过清单** —— 进来又出去，清单上一个字都没有。
//
// # 两个方向都查
//
//	包里有、清单没有   → 新增的导出面没记（门禁 ④ 的正面）
//	清单有、包里没有   → 删掉的导出面没撤（清单变成考古）
func TestRoadmapListsEveryExport(t *testing.T) {
	for _, c := range []struct{ heading, dir string }{
		{"#### `cmd/oracle/ctp`", "ctp"},
		{"#### `cmd/oracle/safety`", "safety"},
	} {
		words := sectionWords(t, c.heading)
		actual := packageExports(t, c.dir)
		if len(words) < 5 || len(actual) == 0 {
			t.Fatalf("⚠️ %s：小节里 %d 个词、包里 %d 项 —— 有一边太少，"+
				"下面的比较会**在空集上通过**（那是全绿）", c.dir, len(words), len(actual))
		}
		for _, a := range actual {
			// 方法写作 T.M：两半都要在这一节里出现过。
			for _, part := range strings.Split(a, ".") {
				if !words[part] {
					t.Errorf("⚠️ %s 导出了 %s，而 roadmap 的清单里找不到 %q —— "+
						"门禁第 ④ 条查的就是这个", c.dir, a, part)
				}
			}
		}
	}
}

// sectionWords 取出一个 roadmap 小节里出现过的全部「词」。
//
// ⚠️ **判据是「这个名字在这一节里出现过」，不是「清单的结构解析得出来」。**
// 试过后者：清单里写的是 `Side（枚举）/ Intent`、`Valve.Check(Intent) error`
// 这类给人读的形状，把它解析成限定名要一堆特例，而**每一条特例都是一次
// 「按现状对齐」** —— 那会把此刻可能已经存在的遗漏一起固化进去。
//
// ⚠️ 代价写在这里：本条**只查一个方向**（包里有、清单没有）。
// 反向（清单里有个已经删掉的名字）查不了 —— 那要求解析结构。
// `SeedOrderSeq` 那种「进来又出去」的项，本条抓得住它进来那一次，抓不住出去那一次。
func sectionWords(t *testing.T, heading string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash("../../docs/roadmap.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	i := strings.Index(body, heading)
	if i < 0 {
		t.Fatalf("⚠️ roadmap 里找不到小节 %q —— 标题改了？本条会在空集上通过", heading)
	}
	if j := strings.Index(body[i+len(heading):], "\n#### "); j >= 0 {
		body = body[i : i+len(heading)+j]
	} else {
		body = body[i:]
	}
	// ⚠️ **只数缩进的清单行，散文不算。**
	// 破坏 255 第一版就栽在这里：我抹掉清单里的 `MarketData`，测试照样绿 ——
	// 因为同一节的散文里有一句「`MarketData` / `SplitSymbol` / `FarPrice`
	// 三个都没有桩」。⚠️ **「这一节提到过这个名字」不等于「清单记了它」**，
	// 而前者恰恰在讲一个缺陷的段落里最容易成立。
	out := map[string]bool{}
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "    ") {
			continue
		}
		n++
		for _, w := range wordRe.FindAllString(line, -1) {
			out[w] = true
		}
	}
	if n == 0 {
		t.Fatalf("⚠️ 小节 %q 里一行缩进清单都没有 —— 格式变了？本条会在空集上通过", heading)
	}
	return out
}

var wordRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// packageExports 用 AST 取一个包的导出面。
//
// ⚠️ **连 `_windows.go` 一起解析**：那才是真实的导出面，
// 而 `go doc` 在别的平台上看不见它们。解析只看语法，不受 build tag 影响。
func packageExports(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	fset := token.NewFileSet()
	n := 0
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		n++
		af, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s：%v", name, err)
		}
		for _, d := range af.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				if d.Recv == nil {
					seen[d.Name.Name] = true
					continue
				}
				if r := recvName(d); r != "" {
					seen[r+"."+d.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, sp := range d.Specs {
					switch sp := sp.(type) {
					case *ast.TypeSpec:
						if sp.Name.IsExported() {
							seen[sp.Name.Name] = true
							// ⚠️ **导出结构上的导出字段也是导出面。**
							//
							// 20260911 送审前撞到的：我在 `OrderState` 上加了
							// `ErrorID`，而本条**一个字都没说** —— 它只数
							// 函数 / 类型 / 变量常量，不数字段。
							//
							//	⚠️ 一个「导出面变了要记进 roadmap」的守卫，
							//	在**字段**这一类上完全没有覆盖 ——
							//	而调用方用得最多的恰恰是字段。
							//
							// ⇒ 记成 `类型.字段`，与方法的记法一致。
							if st, ok := sp.Type.(*ast.StructType); ok && st.Fields != nil {
								for _, f := range st.Fields.List {
									for _, id := range f.Names {
										if id.IsExported() {
											seen[sp.Name.Name+"."+id.Name] = true
										}
									}
								}
							}
						}
					case *ast.ValueSpec:
						for _, id := range sp.Names {
							if id.IsExported() {
								seen[id.Name] = true
							}
						}
					}
				}
			}
		}
	}
	if n == 0 {
		t.Fatalf("⚠️ %s 里一个非测试 .go 都没读到 —— 本条在空转", dir)
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func recvName(d *ast.FuncDecl) string {
	if len(d.Recv.List) != 1 {
		return ""
	}
	switch x := d.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.Ident:
		return x.Name
	}
	return ""
}
