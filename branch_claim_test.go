package futsim

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// devOnlyMarker 是「有东西只在 dev 上」这句话的字面形式。
//
// ⚠️ 守卫认的是这个字符串。改措辞会**悄悄停掉**这条守卫 ——
// 所以下面同时查它的**缺席**：两边都查，改措辞时必然红一次。
//
// ⚠️ 带着**加粗号**是刻意的：不加粗的「只在 `dev` 上」在同一份文件的
// 说明文字里也出现，于是判据会被那一处**替身**顶住 ——
// 破坏 160 当场演示过：把表格里那一行改掉，这条测试照样绿。
// 一个能被无关文字顶住的判据，和一个正确的判据长得一模一样。
const devOnlyMarker = "**只在 `dev` 上**"

// TestDevOnlyClaimIsAccurate 断言状态横幅对「哪些东西在 main 上」说了实话。
//
// # 它堵的是什么
//
// `go get` 在没有 tag 的仓库上默认拿到 `main`。把只在 `dev` 上的东西
// 写成「已实现」，读的人会去 main 上找一个不存在的包 —— 而那不会报错，
// 只会让人以为自己用错了。
//
// ⚠️ **两个方向都查**，理由与评审门禁①同：低报与高报同样是过期陈述。
//
//	dev 比 main 多  → 横幅里必须有那句区分
//	两者一样        → 那句区分必须**消失**（否则是低报）
//
// # ⚠️ 它的盲区，写出来
//
// 判据是一个字面字符串。把那句话换个说法，这条守卫会**悄悄失效** ——
// 所以「两者一样时那句必须消失」这一支同时兼作字符串还在不在的哨兵：
// 措辞一改，至少有一个方向会红一次，逼人回到这里。
//
// ⚠️ 还有一处真盲区：本地没有 `main` 分支时（浅克隆、只拉了 dev），
// 这条守卫**跳过**。跳过是绿的，而绿在这里意味着「没查」。
func TestDevOnlyClaimIsAccurate(t *testing.T) {
	touchGitState(t) // ⚠️ 见它的注释：不读一遍 git 状态，这条测试会被缓存端出旧判决
	if _, err := exec.Command("git", "rev-parse", "--verify", "main").Output(); err != nil {
		t.Skipf("⚠️ 本地没有 main 分支，这条守卫**没有查任何东西**：%v", err)
	}
	out, err := exec.Command("git", "rev-list", "--count", "main..HEAD").Output()
	if err != nil {
		t.Skipf("⚠️ 数不出 main..HEAD：%v —— 这条守卫没有查任何东西", err)
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("git 返回的不是数字：%q", out)
	}
	body := strings.Join(readLines(t, filepath.Join("docs", "fidelity.md")), "\n")
	has := strings.Contains(body, devOnlyMarker)
	t.Logf("dev 比 main 多 %d 个提交；fidelity.md 里%s那句区分",
		ahead, map[bool]string{true: "有", false: "没有"}[has])
	switch {
	case ahead > 0 && !has:
		t.Errorf("⚠️ dev 比 main 多 %d 个提交，而 docs/fidelity.md 里没有 %q —— "+
			"横幅把只在 dev 上的东西说成了已实现。`go get` 默认拿 main，"+
			"读的人会去 main 上找一个不存在的包，而那不会报错", ahead, devOnlyMarker)
	case ahead == 0 && has:
		t.Errorf("⚠️ dev 与 main 已经一样了，而 docs/fidelity.md 里还留着 %q —— "+
			"那是一条**低报**：把已经合进去的东西说成还没合。"+
			"评审门禁①明写「低报也算」", devOnlyMarker)
	}
}
