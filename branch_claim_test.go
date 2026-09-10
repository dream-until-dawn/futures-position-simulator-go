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
//
// ⚠️ **第三处盲区：讲的和做的不是同一个对象。**
//
// 上面那段危害说的是「`go get` 拿到 `main`，读的人会去 main 上找一个不存在的包」——
// 而 `go get` 拿的是 **`origin/main`**，这里量的是**本地 main**。
// 两者只在 `main == origin/main` 时重合，而 2026-09-09 实测差 **124 个提交**：
//
//	本地 main    有 order / cmd/oracle 的 safety、ctp、conformance
//	origin/main  ⚠️ 一个都没有
//
// ⇒ 本条绿，说明的是「**本地**横幅与**本地** main 一致」，
// 不是「用户拿到的横幅与用户拿到的包一致」。
//
// ⚠️ 而恰好没有真缺陷：已发布那一版的横幅自己写着「只在 dev 上……未合 main」，
// 与已发布的包**自洽**。用户读的横幅也来自 origin/main，不是本地这份。
// 评审方一度把本地的横幅与 origin 的包摆在一起，拼出过一个吓人的结论 ——
// **两个事实取自不同的 ref**（见 silent-risks 方法论 65）。
//
// ⚠️ **这里原本写着一条「所以不改」的理由，而那条理由是假的**：
// 「引进来意味着测试依赖**网络**，离线跑不出结果」——
// `origin/main` 是 `refs/remotes/origin/main`，**本地引用，读它不联网**。实测：
//
//	GIT_ALLOW_PROTOCOL=none git ls-tree -r --name-only origin/main  → 211 个文件 ✅
//	GIT_ALLOW_PROTOCOL=none git ls-remote origin                    → fatal: transport not allowed
//
// 对照组当场失败，证明协议确实被禁 —— 而前者根本没走传输。
//
// ⚠️ **一个错的理由比没有理由更糟**：没有理由时这个问题还开着；
// 写了理由，它就结案了 —— 而这条理由结掉的是一个**刚被登记为盲区**的问题。
//
// 成立的只剩**陈旧性**：`origin/main` 只新鲜到上一次 `fetch`。
// 而它弱得多 —— 陈旧的 `origin/main` 至少是**某个用户某一刻真的拿到过**的东西，
// 本地 main 则谁都没拿到过。
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
	base, how := mainRef(t)
	onlyOnDev := packagesOnlyOn(t, "HEAD", base)

	body := strings.Join(readLines(t, filepath.Join("docs", "fidelity.md")), "\n")
	has := strings.Contains(body, devOnlyMarker)
	t.Logf("只在 dev 上的包 %v（基准 %s，来自 %s）；fidelity.md 里%s那句区分",
		onlyOnDev, base, how, map[bool]string{true: "有", false: "没有"}[has])
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

// mainRef 挑一个可用的 `main` 基准，并**报告用的是哪一个**。
//
// ⚠️ 它补的是本条第二处盲区：`git clone` 一个本地仓库**只带当前检出的那个分支**，
// 于是副本里没有 `main`，而上一版在那种情况下直接 `t.Skip` ——
// **跳过是绿的，而 `go test ./...` 对「有 skip 的包」照样打 `ok`**：
// 跳过在汇总层面**完全看不见**。
//
// 2026-09-09 评审方在副本里验证时正好走进这个盲区，并因此一度报出
// 「破坏 160 与 244 都变绿了」—— 实际是这条守卫**根本没跑**。
//
// ⚠️ 回落到 `origin/main` 而不是继续 skip：它是**本地引用**（读它不联网，见上），
// 且它至少是**某个用户某一刻真的拿到过**的东西。
// ⚠️ 「用的是哪一个」必须打出来 —— 「查到了」与「拿什么当基准查的」是两件事。
// ⚠️ **基准优先取本地 `main`，而本条注释里的危害说的是 `go get` 拿到的 `origin/main`。**
//
// 这两者**只在它们恰好一致时是同一件事**。20260910 推送之后确实一致了：
//
//	origin/main = main = f69529a，两向差 0；生产包数 24 = 24
//
// ⚠️ **而重合是状态，不是修复。** 下一次 `main` 领先 `origin/main`
// （合了没推、或推失败），本条会重新开始量一个与它承诺无关的东西，
// **而它不会说一个字** —— 它照样 PASS。
//
//	⚠️ 与行尾那件事同形：那里「约定只存在于现状恰好是 CRLF 里」，
//	这里「承诺只在本地与已发布恰好一致时成立」。
//
// （评审方 20260910 指出；⚠️ 他给的两个数我复不出来 —— 他说「已发布包 25」
// 而三个 ref 我数都是 24，他说「早上 origin/main 落后 124 条」而我 15:36
// 实测两向差 0。**结构性论断成立，那两个数存疑**，已请他按 `git remote -v`
// 复核 —— 副本的 `origin/*` 指向本地仓库而不是 GitHub，那是记过的坑。）
//
// ⇒ 真要根治，判据得改成**显式对 `origin/main` 取基准并在取不到时报错**，
// 而不是回落。⚠️ 不在本批：那会让离线开发跑不了测试，取舍要先想清楚。
func mainRef(t *testing.T) (ref, how string) {
	t.Helper()
	for _, c := range []struct{ ref, how string }{
		{"main", "本地 main"},
		{"origin/main", "⚠️ 本地无 main，回落到 origin/main（只新鲜到上次 fetch）"},
	} {
		if err := exec.Command("git", "rev-parse", "--verify", c.ref).Run(); err == nil {
			return c.ref, c.how
		}
	}
	t.Skip("⚠️ 本地既没有 main 也没有 origin/main —— 这条守卫**没有查任何东西**")
	return "", ""
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
