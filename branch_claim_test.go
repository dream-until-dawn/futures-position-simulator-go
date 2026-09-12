package futsim

import (
	"os/exec"
	"path/filepath"
	"sort"
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

// tagConventionEveryMerge / tagConventionOnAcceptance 是 roadmap 里那条约定的两种写法。
//
// ⚠️ 用**措辞**当锚，而不是小节标题：标题会被重排，而这两句话的**内容**就是约定本身。
const (
	tagConventionEveryMerge   = "每次合并打"
	tagConventionOnAcceptance = "**某个版本的验收达成时**打"
	// zeroTagDeclPrefix 是那句声明的**行首前缀**，不是整句。
	// ⚠️ 用前缀 + 行首：整句放在 body 里 Contains 会被**关于它的叙述**顶替，
	// 而那正是 tagConvention* 已经踩过一次的坑（20260912 评审指出同一个函数里只填了一半）。
	zeroTagDeclPrefix = "> ⓘ **当下的状态：本地一个 tag 都没有"
)

// TestTagConventionMatchesReality 查**「文档说的」与「仓库里的」有没有对上**。
//
// # ⚠️ 它来自一条执行不了的约定，而那条约定是以「被忽略」消失的
//
// roadmap 原先写「每次合并打 `vX.Y.Z` tag」。2026-09-12 合并前核 `git tag`：
// **空的**，远端也空 ⇒ 上一次合并（`97d7845`）就漏了，**两次都没有任何信号**。
//
//	⚠️ 而它漏掉的原因不是忘了，是那条约定与语义版本**冲突**：
//	`Version = "v0.4.0"` 明写它是「当前开发中」，而 v0.4.0 的验收未达成
//	⇒ 打 v0.4.0 等于宣称它发布了，**那是一句假话**。
//
// ⇒ **一条执行不了的约定，不会以「被违反」的形式暴露，它以「被忽略」的形式消失** ——
// 而「被忽略」在仓库里没有任何痕迹，除了那个空的 `git tag`，而没人会去看它。
//
// 本条把它变成有痕迹的：三支各对一种状态，而**「零 tag」那一支要求文档自己说出来**。
func TestTagConventionMatchesReality(t *testing.T) {
	touchGitState(t)
	body := strings.Join(readLines(t, filepath.Join("docs", "roadmap.md")), "\n")
	// ⚠️ **约定在「分支与发布约定」那张表的 `main` 那一行里，不在正文里。**
	//
	// 第一版拿整篇文档当锚，当场红了 —— 因为讲这条历史的那一格**引用了旧措辞**
	// （「每次合并打 tag」），于是两句同时命中，守卫报「约定说不清」。
	//
	//	⚠️ 那一红是**对的**：它说的正是「我分不清哪句是约定、哪句是注解」。
	//	⇒ 缩小到那一行，让**约定**与**关于约定的叙述**分开。
	//
	// ⚠️ 20260912 评审补的第二半：那一行里**不许出现关于约定的否定句**。
	// 当时写着「⚠️ 不是「每次合并都打」—— 见下方那一格」，而常量是
	// 「每次合并打」（没有「都」）—— **只差一个字就会两句同时命中**。
	// 约定与「关于约定的否定句」挤在同一行，正是第一版那一红的成因，换了个地方又长了出来。
	row := mainBranchRow(t, body)
	everyMerge := strings.Contains(row, tagConventionEveryMerge)
	onAcceptance := strings.Contains(row, tagConventionOnAcceptance)
	if everyMerge == onAcceptance {
		t.Fatalf("⚠️ roadmap 那张表的 `main` 行里，tag 约定既不是「每次合并」也不是「验收达成时」，"+
			"或者两句同时在（每次合并=%v，验收达成=%v）—— "+
			"**约定说不清的时候，本条守不住任何东西**。"+
			"⚠️ 关于约定的叙述（含否定句）请写到正文里，别留在这一行：\n    %s",
			everyMerge, onAcceptance, row)
	}

	// ⚠️ 读的是**本地** tag。「我们没发布过版本」是一句关于**远端**的话，
	// 而本条查不了远端（走网络会脆）。⇒ 声明那一句必须自己说清是哪一个，
	// 见 `zeroTagDeclaration` —— 同你们自己那条「副本的 `origin/*` 不是发布状态」。
	out, err := exec.Command("git", "tag", "--list", "v*").Output()
	if err != nil {
		t.Fatalf("⚠️ 读不到本地 tag 列表：%v —— **没有结论**，不是「没有 tag」", err)
	}
	var tags []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			tags = append(tags, l)
		}
	}
	t.Logf("ⓘ 约定=%s；**本地** tag %v（共 %d 个）；Version=%s",
		map[bool]string{true: "每次合并都打", false: "验收达成时打"}[everyMerge],
		tags, len(tags), Version)

	if everyMerge {
		// ⚠️ 这一支平时走不到（约定已改），而 20260912 评审指出它**说强了**：
		// roadmap 写「断言 tag 数 ≥ main 上的合并数」，而代码只查了 `len(tags) == 0`，
		// 合并数**只进了报错信息、没进断言**。
		//
		//	⚠️ 在一个专门治「文档与现实对不上」的提交里，
		//	**文档与它自己的守卫对不上** —— 而这一处没有任何东西会发现它：
		//	本条查的是「约定 ↔ tag 现实」，不查「roadmap 对本条的描述 ↔ 本条」。
		//
		// ⇒ 按文档那句**改强**（`≥` 比「不为零」对：每次合并都打，就该数得上）。
		out, err := exec.Command("git", "rev-list", "--merges", "--count", "main").Output()
		if err != nil {
			t.Fatalf("⚠️ 数不出 main 上的合并数：%v —— **没有结论**", err)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatalf("⚠️ 合并数解析不出来（%q）：%v", strings.TrimSpace(string(out)), err)
		}
		// ⚠️ **`n` 数的是合并「提交」，而不是「集成次数」**（20260912 评审提）：
		// 走 fast-forward 的那几次不产生合并提交、不计入 `n` ⇒ 这条判据**偏松**。
		// 方向是安全的那一侧（少要求，不会逼人打多余的 tag），所以留着，
		// 但**别把它读成「tag 数 ≥ 集成次数」**。
		if len(tags) < n {
			t.Errorf("⚠️ 文档写着 %q，而 main 上有 **%d** 次合并、本地只有 **%d** 个 tag —— "+
				"那条约定此刻是一句没人执行的话", tagConventionEveryMerge, n, len(tags))
		}
		return
	}

	// —— 约定是「验收达成时才打」——
	if len(tags) == 0 {
		// ⚠️ **零 tag 必须在文档里被说出来**，否则它是一个沉默的状态。
		// 同本项目「语料待拍」「留底缺一列」那一条处置。
		requireZeroTagDeclaration(t, body, true)
		return
	}
	// 有 tag 了：那句「一个都没有」必须被删掉，否则是过期低报。
	requireZeroTagDeclaration(t, body, false)
	// ⚠️ 判据本身抽在 `checkTagsAgainstVersion` 里，这里只负责把它翻成 t.Errorf。
	//
	// ⚠️ 20260912 评审第二次指出：418/419 那个「如预期仍然绿」**不是结构性欠着**，
	// 是**可测性**欠着 —— 它与 403/409/413 不同族：
	//
	//	403 / 409 / 413   判别力**在仓库之外**（要换柜台、要等夜盘）
	//	418 / 419         判别力**在仓库之内**，只是与 `git tag` 的 I/O 缠在一起：
	//	                  零 tag ⇒ 循环不执行 ⇒ 把判据改弱在当下语料上无从显形
	//
	// ⇒ 抽出来喂合成 tag，**一个 tag 都不用真打**，418/419 立刻能红。
	// ⚠️ 他说得对，而我原先把它记进了「结构性欠着」那一栏 ——
	// **那会让一条明天就能还的债，和那些真要换柜台的条目混在同一张表上。**
	for _, p := range checkTagsAgainstVersion(tags, Version) {
		if p.Kind == kindVersionUnparseable {
			// ⚠️ 这一支要 Fatal：Version 认不得时，**没有一个 tag 判得了** ——
			// 继续往下报每个 tag 只会产出 N 条指错地方的错误。
			t.Fatal(p.Msg)
		}
		t.Error(p.Msg)
	}
}

