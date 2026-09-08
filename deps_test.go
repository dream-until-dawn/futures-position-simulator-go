package futsim

import (
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// TestMainModuleHasOneDependency 断言**主模块的依赖树只有 shopspring/decimal 一个**。
//
// ⚠️ 这是 design.md §3 写死的硬约束，而它此前**没有任何守卫**。
//
// 约束的理由不是洁癖：使用者 `go get` 这个库时，依赖会出现在**他们的**模块图里。
// 一次随手的 `import "github.com/xxx/yyy"` 就能破掉它，而破掉的方式是
// **本仓库这边没有任何报错** —— 使用者拉下来才看见多了一个依赖。
//
// # ⚠️ 嵌套模块不算，方向是关键
//
// `cmd/oracle` 依赖主模块（2026-09-09 加的，为了让实时对拍复用同一套比对代码），
// 那**不影响**这条约束：方向是 oracle → 主模块，而使用者拿到的是主模块。
// 反过来（主模块 import cmd/oracle）才会破掉它，那时 WebSocket 客户端
// 会出现在每一个使用者的模块图里。
//
// 本条只看主模块，正是因为方向要紧。
//
// # ⚠️ 破坏验证曾经有个洞，补上了 —— 而补的过程本身值得记
//
// 真正要防的场景是「有人加了一个 import **并且**补上了 require」。
// 而 `tools/breakcheck` 原来只改**一个文件**，于是那个场景表达不出来：
//
//	只加 import   go.mod 里没有 require → 编译失败，红的是编译器不是断言
//	只加 require  没有 import → 依赖不进 `go list -deps`，什么都不会变
//
// 我先如实把这个洞写在这里，然后给 breakcheck 加了多文件支持（`also`）。
// ⚠️ 补的时候才发现要改的是**四处**，不是两处：
//
//	view/view.go   加 import
//	go.mod         加 require，**并把 go 指令从 1.22 提到 1.23**
//	go.sum         加两行校验和
//
// 少任何一处，`go list` 都会以「updates to go.mod needed」告吹 ——
// 而那时红的是 go 的模块系统，不是本条断言。
// ⚠️ 也就是说这条约束其实有**两层**保护：go 自己的模块系统会拦下
// 一次不完整的添加，本条拦下一次**完整而正确**的添加。
// 第 112 条（打本守卫自己的归并逻辑）与第 116 条（真场景）现在都在册。
//
// ⚠️ 顺带一条只有踩过才知道的：`exec.Command(...).Output()` **只捕获 stdout**。
// 第一版报错只有一句「exit status 1」，而真正的原因全在 stderr 里 ——
// 一条只说「失败了」的错误信息，会让人去查错的地方。
func TestMainModuleHasOneDependency(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./...")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// ⚠️ 必须把 stderr 打出来。`exec.Command(...).Output()` 只捕获 stdout，
		// 于是报错只有一句「exit status 1」—— 那句话什么都没说，
		// 而 go list 真正的原因（模块声明、go.sum 缺条目、语法错）全在 stderr 里。
		// 一条只说「失败了」的错误信息，会让人去查错的地方。
		t.Fatalf("go list -deps 失败：%v\n%s", err, stderr.String())
	}
	const self = "github.com/dream-until-dawn/futures-position-simulator-go"
	const allowed = "github.com/shopspring/decimal"

	// ⚠️ 归并到**模块**（前三段：github.com/owner/repo），不是域名。
	// 第一版按域名归并，于是两个不同的 github 依赖会被并成一个
	// "github.com" —— 那时守卫看起来通过了，而实际上多了一个依赖。
	// ⚠️ 一条把两个不同的东西并成一个的守卫，与没有守卫是同一回事。
	mods := map[string]bool{}
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p := strings.TrimSpace(line)
		if p == "" {
			continue
		}
		total++
		if strings.HasPrefix(p, self) {
			continue
		}
		// stdlib 的包路径第一段不含点号（"fmt"、"encoding/json"）。
		first := p
		if i := strings.Index(p, "/"); i >= 0 {
			first = p[:i]
		}
		if !strings.Contains(first, ".") {
			continue
		}
		if parts := strings.Split(p, "/"); len(parts) >= 3 {
			mods[strings.Join(parts[:3], "/")] = true
		} else {
			mods[p] = true
		}
	}
	// ⚠️ 一个包都没列到时本条恒真。卡下界。
	if total < 20 {
		t.Fatalf("⚠️ go list -deps 只给出 %d 个包 —— 本条在几乎空的集合上跑", total)
	}
	names := make([]string, 0, len(mods))
	for m := range mods {
		names = append(names, m)
	}
	sort.Strings(names)
	if len(names) != 1 || names[0] != allowed {
		t.Errorf("⚠️ 主模块的第三方依赖是 %v，而硬约束是**只有** %s —— "+
			"⚠️ 使用者 go get 这个库时，多出来的依赖会进**他们的**模块图，"+
			"而本仓库这边不会有任何报错。见 design.md §3", names, allowed)
	}
	t.Logf("主模块共 %d 个包，第三方依赖 %d 个：%v", total, len(names), names)
}
