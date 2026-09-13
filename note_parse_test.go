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
// # ⚠️ 判据：白名单 —— 读 `.Note` 只许三种形状
//
//	透传进结构体字面量   `Note: raw.Note`
//	写                   `x.Note = …`
//	打印（打出去）       作 fmt.Print* / Fprint*、log.Print*、t.Log* / Error* / Fatal*、logf 的实参
//	                     ⚠️ fmt.Sprint* / Sscan* / Errorf **不算**：它们把内容还回程序里
//
// 其余**任何**读法都算违规，另加一条：`m["note"]` 按键取值（从通用 map 里把注记当数据读出来）。
//
// ⚠️ 上一版是**黑名单**（`==`/`!=`、switch tag、strings/regexp/… 的实参），登记的盲区是
// 「先赋给局部变量再比」。20260913 评审用四个探针指出盲区比登记的宽：
// `zzStage(f.Note)`（自写一个解析函数）同样逃掉，**而那是写解析器最自然的形状**。
// 两者同根 ——「任何去掉 `.Note` 选择子的间接都逃得掉」，而黑名单只能按例子补。
// ⇒ 换成白名单：**在读的那一刻就判**，间接走不走得通与本条无关。
// 这正是本仓库脱敏那条规矩的同一个道理：黑名单漏一个 → 静默；白名单漏一个 → 报错。
//
// ⚠️ 仍然抓不到的（按根写，不按例子写），两个根：
//
//	① **不经 `.Note` 选择子**拿到注记内容 —— 整个结构体序列化再解析、反射、`m[k]` 用变量键取
//	② 「打印」是**按名字**认的 —— 一个叫 `logf` 的局部闭包、一个叫 `t` 的变量，谁都能定义，
//	   而本条不看它们的实现。把注记交给一个自写的、恰好叫 logf 的解析函数，本条放行
//
// ⚠️ 20260913 评审之前我登记的只有 ①，而 `fmt.Sscanf(f.Note, …)` **经过**选择子、却被当成打印放过 ——
// 那句「按根登记」当时不准。本仓库此刻两种都没有，而本条不假装守住了它们。
func TestNobodyParsesFixtureNotes(t *testing.T) {
	// ⚠️ **先证明探测器抓得住**，再去扫仓库。
	// 否则「整个仓库零命中」有两种读法 —— 没人 parse，或者探测器从来不会响 ——
	// 而两者在全绿时长得一模一样。
	// ⚠️ 合成样本**两侧都有**：6 处违规（含评审的 P2 局部变量、P3 自写解析函数），
	// 3 处允许的读法（透传、写、打印）。只放违规样本的话，一个「见 `.Note` 就报」的探测器也能过。
	const synthetic = `package x
import ("fmt"; "strings")
type F struct{ Note string }
func zzStage(s string) int { return strings.Index(s, "①") }
func a(f F, m map[string]any) bool {
	_ = m["note"]
	switch f.Note { case "x": }
	n := f.Note
	_ = zzStage(f.Note)
	_ = F{Note: f.Note}
	f.Note = "w"
	fmt.Println(f.Note)
	logf("%s", f.Note)
	t.Logf("%s", f.Note)
	var stage int
	_ = strings.Contains(fmt.Sprintf("%s", f.Note), "后")
	_ = fmt.Sprint(f.Note) == "x"
	_ = strings.HasSuffix(fmt.Errorf("%s", f.Note).Error(), "后")
	_, _ = fmt.Sscanf(f.Note, "ctp-slices 阶段%d", &stage)
	return strings.Contains(f.Note, "①") || f.Note == "y" || n == ""
}`
	if got := noteParses(t, "synthetic.go", synthetic); len(got) != 10 {
		t.Fatalf("⚠️ 探测器在合成代码上应抓到 10 处（m[\"note\"] / switch / 局部变量 / 自写解析函数 / "+
			"fmt.Sprintf / fmt.Sprint / fmt.Errorf / fmt.Sscanf / strings.Contains / ==），"+
			"且放过透传、写、fmt.Println、logf、t.Logf 五处；实际 %d 处：%v —— "+
			"**探测器坏了，下面扫仓库的结果不可信**", len(got), got)
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
	// ⚠️ 「打印」= 把内容**打出去**，不再回到程序里。20260913 评审第二节：上一版把整个 `fmt` 包当打印，
	// 于是 `fmt.Sscanf(f.Note, "阶段%d", &stage)` —— 字面意义上的解析注记 —— 被放行。
	// ⇒ 分开**打出去**与**还回来**：
	//
	//	打出去   fmt.Print* / Fprint*、log.Print*、t|b|tb 的 Log* / Error* / Fatal*、logf
	//	还回来   fmt.Sprint* / Sscan* / Fscan* / Append*、**fmt.Errorf**（错误往上传之后 `.Error()` 拿得回内容）
	outward := map[string]bool{"Print": true, "Printf": true, "Println": true,
		"Fprint": true, "Fprintf": true, "Fprintln": true}
	testOut := map[string]bool{"Log": true, "Logf": true, "Error": true, "Errorf": true,
		"Fatal": true, "Fatalf": true}
	isPrinter := func(fun ast.Expr) bool {
		switch f := fun.(type) {
		case *ast.Ident:
			return f.Name == "logf"
		case *ast.SelectorExpr:
			pkg, ok := f.X.(*ast.Ident)
			if !ok {
				return false
			}
			switch pkg.Name {
			case "fmt", "log":
				return outward[f.Sel.Name]
			case "t", "b", "tb":
				return testOut[f.Sel.Name]
			}
		}
		return false
	}
	var out []string
	at := func(n ast.Node, what string) {
		out = append(out, fset.Position(n.Pos()).String()+"："+what)
	}
	// ⚠️ 白名单要看**父节点**，而 ast.Inspect 不给父节点 —— 自己维护一个栈。
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		stack = append(stack, n)
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if v.Sel.Name != "Note" {
				return true
			}
			switch p := parent.(type) {
			case *ast.KeyValueExpr:
				if p.Value == v {
					return true // 透传进结构体字面量
				}
			case *ast.AssignStmt:
				for _, l := range p.Lhs {
					if l == v {
						return true // 写
					}
				}
				at(v, "`.Note` 被读出来赋给别的东西（间接之后本条就看不见了，所以在读的这一刻判）")
				return true
			case *ast.CallExpr:
				if isPrinter(p.Fun) {
					return true // 打印
				}
				at(v, "`.Note` 作一个非打印函数的实参（自写的解析函数也算）")
				return true
			case *ast.SwitchStmt:
				at(v, "`.Note` 作 switch 的 tag")
				return true
			case *ast.BinaryExpr:
				at(v, "`.Note` 作运算的操作数")
				return true
			}
			at(v, "`.Note` 以白名单之外的形状被读")
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