// tagVersionProblem 是一条「tag 与 Version 对不上」的判定。
//
// ⚠️ `Tag` 为空表示问题出在 **Version 自己**身上，与任何一个 tag 无关。
type tagVersionProblem struct {
	Tag  string
	Kind string
	Msg  string
}

const (
	kindVersionUnparseable = "version-unparseable"
	kindTagUnparseable     = "tag-unparseable"
	kindNotEarlier         = "not-earlier"
)

// checkTagsAgainstVersion 判每个 tag 是不是**早于** version。
//
// # ⚠️ 它被抽出来有两个理由，而第二个是评审实测出来的缺陷
//
// 一、可测性：判据原先与 `git tag` 的 I/O 缠在一起，零 tag 时那个循环
// 一次都不执行 ⇒ 把判据改弱（`>=` 改回 `==`、认不得的悄悄放过）
// **在当下语料上无从显形**。抽出来喂合成 tag 就能直接测。
//
// 二、⚠️ **原先 Version 认不得时，报错指的是 tag。**
// `semverCompare` 在**任一边**解析失败都返回 `false`，而调用处只点 `tg` ⇒
//
//	Version = "v0.4.0-rc.1" 时，**每一个完好的 tag 都被报成「认不得的 tag 形状」**，
//	而没有一条提到 Version。
//
// ⚠️ 触发条件不是假想的：使用者 20260912 那次裁决的选项②就是 `v0.4.0-rc.1`。
// 它是**响的**（红，不是静默放过），所以不挡事 —— 而该现在修的理由是本仓自己的教训：
// **一个指错地方的判定，转述出去之后就与真的分不开了**
// （同一天刚在 406 的 mojibake 上栽过一次，这是它的静态版本）。
//
// ⇒ 先判 version，认不得就**只报 version 这一条**并停手：
// 那时候没有任何一个 tag 判得了，继续报只会产出 N 条指错地方的错误。
func checkTagsAgainstVersion(tags []string, version string) []tagVersionProblem {
	if _, ok := parseSemver(version); !ok {
		return []tagVersionProblem{{
			Kind: kindVersionUnparseable,
			Msg: "⚠️ **Version 自己认不得**：" + version + "（要严格 `vX.Y.Z`）—— " +
				"这时候**没有一个 tag 判得了**，本条停在这里。" +
				"⚠️ 别把它读成「tag 有问题」：`semverCompare` 两边都会解析，" +
				"而原先的报错只点 tag —— 那正是这一支存在的理由",
		}}
	}
	var out []tagVersionProblem
	for _, tg := range tags {
		cmp, ok := semverCompare(tg, version)
		if !ok {
			out = append(out, tagVersionProblem{Tag: tg, Kind: kindTagUnparseable,
				Msg: "⚠️ 认不得的 tag 形状 " + tg + "（要严格 `vX.Y.Z`）—— **没有结论**，" +
					"而悄悄放过它等于假设它合规"})
			continue
		}
		if cmp >= 0 {
			out = append(out, tagVersionProblem{Tag: tg, Kind: kindNotEarlier,
				Msg: "⚠️ tag " + tg + " **不早于** Version " + version +
					" —— Version 是「**当前开发中**」的版本，给它（或更晚的版本）打 tag " +
					"等于宣称一件没发生的事。⚠️ 那正是原约定「每次合并都打」逼出来的那句假话"})
		}
	}
	return out
}

