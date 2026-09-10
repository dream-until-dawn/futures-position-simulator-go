package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testFuncsIn 列出一个包目录下的顶层测试函数名。
//
// ⚠️ 只看本目录，不递归：清单里每一条的 pkg 都是**一个具体的包**
// （`./view/`、`./probe/`…），没有 `./...`。递归会把子包的名字也算进来，
// 于是一个写错了包的 test 字段照样能「找得到」—— 那就白查了。
func testFuncsIn(dir string) (map[string]bool, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			names[fn.Name.Name] = true
		}
	}
	return names, nil
}

// TestEveryBreakNamesARealTest 钉住**清单里点名的每一个测试都真的存在**。
//
// ⚠️ 它是被一次「跑了零条测试，报了一个结论」逼出来的。
//
// 破坏 70 的 test 字段写的是 `TestPositionFrozen`。probe 包里没有这个函数，
// 只有四个更长的名字（TestPositionFrozenUsesTheGuards 等）。而 breakcheck 是
// **锚定**跑的（`-run '^(…)$'`），于是它一条也选不中，go test 打印
// "no tests to run"、PASS、**退出 0** —— breakcheck 看退出码，判「如预期仍然绿」，
// 连带把那条精心写过的「盲区」说明一起打出来。每一次全量运行都是这样。
//
// ⚠️ 方向是最贵的那一边：expect=green 的破坏，跑零条测试**必然**满足期望。
// 它永远不会喊，只会一直点头 —— 一个空转的守卫，长得和一个成立的守卫一模一样。
//
// ⚠️ 更该记的是「两个人都写下过同一句错话」：我和评审方都说过那个字段
// 「是个前缀（-run 用正则），它覆盖持仓冻结那一族的四条」。
// 这句话对**手敲的** `go test -run TestPositionFrozen` 是真的，
// 对 breakcheck 是假的 —— 差别只在 breakcheck 自己加的那两个锚点上。
//
//	看的是同一个字段，读的是两套语义，而两套都说得通。
//
// 运行期另有一道（run() 里对 "no tests to run" 的拦截）。这一道在写清单的时候拦。
func TestEveryBreakNamesARealTest(t *testing.T) {
	var breaks []Break
	if err := json.Unmarshal(registryJSON, &breaks); err != nil {
		t.Fatalf("⚠️ 读不了破坏清单：%v", err)
	}
	if len(breaks) == 0 {
		t.Fatal("⚠️ 清单是空的 —— 下面的循环一条也不会跑")
	}

	// 缓存按目录建，一个包只解析一次。
	cache := map[string]map[string]bool{}
	checked := 0
	for _, b := range breaks {
		dir := filepath.Join("..", "..", b.Dir, filepath.Clean(b.Pkg))
		have, ok := cache[dir]
		if !ok {
			var err error
			if have, err = testFuncsIn(dir); err != nil {
				t.Errorf("⚠️ %q 的包目录 %s 打不开：%v —— "+
					"dir/pkg 这一对指到了不存在的地方", b.Name, dir, err)
				cache[dir] = map[string]bool{}
				continue
			}
			if len(have) == 0 {
				t.Errorf("⚠️ %q 的包目录 %s 里一个测试函数都没有 —— "+
					"要么 pkg 写错了，要么这个枚举器坏了", b.Name, dir)
			}
			cache[dir] = have
		}
		// ⚠️ 按 `|` 拆，逐段要求**全名相等**。清单里有 5 条用 `A|B`
		// 写多个名字，那是合法写法；但每一段都必须是完整的函数名，
		// 因为 breakcheck 会把整串包进 `^(…)$`。
		for _, name := range strings.Split(b.Test, "|") {
			checked++
			if !have[name] {
				t.Errorf("⚠️ %q 点名的测试 %s 在 %s 里不存在 —— "+
					"breakcheck 锚定跑 `-run '^(%s)$'`，它会选中零条测试、"+
					"退出 0，然后把这条破坏报成一个结论", b.Name, name, dir, b.Test)
			}
		}
	}

	// ⚠️ 判别力：上面每一条都是「没找到才喊」。一个 have 恒为
	// 「什么都有」的实现、或者一个 breaks 提前空掉的循环，都能全绿。
	// 这里钉住**确实逐条比对过**，而且比对的条数不少于破坏条数。
	if checked < len(breaks) {
		t.Fatalf("⚠️ 只比对了 %d 个测试名，而清单有 %d 条破坏 —— "+
			"有破坏被跳过了，这一条查的东西比它看起来的少", checked, len(breaks))
	}
}

// TestZeroTestsIsNotAVerdict 钉住**跑了零条测试不算一个结论**。
//
// ⚠️ 这是运行期那一道，与 TestEveryBreakNamesARealTest 走的是两条路：
// 那一条在**写清单**的时候比对名字，这一条在**跑**的时候看 go test 说了什么。
// 名字对而包写错、包对而那条测试被 build tag 挡掉 —— 都能绕过第一道，
// 却绕不过「no tests to run」。
//
// ⚠️ 两边都要断言。只断言「零条时不判绿」的话，一个恒判「零层未成立」
// 的实现照样能过 —— 而那会把整份清单打成 269 条未按预期。
func TestZeroTestsIsNotAVerdict(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("go.mod", "module tmpprobe\n\ngo 1.21\n")
	target := write("x.go", "package tmpprobe\n\n// 锚点\nvar X = 1\n")
	write("x_test.go", "package tmpprobe\n\nimport \"testing\"\n\n"+
		"func TestReal(t *testing.T) { _ = X }\n")

	// 破坏改的是一句注释：它**不会**让任何测试失败。
	// 于是「绿」这一侧是干净的，两侧的差别只剩测试名选中了几条。
	base := Break{
		Dir: dir, File: target, Pkg: "./",
		Old: "// 锚点", New: "// 被改坏了",
		Expect: "green", Why: "改注释不影响任何断言",
	}

	hit := base
	hit.Name, hit.Test = "选得中", "TestReal"
	if v, d := run(hit); v != "如预期仍然绿" {
		t.Fatalf("⚠️ 选中了一条真测试、而破坏只动了注释，判定却是 %q（%s）—— "+
			"「绿」这一侧就没成立，下面那一侧的对比也就不说明什么", v, d)
	}

	miss := base
	miss.Name, miss.Test = "选不中", "TestNoSuchThing"
	v, d := run(miss)
	if v == "如预期仍然绿" {
		t.Fatalf("⚠️ `-run '^(TestNoSuchThing)$'` 一条测试都没选中，判定却是「如预期仍然绿」—— " +
			"这正是破坏 70 空转了很久的那个洞：零条测试**必然**满足「期望绿」，" +
			"它不会喊，只会一直点头")
	}
	if !strings.Contains(d, "一条测试也没选中") {
		t.Errorf("⚠️ 判成了 %q，但说明里没提「一条也没选中」（%s）—— "+
			"红对了理由才算数：它可能是因为别的原因没绿", v, d)
	}
}
