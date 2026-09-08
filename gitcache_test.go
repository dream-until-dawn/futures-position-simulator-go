package futsim

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// touchSourceTree 让缓存**看得见「多了一个包」这件事**。
//
// ⚠️ 判据是 `os.ReadDir`（WalkDir 内部就是它）—— 20260909 在仓库外的
// 临时模块里实测过，**带对照组**：
//
//	往**没读过**的目录里加一个非 .go 文件   ok (cached)   ← 对照组
//	往**读过**的目录里加一个非 .go 文件     真跑了
//
// 对照组是关键：没有它，第二步的失效可以被解释成「加文件本身就会失效」。
// 用非 .go 文件也是刻意的 —— .go 会改包本身，那就分不开是 ReadDir 的功劳
// 还是重编的功劳。
//
// ⚠️⚠️ **如实记下 A/B 的结果：在本仓库，对照组也红了。**
// 把这个调用注释掉、再加一个 design.md 没画的包，`go test ./` **照样失败** ——
// 因为根包里已经有八个测试**碰巧**在走目录（kqref、pkgdoc、eol、simnow_ref……），
// 没有一个是为这件事写的。所以这行调用**现在什么也没多做**。
//
// 留着它的理由是另一条：**一份靠巧合成立的保护，与一份写明了的保护，
// 在绿的那一刻长得一模一样 —— 而前者会在某次无关的重构里安静消失。**
// 这里把偶然的依赖变成写下来的依赖，并带自己的零层（走不到足够多的目录就 Fatal）。
//
// ⚠️ 它只覆盖**目录条目的增删**。文件内容变了而条目没变时它看不见 ——
// 所以「主模块只有一个依赖」那条守卫**不能只靠它**：新依赖体现为某个
// .go 文件里多一行 import，目录条目没变。那一条要 touchSourceBytes。
func touchSourceTree(t *testing.T) {
	t.Helper()
	dirs := 0
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "testdata" {
				return filepath.SkipDir
			}
			dirs++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("⚠️ 走目录失败：%v —— 这条测试会退回「可缓存」", err)
	}
	if dirs < 10 {
		t.Fatalf("⚠️ 只走到 %d 个目录 —— 走目录很可能坏了，"+
			"而坏掉之后缓存看不见新包，守卫会在「刚好发生了它要抓的那件事」之后返回 ok", dirs)
	}
}

// touchSourceBytes 让缓存**看得见某个 .go 文件多了一行 import** 这件事。
//
// ⚠️ 目录级的 touchSourceTree 在这里**不够**：新增一个依赖不改目录条目，
// 只改某个文件的内容。评审方 20260909 实测「只改 go.mod 不失效」——
// 而 `go.mod` 压根不在根包测试的缓存键里。
//
// ⚠️ 于是这条真读一遍全仓 .go 的字节。代价是任何一次源码改动都会让
// 依赖守卫重跑 —— 那正是它该有的依赖关系。
//
// ⚠️⚠️ **同样如实记下：这一条的 A/B 对照组也红了。** 注释掉它、
// 只改 `order/order.go` 的**内容**（不动任何目录条目），根包照样重跑 ——
// 因为 kqref_test.go 已经在读全仓 .go/.md 的内容了。理由同上：
// 把偶然变成写下来的，而不是因为它现在多做了什么。
func touchSourceBytes(t *testing.T) {
	t.Helper()
	files, total := 0, 0
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		files++
		total += len(b)
		return nil
	})
	if err != nil {
		t.Fatalf("⚠️ 读源码失败：%v —— 这条测试会退回「可缓存」", err)
	}
	if files < 50 || total == 0 {
		t.Fatalf("⚠️ 只读到 %d 个 .go / 共 %d 字节 —— 读源码那一步很可能坏了", files, total)
	}
}