// requireZeroTagDeclaration 双向核对 roadmap 里那句「本地一个 tag 都没有」。
//
// # ⚠️ 它为什么要自己找行，而不是在整篇里 strings.Contains
//
// 20260912 评审指出：`tagConvention*` 已经缩到 `main` 那一行了，理由是
// **「叙述会引用措辞」**，而这句声明当时仍在**整篇** `body` 里找 ——
// **同一个坑在同一个函数里只填了一半**。
//
//	将来一句「当时一个 tag 都没有」的历史叙述，就能在真声明被删之后顶替它；
//	反方向也一样：真打了 tag 之后，那句历史叙述会让「过期低报」那条**误红**，
//	而它改不掉 —— 历史就是那么写的。
//
// ⇒ 只认**行首**是那个前缀的行，并且要求**恰好一行**：
// 多于一行说明有人复制了它，那时「删掉哪一句」就没有唯一答案。
func requireZeroTagDeclaration(t *testing.T, body string, want bool) {
	t.Helper()
	n := 0
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), zeroTagDeclPrefix) {
			n++
		}
	}
	switch {
	case want && n == 0:
		t.Errorf("⚠️ 本地一个 tag 都没有，**而 roadmap 里也没有以 %q 开头的那一行** —— "+
			"两头都不说，这件事就没有任何可见的对应物了", zeroTagDeclPrefix)
	case !want && n > 0:
		t.Errorf("⚠️ 已经有 tag 了，而 roadmap 里仍有 %d 行以 %q 开头 —— "+
			"**过期陈述**，门禁①明写「低报也算」", n, zeroTagDeclPrefix)
	case n > 1:
		t.Errorf("⚠️ roadmap 里有 %d 行以 %q 开头（要恰好一行）—— "+
			"复制之后「该删哪一句」就没有唯一答案了", n, zeroTagDeclPrefix)
	}
}

