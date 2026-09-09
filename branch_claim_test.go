package futsim

import (
	"os/exec"
	"path/filepath"
	"sort"
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
//	有包只在 dev 上  → 横幅里必须有那句区分
//	一个都没有      → 那句区分必须**消失**（否则是低报）
//
// ⚠️ 判据是**包的存在性**，不是提交数 —— 20260909 第一次合并之后当场换的，
// 理由写在函数体里：纯文档提交会让 dev「领先」，而据此要求横幅写
// 「order 只在 dev 上」是逼文档说一句**假话**。
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

	// ⚠️ 判据是**包在不在**，不是**领先几个提交**。
	//
	// 上一版数的是 `main..HEAD` 的提交数。2026-09-09 第一次真的合并之后立刻暴露：
	// 合完再推两个**纯文档**提交，dev 就「领先 2 个」，于是它要求横幅重新写上
	// 「报单校验与冻结只在 dev 上」—— **而那句话此刻是假的**，order 已经在 main 上了。
	//
	// ⚠️ 一条守卫逼着文档写一句假话，比它不存在更坏。
	// 它真正要防的从来不是「有没有领先」，是
	// **「横幅有没有把 main 上没有的东西说成已实现」** —— 那是包的存在性问题。
	onlyOnDev := packagesOnlyOn(t, "HEAD", "main")

	body := strings.Join(readLines(t, filepath.Join("docs", "fidelity.md")), "\n")
	has := strings.Contains(body, devOnlyMarker)
	t.Logf("只在 dev 上的包 %v；fidelity.md 里%s那句区分",
		onlyOnDev, map[bool]string{true: "有", false: "没有"}[has])
	switch {
	case len(onlyOnDev) > 0 && !has:
		t.Errorf("⚠️ 这些包**只在 dev 上**：%v，而 docs/fidelity.md 里没有 %q —— "+
			"横幅把只在 dev 上的东西说成了已实现。`go get` 默认拿 main，"+
			"读的人会去 main 上找一个不存在的包，而那不会报错", onlyOnDev, devOnlyMarker)
	case len(onlyOnDev) == 0 && has:
		t.Errorf("⚠️ 没有任何包只在 dev 上，而 docs/fidelity.md 里还留着 %q —— "+
			"那是一条**低报**：把已经合进去的东西说成还没合。"+
			"评审门禁①明写「低报也算」", devOnlyMarker)
	}
}

// packagesOnlyOn 返回 ref 上有、而 base 上没有的 Go 包目录。
//
// ⚠️ 只看**非测试**的 .go：一个包在不在 main 上取得到由生产代码决定，
// 测试文件在不在**不改变 `go get` 的结果**。
func packagesOnlyOn(t *testing.T, ref, base string) []string {
	t.Helper()
	pkgs := func(r string) map[string]bool {
		out, err := exec.Command("git", "ls-tree", "-r", "--name-only", r).Output()
		if err != nil {
			t.Skipf("⚠️ 列不出 %s 的文件，这条守卫**没有查任何东西**：%v", r, err)
		}
		set := map[string]bool{}
		for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			f = strings.TrimSpace(f)
			if !strings.HasSuffix(f, ".go") || strings.HasSuffix(f, "_test.go") {
				continue
			}
			if i := strings.LastIndex(f, "/"); i > 0 {
				set[f[:i]] = true
			} else {
				set["."] = true
			}
		}
		return set
	}
	a, b := pkgs(ref), pkgs(base)
	// ⚠️ 两侧都要卡下界：任一侧解析成空时，差集会变成一个看起来很有意义的答案
	// （「全部都是 dev 独有」或者「什么都不独有」），而两者都是错的。
	if len(a) < 5 || len(b) < 5 {
		t.Fatalf("⚠️ 解析到的包数 %s=%d / %s=%d —— 太少，这条在空转", ref, len(a), base, len(b))
	}
	var only []string
	for k := range a {
		if !b[k] {
			only = append(only, k)
		}
	}
	sort.Strings(only)
	return only
}