// touchGitState 让 `go test` 的缓存**看得见 git 的状态**。
//
// # 为什么需要它
//
// 缓存跟踪的是**测试进程打开过的文件**与环境变量；
// ⚠️ **子进程的输出不在其中**。于是一条靠 `exec.Command("git", …)` 判断的
// 测试，在「git 状态变了而本包文件没变」时会被直接端出上一次的 PASS。
//
// 2026-09-09 当场撞到：新增一个 `wip` 提交后守卫报 `ok (cached)`，
// `go clean -testcache` 之后才红。⚠️ **失败方向要记住**：
//
//	守卫失效   该红没红
//	判决过期   该红没红      ← 一模一样，而它会让人去改一个没坏的东西
//
// # 做法
//
// 用**普通文件 API** 读一遍「随提交变化的那几个文件」，缓存就看得见了。
// 读到即可，内容不用。新提交落下 → 那些文件变 → 缓存失效 → 测试真跑。
//
// ⚠️ 它替换掉的是「记得加 `-count=1`」——那是一个**需要有人记得的参数**，
// 而它停着的时候一切看起来正常（silent-risks 方法论 40 / 46）。
//
// ⚠️ 三种布局都要兜住，否则它会**安静地退回可缓存**：
//
//	松散引用    .git/refs/heads/<分支>   —— 提交时被改写
//	打包引用    .git/packed-refs         —— 松散文件可能不存在
//	reflog      .git/logs/HEAD           —— HEAD 每动一次就追加
//
// 一个都读不到就 `Fatal`：读不到而继续跑，正是它要防的那件事。
func touchGitState(t *testing.T) {
	t.Helper()
	git := ".git"
	if fi, err := os.Stat(git); err == nil && !fi.IsDir() {
		// ⚠️ worktree / submodule 里 .git 是**文件**，内容形如 `gitdir: …`。
		b, rerr := os.ReadFile(git)
		if rerr != nil {
			t.Fatalf("⚠️ .git 是文件却读不出来：%v —— "+
				"这条测试会退回「可缓存」，而缓存里的绿是过期的判决", rerr)
		}
		git = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "gitdir:"))
	}
	read := 0
	if b, err := os.ReadFile(filepath.Join(git, "HEAD")); err == nil {
		read++
		line := strings.TrimSpace(string(b))
		if ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:")); ref != line {
			if _, err := os.ReadFile(filepath.Join(git, filepath.FromSlash(ref))); err == nil {
				read++
			}
		}
	}
	for _, p := range []string{
		filepath.Join(git, "packed-refs"),
		filepath.Join(git, "logs", "HEAD"),
	} {
		if _, err := os.ReadFile(p); err == nil {
			read++
		}
	}
	// ⚠️ 只读到 HEAD 一个是不够的：切分支才会改它，**提交不会**。
	// 所以要求至少两个 —— HEAD 之外还得有一个随提交变的。
	if read < 2 {
		t.Fatalf("⚠️ 只读到 %d 个 git 状态文件（%s 下）—— "+
			"随提交变化的那几个一个都没读到，这条测试会退回「可缓存」，"+
			"而那时它的绿是**上一次的判决**，不是这一次的", read, git)
	}
}

// TestTouchGitStateReadsSomethingThatChanges 断言上面那个机制**真的读到了东西**。
//
// ⚠️ 它是 touchGitState 的零层：那个函数一旦读不到（布局变了、路径拼错），
// 会**安静地**让所有依赖它的测试退回可缓存 —— 而退回之后一切看起来正常。
func TestTouchGitStateReadsSomethingThatChanges(t *testing.T) {
	touchGitState(t) // 读不到会在里面 Fatal
	// 再单独确认「随提交变的那一个」确实存在，而不是只靠 HEAD 凑数。
	b, err := os.ReadFile(filepath.Join(".git", "HEAD"))
	if err != nil {
		t.Skipf("⚠️ 读不到 .git/HEAD（可能是 worktree 布局）：%v —— 本条没有查任何东西", err)
	}
	line := strings.TrimSpace(string(b))
	ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
	loose := filepath.Join(".git", filepath.FromSlash(ref))
	_, looseErr := os.Stat(loose)
	_, packedErr := os.Stat(filepath.Join(".git", "packed-refs"))
	_, logErr := os.Stat(filepath.Join(".git", "logs", "HEAD"))
	if looseErr != nil && packedErr != nil && logErr != nil {
		t.Error("⚠️ 松散引用、packed-refs、reflog 三者都不存在 —— " +
			"没有任何一个文件会随提交变化，缓存看不见 git 的状态")
	}
	t.Logf("git 状态文件：松散引用 %v / packed-refs %v / reflog %v",
		looseErr == nil, packedErr == nil, logErr == nil)
}