// semverCompare 比两个 `vX.Y.Z`。第二个返回值为 false 表示**认不得**，
// 而认不得要由调用方报出来 —— ⚠️ 悄悄当成 0（相等）会让一个奇形怪状的 tag 通过。
func semverCompare(a, b string) (int, bool) {
	pa, ok := parseSemver(a)
	if !ok {
		return 0, false
	}
	pb, ok := parseSemver(b)
	if !ok {
		return 0, false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseSemver 只认**严格**的 `vX.Y.Z`。
//
// ⚠️ 带预发布后缀（`v0.4.0-rc.1`）一律判为认不得，而**不是**猜一个次序 ——
// 本项目此刻没有预发布约定，编一个出来等于替将来的自己做决定。
// 真要用预发布，先把约定写进 roadmap，再来改这里。
func parseSemver(s string) ([3]int, bool) {
	var out [3]int
	if !strings.HasPrefix(s, "v") {
		return out, false
	}
	parts := strings.Split(s[1:], ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// mainBranchRow 取「分支与发布约定」那张表里 `main` 那一行。
//
// ⚠️ 找不到就 Fatal，**不回退到整篇文档** —— 回退会让守卫在一个更宽的锚上
// 看起来还在跑，而它守的已经不是同一样东西了。
func mainBranchRow(t *testing.T, body string) string {
	t.Helper()
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "| `main` |") {
			return l
		}
	}
	t.Fatal("⚠️ roadmap 里找不到「分支与发布约定」表的 `main` 那一行 —— " +
		"表格形状变了，本条守卫失效（**不回退到整篇文档**：那会让它守错东西）")
	return ""
}

// TestCheckTagsAgainstVersion 喂**合成** tag 测那条判据 —— 一个 tag 都不用真打。
//
// # ⚠️ 它是 20260912 评审第二次指出的那件事的兑现
//
// 破坏 418（「早于」放回「不等于」）与 419（认不得的 tag 悄悄放过）当时都是
// 「如预期仍然绿」，而我把它记成了与 403/409/413 同族的「结构性欠着」。
// **评审核出它不同族**：
//
//	403 / 409 / 413   判别力**在仓库之外** —— 要换柜台、要等夜盘
//	418 / 419         判别力**在仓库之内**，只是与 `git tag` 的 I/O 缠在一起
//
// ⚠️ 记错这一格的代价具体：**一条明天就能还的债，会和那些真要换柜台的条目
// 混在同一张表上** —— 而那张表是用来决定「先还哪一条」的。
//
// ⇒ 抽成纯函数 + 本条表驱动。418/419 从此是真红搭档，不再是登记的盲区。
func TestCheckTagsAgainstVersion(t *testing.T) {
	cases := []struct {
		name    string
		tags    []string
		version string
		want    []string // 期望的 Kind，按顺序
		// tagInMsg 断言那条信息里点的是**谁**。空串表示不查。
		tagInMsg string
	}{
		{"早于 ⇒ 无话", []string{"v0.3.0"}, "v0.4.0", nil, ""},
		{"空 tag 列表 ⇒ 无话", nil, "v0.4.0", nil, ""},
		{"等于在开发中的版本", []string{"v0.4.0"}, "v0.4.0",
			[]string{kindNotEarlier}, "v0.4.0"},
		// ⚠️ 这一格正是破坏 418 要翻的：`==` 放它过去，`>=` 才拦得住。
		{"晚于在开发中的版本", []string{"v0.5.0"}, "v0.4.0",
			[]string{kindNotEarlier}, "v0.5.0"},
		// ⚠️ 这一格是破坏 419 要翻的。
		{"tag 形状认不得", []string{"0.4.0"}, "v0.4.0",
			[]string{kindTagUnparseable}, "0.4.0"},
		{"tag 段数不对", []string{"v0.4"}, "v0.4.0",
			[]string{kindTagUnparseable}, "v0.4"},
		// ⚠️⚠️ **这一格是评审实测出来的那个必修**：Version 认不得时，
		// 原实现对着每一个**完好**的 tag 报「认不得的 tag 形状」，
		// 而没有一条提到 Version。触发条件不是假想的 ——
		// 使用者那次裁决的选项②就是 `v0.4.0-rc.1`。
		{"⚠️ Version 自己认不得 ⇒ 只报 Version，且点的是 Version",
			[]string{"v0.3.0", "v0.2.0"}, "v0.4.0-rc.1",
			[]string{kindVersionUnparseable}, "v0.4.0-rc.1"},
		{"多个 tag 各报各的", []string{"v0.3.0", "v0.9.0", "x"}, "v0.4.0",
			[]string{kindNotEarlier, kindTagUnparseable}, ""},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		got := checkTagsAgainstVersion(c.tags, c.version)
		var kinds []string
		for _, p := range got {
			kinds = append(kinds, p.Kind)
			seen[p.Kind] = true
		}
		if len(kinds) != len(c.want) {
			t.Errorf("%s：得到 %d 条（%v），要 %d 条（%v）",
				c.name, len(kinds), kinds, len(c.want), c.want)
			continue
		}
		for i := range kinds {
			if kinds[i] != c.want[i] {
				t.Errorf("%s：第 %d 条是 %s，要 %s", c.name, i, kinds[i], c.want[i])
			}
		}
		// ⚠️ **信息里点的是谁**，这一条才是那个必修的核心：
		// 判定对了而指错了地方，转述出去之后与指对了分不开。
		if c.tagInMsg != "" && len(got) > 0 &&
			!strings.Contains(got[0].Msg, c.tagInMsg) {
			t.Errorf("%s：信息里没有点到 %q —— 判定对了而**指错了地方**：\n    %s",
				c.name, c.tagInMsg, got[0].Msg)
		}
	}
	// ⚠️ 反空转：三种 Kind **每一种都要被上面这张表走到**。
	// 一张只覆盖两种的表，与一张覆盖全的表，在全绿时长得一模一样。
	for _, k := range []string{kindVersionUnparseable, kindTagUnparseable, kindNotEarlier} {
		if !seen[k] {
			t.Errorf("⚠️ %s 一条用例都没走到 —— 那一支从没被测过", k)
		}
	}
}
