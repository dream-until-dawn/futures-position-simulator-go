package futsim

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// wipAllowed 是**已经存在**的三个 wip 提交，逐个写明它们的「为什么」落在哪。
//
// ⚠️ 它们不重写历史：`dev` 虽然实质单人，但已推 origin，
// 而评审会话正在按 SHA 追踪引用 —— 使用者 2026-09-07 也已就
// 「不重写历史」做过一次同类裁决。**这是名单，不是豁免条件**：
// 名单只挡住这三个，挡不住第四个。
var wipAllowed = map[string]string{
	"fcd3b49": "F1 的对数单一来源那一批。⚠️ 里面动了 order/order.go，" +
		"但全是注释（「刻意不写数字」那段重写）。为什么见 4415bd4 的提交信息" +
		"与 state.md 的 `reject_priority_measured` 键",
	"5d57dfb": "依赖树守卫。为什么写在 TestMainModuleHasOneDependency 的注释里" +
		"（design.md §3 的硬约束此前没有任何守卫）",
	"da57b08": "breakcheck 的多文件破坏。为什么见 3748242 的提交信息",
}

var wipRe = regexp.MustCompile(`(?i)^(wip\b|wip\d*$)`)

// TestNoNewWipCommits 断言**不再往 main 的方向送 `wip` 提交**。
//
// # 为什么这条值得有
//
// 这个仓库的价值有一半是它的记录 —— 证据等级、变更记录、每条守卫旁边的理由。
// ⚠️ 而 `git log` 此前**没有应用同一套纪律**：`main..dev` 里躺着三个 `wip`，
// 其中一个动了 `order/order.go`。将来有人问「这次为什么改」，
// `git log` 回答「wip18」（评审方 20260909 指出）。
//
// 而这个范围是要去 `main` 的，而 `main` 是公开的、
// 也是没有 tag 时 `go get` 拿到的那一个。
//
// # 为什么是名单而不是改写历史
//
// 改写历史要 force-push 一条已推的分支，而评审会话正在按 SHA 追踪引用；
// 使用者 2026-09-07 也就「不重写历史」做过一次同类裁决。
// 于是选了零风险的那条：**确认这三处的「为什么」各有落点**（见 wipAllowed），
// 并挡住第四个。
//
// ⚠️ 名单挡不住「把新提交也叫 wip 然后加进名单」—— 那需要人不自欺，
// 守卫做不到。它能做到的是：**让加进名单这个动作显式发生**。
// ⚠️ 这条测试依赖 `git log`，而 `go test` 的缓存**看不见** exec 出来的输出：
// git 状态变了而本包文件没变时，它会把上一次的 PASS 直接端出来。
// 20260909 当场撞到：新增一个 wip 提交后它报 `ok (cached)`，
// `go clean -testcache` 之后才红 —— 那一刻我差点据此认为守卫没生效。
// ⚠️ 缓解**不能**只写成「单独跑时加 -count=1」——那句方向是反的：
// 单独跑这条测试时人正盯着它，本来就最不容易被骗；
// **真正会骗到人的是 `go test ./...`**，那是被当成「全绿了」来引用的
// 那条命令，而它恰恰是会命中缓存的。所以缓解打在 touchGitState 上，
// 让缓存自己看得见 git 的状态。见 silent-risks 方法论 46。
func TestNoNewWipCommits(t *testing.T) {
	touchGitState(t) // ⚠️ 见它的注释：不读一遍 git 状态，这条测试会被缓存端出旧判决
	// ⚠️ 范围是**整条历史**，不是 `main..HEAD`。
	//
	// 上一版查的是 `main..HEAD`，那默认了「dev 永远领先 main」——
	// 而 2026-09-09 第一次真的合并之后，那个假设当场不成立：
	//
	//	main..HEAD 变成空       → 下面的空转下界 fatal
	//	wipAllowed 的三个 SHA   → 都跑到 main 那侧去了，名单一条也命中不了
	//
	// ⚠️ 后果比「一条守卫红了」严重：**`main` 跑不过自己的测试套件** ——
	// 任何人 clone 下 main 跑 `go test ./...` 都会看到红，
	// 而这个仓库把「全绿」当成对外的第一句话。
	//
	// 换成整条历史之后它与在哪个分支上跑无关，名单里的 SHA 也永远可达。
	// ⚠️ 这条守卫问的本来就是「有没有 wip 进过 main」，
	// 而那是一个关于**历史**的问题，不是关于**分支差**的问题 ——
	// 上一版把它写成了后者，因为当时两者恰好等价。
	out, err := exec.Command("git", "log", "--format=%h %s", "HEAD").Output()
	if err != nil {
		t.Skipf("⚠️ 数不出 git log，这条守卫**没有查任何东西**：%v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	checked, allowed := 0, 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		checked++
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		sha, subject := parts[0], parts[1]
		if !wipRe.MatchString(strings.TrimSpace(subject)) {
			continue
		}
		if why, ok := wipAllowed[sha]; ok {
			allowed++
			t.Logf("ⓘ %s（%s）在名单里：%s", sha, subject, why)
			continue
		}
		t.Errorf("⚠️ %s 的提交信息是 %q —— "+
			"这个范围要去 `main`，而 `main` 是公开的、也是没有 tag 时 "+
			"`go get` 拿到的那一个。⚠️ 这个仓库的价值有一半是它的记录，"+
			"而「wip」把「为什么改」这个问题留给了以后。"+
			"要么把它的信息补齐，要么把它连同理由加进 wipAllowed", sha, subject)
	}
	// ⚠️ 判别力两条：
	//   数不到提交 → 这条恒绿而什么都没查（分支名变了、浅克隆）
	//   名单一条都没命中 → 名单那一支从未被走到，它是不是还对得上？
	if checked < 50 {
		t.Fatalf("⚠️ 整条历史只数到 %d 个提交 —— 太少，这条很可能什么都没查"+
			"（浅克隆？）", checked)
	}
	if allowed != len(wipAllowed) {
		t.Errorf("⚠️ 名单里有 %d 条，实际命中 %d 条 —— "+
			"名单和历史对不上了：要么那几个 SHA 变了（历史被改写过？），"+
			"要么它们已经不在 main..HEAD 里，那就该把名单清掉",
			len(wipAllowed), allowed)
	}
	t.Logf("整条历史共 %d 个提交，wip 命中 %d 个（全在名单里）", checked, allowed)
}
