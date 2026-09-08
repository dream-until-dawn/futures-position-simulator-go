package futsim

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// declaration 是一处「明确不建模」的声明。
type declaration struct {
	File  string
	Line  int
	Until string
	Form  string // Skip(...) 还是 NotModeledUntil:
}

// parseVersion 把 "v0.4.0" 解析成三个数。
//
// ⚠️ 不许「解析不了就跳过」：一个写错的版本号（"v0.4"、"0.4.0"、"下个版本"）
// 若被静默跳过，它就成了一句**永远不会到期**的豁免 ——
// 而那正是本机制要防的那件事，只是换了个形状。
func parseVersion(s string) ([3]int, error) {
	var out [3]int
	if !strings.HasPrefix(s, "v") {
		return out, fmt.Errorf("版本号 %q 不以 v 开头", s)
	}
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("版本号 %q 不是 vX.Y.Z 三段", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, fmt.Errorf("版本号 %q 的第 %d 段不是非负整数", s, i+1)
		}
		out[i] = n
	}
	return out, nil
}

func versionLess(a, b [3]int) bool {
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// collectNotModeled 扫全仓，找出每一处带到期版本的「不建模」声明。
//
// ⚠️ 用 AST 而不是 grep：注释里到处都是 `NotModeledUntil` 与版本号，
// grep 会把说明文字也算成声明，于是这个机制的**计数**先失真。
// （方法论第 33 条附近同一条教训：grep 查的是文本，不是代码。）
func collectNotModeled(t *testing.T) []declaration {
	t.Helper()
	var out []declaration
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".go" {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0) // 0 = 丢掉注释
		if perr != nil {
			t.Fatalf("解析 %s 失败：%v", p, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				// view.Skip("v1.0.0", "…")，或 view 包内部的裸 Skip(…)。
				//
				// ⚠️ **必须看限定符**，只看函数名会把 `t.Skip("找不到夹具")` 也算进来 ——
				// 首跑当场撞到 4 处，它们的「到期版本」是一句中文，
				// 于是本条以「解析不了」的形式红，而红的理由是错的。
				//
				// 这是 AST 那条教训再深一层：**AST 给了结构，
				// 但只用函数名这一层，等于又退回了按名字匹配。**
				matched := false
				switch fn := x.Fun.(type) {
				case *ast.Ident:
					// 裸 Skip：只有 view 包自己的文件里才算。
					matched = fn.Name == "Skip" && f.Name.Name == "view"
				case *ast.SelectorExpr:
					pkg, ok := fn.X.(*ast.Ident)
					matched = ok && fn.Sel.Name == "Skip" && pkg.Name == "view"
				}
				if !matched || len(x.Args) == 0 {
					return true
				}
				if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					out = append(out, declaration{
						File: filepath.ToSlash(p), Line: fset.Position(lit.Pos()).Line,
						Until: v, Form: "Skip(…)",
					})
				}
			case *ast.KeyValueExpr:
				// conformance.Field{NotModeledUntil: "v0.4.0"} 或 Value{Until: "…"}
				key, ok := x.Key.(*ast.Ident)
				if !ok || (key.Name != "NotModeledUntil" && key.Name != "Until") {
					return true
				}
				if lit, ok := x.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					if v == "" {
						return true // 空字符串表示「不是不建模」，不是声明
					}
					out = append(out, declaration{
						File: filepath.ToSlash(p), Line: fset.Position(lit.Pos()).Line,
						Until: v, Form: key.Name + ":",
					})
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// TestNotModeledDeclarationsHaveNotExpired 是 roadmap.md 点破的那个洞的堵法。
//
// > ⚠️ 声明一旦写下去，没有任何机制逼它在 v0.4.0 被摘掉。
// > 一个「暂缓」会安静地变成永久。
//
// 这就是那个机制：把 Version 往前推一个数，会**当场逼出**所有到期的欠账。
func TestNotModeledDeclarationsHaveNotExpired(t *testing.T) {
	cur, err := parseVersion(Version)
	if err != nil {
		t.Fatalf("⚠️ Version 常量本身不合法：%v", err)
	}
	decls := collectNotModeled(t)

	// ⚠️ 判别力守卫：一处声明都没扫到时，本条恒绿而什么都没查。
	// 而「扫不到」最可能的原因是扫描本身坏了（改了构造函数名、加了包装层），
	// 那时它会安静地放行全部声明。
	// ⚠️ **分臂各设下界，不看合计。**
	//
	// 扫描有两条互相独立的臂：调用式 `view.Skip("v…", …)` 与
	// 键值式 `NotModeledUntil: "v…"`。破坏验证当场演示了合计下界的盲区：
	// 把调用式那条改成永不匹配，声明数从 10 掉到 8，**仍然过了 5 这个下界**，
	// 于是那一整臂的失效被合计掩盖。
	//
	// ⚠️ 这与方法论第 32 条「查合计，别查表」不矛盾，是它的对偶
	// （第 27 条）：合计判据防的是「有东西没被算进去」，
	// 分项判据防的是「某一类整个不见了」。**两种盲区互为对偶，要各设各的。**
	byForm := map[string]int{}
	for _, d := range decls {
		byForm[d.Form]++
	}
	for _, form := range []string{"Skip(…)", "NotModeledUntil:", "Until:"} {
		if byForm[form] == 0 {
			t.Fatalf("⚠️ 扫描的「%s」这一臂一处都没找到（各臂：%v）—— "+
				"那一臂很可能坏了，而它的失效会被总数掩盖。"+
				"扫不到的话那一类声明会被安静地放行", form, byForm)
		}
	}
	if len(decls) < 5 {
		t.Fatalf("⚠️ 全仓只扫到 %d 处「不建模」声明 —— 太少，扫描很可能坏了。"+
			"扫不到的话本条恒绿，而全部声明会被安静地放行", len(decls))
	}
	byVersion := map[string]int{}
	for _, d := range decls {
		byVersion[d.Until]++
	}
	versions := make([]string, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	t.Logf("当前 %s；全仓 %d 处「不建模」声明，到期版本分布：", Version, len(decls))
	for _, v := range versions {
		t.Logf("    %-10s %d 处", v, byVersion[v])
	}

	for _, d := range decls {
		got, err := parseVersion(d.Until)
		if err != nil {
			// ⚠️ 解析不了**就是失败**，不是跳过：一个写错的版本号
			// 若被静默跳过，就成了一句永远不会到期的豁免。
			t.Errorf("⚠️ %s:%d 的到期版本 %q 解析不了：%v —— "+
				"⚠️ 解析不了的到期版本 = 永远不会到期的豁免，那正是本机制要防的",
				d.File, d.Line, d.Until, err)
			continue
		}
		if !versionLess(cur, got) {
			t.Errorf("⚠️ %s:%d（%s）声明不建模到 %s，而当前版本已是 %s —— "+
				"**这笔欠账到期了**。要么实现它，要么把到期版本往后推**并说明为什么**；"+
				"不许默默改数字",
				d.File, d.Line, d.Form, d.Until, Version)
		}
	}
}

// TestVersionMatchesDocComment 断言包文档里的版本与常量一致。
//
// ⚠️ 两处写着同一个版本号，就是两处会漂开的地方。
// 这条把它们绑在一起 —— 而绑法是查文档里有没有那个字符串，
// 不是反过来从文档解析版本（那会让文档的措辞变成代码的约束）。
func TestVersionMatchesDocComment(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "doc.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	if f.Doc == nil {
		t.Fatal("doc.go 没有包文档")
	}
	text := f.Doc.Text()
	want := strings.TrimPrefix(Version, "v")
	if !strings.Contains(text, want) {
		t.Errorf("⚠️ 包文档里找不到当前版本 %s —— "+
			"版本号写在两处就是两处会漂开的地方", want)
	}
	// 反向：文档里不该出现**别的**版本号当作「当前状态」。
	// ⚠️ 这一条只查一个很具体的形状（「vX.Y.Z 开发中」），不做泛化解析：
	// 泛化会把「不建模到 v0.4.0」这类正常措辞也当成冲突。
	for _, other := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0", "1.0.0"} {
		// ⚠️ 跳过**当前**这一个。第一版没跳，于是 Version 一旦往前推，
		// 这条守卫就把正确的包文档判成冲突 ——
		// 一条在「该动的东西终于动了」的时候才报警的守卫，
		// 会让人把 Version 改回去，而那正是它本来要防的事。
		if other == want {
			continue
		}
		if strings.Contains(text, other+" 开发中") {
			t.Errorf("⚠️ 包文档说 %s 开发中，而 Version 是 %s", other, Version)
		}
	}
}

// TestVersionLessHandlesEquality 直接考验版本比较的**相等**那一档。
//
// ⚠️ 它补的是一处真实盲区：破坏验证把判据从 `!versionLess(cur, got)`
// 改成 `versionLess(got, cur)` —— 两者只在 `got == cur` 时不同，
// 而全仓没有一处声明的到期版本恰好等于当前版本，于是**测试照样绿**。
//
// 而「等于当前版本」正是最要紧的那一档：v0.4.0 发版时，
// 「不建模到 v0.4.0」的那些**必须**当场到期。
func TestVersionLessHandlesEquality(t *testing.T) {
	v := func(s string) [3]int {
		out, err := parseVersion(s)
		if err != nil {
			t.Fatalf("%s：%v", s, err)
		}
		return out
	}
	cases := []struct {
		a, b string
		want bool
	}{
		{"v0.1.0", "v0.4.0", true},
		{"v0.4.0", "v0.1.0", false},
		{"v0.4.0", "v0.4.0", false}, // ⚠️ 相等 —— 不小于
		{"v0.1.9", "v0.2.0", true},
		{"v0.2.0", "v0.1.9", false},
		{"v1.0.0", "v0.9.9", false},
	}
	if len(cases) != 6 {
		t.Fatalf("用例 %d 条，应为 6", len(cases))
	}
	for _, c := range cases {
		if got := versionLess(v(c.a), v(c.b)); got != c.want {
			t.Errorf("versionLess(%s, %s) = %v，应为 %v", c.a, c.b, got, c.want)
		}
	}
	// ⚠️ 关键断言：到期版本**等于**当前版本时，判据必须判「到期了」。
	cur := v("v0.4.0")
	same := v("v0.4.0")
	if versionLess(cur, same) {
		t.Error("⚠️ 到期版本等于当前版本时被判成「还没到期」—— " +
			"那意味着 v0.4.0 发版时，「不建模到 v0.4.0」的欠账不会被逼出来，" +
			"而那正是这整套机制存在的理由")
	}
}

// TestParseVersionRejectsMalformed 断言几种写错的版本号都被拒。
func TestParseVersionRejectsMalformed(t *testing.T) {
	for _, s := range []string{"0.4.0", "v0.4", "v0.4.0.1", "v0.x.0", "下个版本", "", "vv1.0.0", "v-1.0.0"} {
		if _, err := parseVersion(s); err == nil {
			t.Errorf("⚠️ %q 应当被拒 —— 解析不了的到期版本 = 永远不会到期的豁免", s)
		}
	}
	// 反向：合法的必须过，否则上面那条可以靠「一律拒绝」通过。
	for _, s := range []string{"v0.0.0", "v0.1.0", "v10.20.30"} {
		if _, err := parseVersion(s); err != nil {
			t.Errorf("%q 应当合法：%v", s, err)
		}
	}
}
