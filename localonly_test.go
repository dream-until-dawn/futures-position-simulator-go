package futsim

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// localOnlyPaths 是**只落本地、不进 git** 的证据目录。
//
// 使用者 2026-09-09 的裁决：柜台通知的**文案**要留下来，但不上 git ——
// 文案是自由文本，本仓库是 public、推上去不可撤。
var localOnlyPaths = []string{"testdata/probes/local/"}

// TestLocalOnlyEvidenceIsGitIgnored 断言那几个目录**真的被 git 挡着**。
//
// # 为什么这不能只是一条约定
//
// 「我们不提交它」是一句**没有执行力**的话，而它失败的方式是最坏的一种：
// 一次 `git add -A` 就够了，而且**推上去不可撤**。
//
// ⚠️ 这条守卫问的是 git 自己：`git check-ignore`。
// 不是去读 .gitignore 的文本 —— 那等于**把 git 的匹配规则再实现一遍**，
// 而两份实现分岔的那一天，分岔的方向恰好是「我以为挡住了」。
func TestLocalOnlyEvidenceIsGitIgnored(t *testing.T) {
	// ⚠️ **按名**的那一道要先验，因为它是唯一与 DumpDir 无关的一道。
	//
	// 评审方 20260909 指出：上一版这条测试问的是**枚举的那几个规范路径**，
	// 于是它证明的是「规范路径被忽略」，**不是「文案不会进库」** ——
	// 我把「保护」和「保护的一个实例」写成了同一件事。
	// 下面这几个落点都是真能发生的：旁档写在 filepath.Join(DumpDir, "local")，
	// 而 DumpDir 来自 -dump，或来自 .env 里那个**绝对路径**的 PROBE_DUMP_DIR。
	for _, p := range []string{
		"out/local/a.notify.json",
		"scratch/a.notify.json",
		"whatever/deep/b.notify.json",
		"testdata/probes/local/a.notify.json",
	} {
		if err := exec.Command("git", "check-ignore", "-q", filepath.FromSlash(p)).Run(); err != nil {
			t.Errorf("⚠️ %s **没有被忽略** —— 按名那条规则（`*.notify.json`）没生效。"+
				"⚠️ 只靠 `testdata/probes/local/` 那条路径规则是不够的："+
				"落点由使用者的 -dump / PROBE_DUMP_DIR 决定，**指到哪里都可能**", p)
		}
	}
	for _, p := range localOnlyPaths {
		probe := filepath.Join(filepath.FromSlash(p), "probe-check.json")
		cmd := exec.Command("git", "check-ignore", "-q", probe)
		if err := cmd.Run(); err != nil {
			t.Errorf("⚠️ %s **没有被 git 忽略**（check-ignore 退出码非零：%v）—— "+
				"那里放的是刻意不入库的证据（柜台通知文案），"+
				"而一次 git add -A 就会把它推上一个 public 仓库，**推上去不可撤**。"+
				"⚠️ 去看 .gitignore 是不是被改了或者路径挪了", p, err)
		}
	}
}

// TestCommittedFixturesCarryNoNotifyContent 断言**进了 git 的**夹具里没有通知文案。
//
// ⚠️ 它与上一条不是重复：上一条管「那个目录别进来」，
// 这一条管「文案别从**另一条路**混进主夹具」——
// 比如有人给 kq.Fixture 的 Notify 加个 Content 字段，那时上一条一个字都不会说。
//
// 两条各自堵一个方向，而**泄漏只需要一个方向没堵**。
func TestCommittedFixturesCarryNoNotifyContent(t *testing.T) {
	// ⚠️ 扫**全仓**，不是只扫 testdata。
	//
	// 上一版只扫 testdata，而旁档的落点由 DumpDir 决定 —— `-dump out` 之后
	// 文案落在 out/local/，这条守卫**够不着**。范围要跟着「可能落在哪」走，
	// 而那个「哪」是使用者的参数。
	out, err := exec.Command("git", "ls-files").Output()
	if err != nil {
		t.Fatalf("列不出已入库的夹具：%v", err)
	}
	files := strings.Fields(string(out))
	if len(files) < 100 {
		t.Fatalf("⚠️ 只列出 %d 个已入库文件 —— 太少，本条在空转", len(files))
	}
	// ⚠️ 判据不是「文件够多」，是「**范围真的覆盖了 testdata 之外**」。
	//
	// 只卡总数是不行的：缩回 `ls-files testdata` 仍有 101 个文件，
	// 任何一个宽松的下界都拦不住 —— 而缩回去正是这条守卫失效的方式。
	// 这一条直接问那件事本身。
	outside := 0
	for _, f := range files {
		if !strings.HasPrefix(f, "testdata/") {
			outside++
		}
	}
	if outside < 20 {
		t.Fatalf("⚠️ 扫到的 %d 个文件里只有 %d 个在 testdata 之外 —— "+
			"范围没有覆盖全仓。旁档的落点由 -dump / PROBE_DUMP_DIR 决定，"+
			"**指到哪里都可能**，所以这条守卫必须扫全仓", len(files), outside)
	}
	// ⚠️ 路径这一层单独查：文案文件**根本不该有一个进过库**。
	// 它与下面逐字段查 content 是两道不同的闸 —— 有人换个字段名装文案时，
	// 这一道仍然拦得住；反过来有人换个文件名时，下面那道仍然拦得住。
	for _, f := range files {
		if strings.HasSuffix(f, ".notify.json") {
			t.Errorf("⚠️ **已入库**的 %s 是通知文案旁档 —— 它只该落本地。"+
				"⚠️ 如果这次提交推出去就撤不回来：先把它从索引里拿掉"+
				"（git rm --cached），再查 .gitignore 的按名规则为什么没挡住", f)
		}
	}
	checked := 0
	for _, f := range files {
		if !strings.HasSuffix(f, ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.FromSlash(f))
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Notifies []map[string]json.RawMessage `json:"notifies"`
		}
		if json.Unmarshal(b, &doc) != nil {
			continue // 不是夹具形状，跳过
		}
		for _, n := range doc.Notifies {
			checked++
			if raw, ok := n["content"]; ok {
				t.Errorf("⚠️ 已入库的 %s 里，通知带着 content=%s —— "+
					"文案刻意不入库（使用者 20260909 裁决：只落本地）。"+
					"⚠️ 它已经在工作树里了，**如果这次提交推出去就撤不回来**："+
					"先把落盘那一侧改回去，再决定这份夹具怎么处理", f, raw)
			}
		}
	}
	if checked < 5 {
		t.Fatalf("⚠️ 只扫到 %d 条已入库的通知 —— "+
			"本条在空转（是不是 notifies 那一段没进夹具了？）", checked)
	}
	t.Logf("已入库夹具里的通知 %d 条，无一带文案；文案在 %v（git 忽略）",
		checked, localOnlyPaths)
}
