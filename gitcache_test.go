package futsim

import (
	"io/fs"
	"os"
	"os/exec"
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
// ⚠️⚠️ **这一行有实效，而我一度写反过。** 在**干净克隆**里做的 A/B：
//
//	                 填缓存后再跑    加一个 design.md 没画的包之后
//	e210c14 修复前    (cached)       **(cached)** ← 守卫根本没跑
//	db64cb7 修复后    (cached)        FAIL        ← 抓到了
//
// 判据是 `go test -run TestPackageTreeMatchesReality ./`，
// 而 `-run` 恰恰是**改一条守卫时最常用的跑法** —— 人盯着它改、跑、看到绿，
// 而那正是那八条巧合保护（kqref、pkgdoc、eol……）被 `-run` 排除在外的窗口。
//
// ⚠️ 我先前测出「对照组也红、这行什么也没多做」，那是**在主仓跑的实验**：
// 主仓里那些测试读全仓 .go、docs/*.md、.git 的 ref，
// **两次运行之间只要有任何东西碰过其中一个就是缓存未命中** ——
// 而它看起来和「-run 从不缓存」一模一样。当时我自己在编辑、评审方在跑破坏。
// **这是同一晚第三次「在会变的树上做实验」得出假结论**（见方法论 49）。
//
// 于是它护的不是「日常 go test ./... 」那条路（那条上巧合保护还在），
// 而是**过滤运行**那条路 —— 在那里它是唯一的保护。
//
// ⚠️ 它只覆盖**目录条目的增删**。文件内容变了而条目没变时它看不见 ——
// 所以「主模块只有一个依赖」那条守卫**不能只靠它**：新依赖体现为某个
// .go 文件里多一行 import，目录条目没变。那一条要 touchSourceBytes。
func touchSourceTree(t *testing.T) {
	t.Helper()
	seen := map[string]bool{}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == ".git" || n == "testdata" {
				return filepath.SkipDir
			}
			if abs, aerr := filepath.Abs(p); aerr == nil {
				seen[filepath.Clean(abs)] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("⚠️ 走目录失败：%v —— 这条测试会退回「可缓存」", err)
	}
	assertCoversEveryPackage(t, "走目录", seen)
}

// assertCoversEveryPackage 断言 touch 到的东西**覆盖了每一个包**。
//
// ⚠️ 下界不用一个手写的数字（那又撞回方法论 40：需要有人维护的参数会停住）。
// 判据是两个**互相独立**的来源之间的覆盖关系：
//
//	走文件系统的那一份    filepath.WalkDir 的结果
//	走构建系统的那一份    go list -f {{.Dir}} ./...
//
// 走歪了根目录、过滤器写窄了、布局不是预期的那种 —— 交集当场缺包，
// 而这个断言**不需要任何人去同步数字**（本条由评审方 20260909 提出）。
//
// ⚠️ 它堵的是这套 touch 机制自己的静默失效模式：**失败的方式是「什么也没读到」，
// 而那正好把测试还原成可缓存** —— 缓存键里什么也没多，测试照样「通过」。
func assertCoversEveryPackage(t *testing.T, what string, seen map[string]bool) {
	t.Helper()
	out, err := exec.Command("go", "list", "-f", "{{.Dir}}", "./...").Output()
	if err != nil {
		t.Fatalf("⚠️ go list 失败：%v —— 覆盖判据没法算，而算不出时"+
			"这套机制的失效是**静默**的", err)
	}
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			pkgs = append(pkgs, filepath.Clean(line))
		}
	}
	// ⚠️ 一个包都没报出来时，下面的循环是空真 —— 那正是最该报警的时候。
	if len(pkgs) == 0 {
		t.Fatal("⚠️ go list 一个包都没报出来 —— 覆盖判据会空真通过")
	}
	missing := 0
	for _, d := range pkgs {
		if !seen[d] {
			missing++
			t.Errorf("⚠️ 「%s」没覆盖到包目录 %s —— "+
				"这套机制失败的方式是「什么也没读到」，而那会把测试**静默还原成可缓存**："+
				"缓存键里什么也没多，测试照样通过", what, d)
		}
	}
	t.Logf("「%s」覆盖 %d 个包目录，缺 %d 个", what, len(pkgs), missing)
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
// ⚠️⚠️ **这一条的 A/B 我是在主仓做的，因此不可信** —— 与上面同一个毛病：
// 主仓在变，「对照组也红」很可能只是缓存被别的改动打掉了。
// ⚠️ 我**没有**在干净克隆里重做这一条（上面那条重做了，结论正好翻过来）。
// 所以这里如实记：**它的实效未经我自己验证**，
// 而按上面那条的结果推断它应当同样有效 —— 推断不是观测。
func touchSourceBytes(t *testing.T) {
	t.Helper()
	seen, read := map[string]bool{}, map[string]bool{}
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
		if abs, aerr := filepath.Abs(p); aerr == nil {
			read[filepath.Clean(abs)] = true
			seen[filepath.Clean(filepath.Dir(abs))] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("⚠️ 读源码失败：%v —— 这条测试会退回「可缓存」", err)
	}
	if total == 0 {
		t.Fatal("⚠️ 一个字节都没读到 —— 读源码那一步坏了，而坏掉之后测试会静默还原成可缓存")
	}
	assertCoversEveryPackage(t, "读源码", seen)
	assertReadEveryGoFile(t, read)
	t.Logf("读了 %d 个 .go、共 %d 字节", files, total)
}

// assertReadEveryGoFile 断言**每一个 .go 文件**都被读到了，不只是每个包一个。
//
// ⚠️ 粒度这一格是评审方 20260909 找出来的，而它恰好是这两条守卫**要求不同**
// 的那一格：
//
//	packagetree  触发 = 多了一个包        → 包级覆盖**正好够**
//	deps         触发 = 某个 .go 多一行 import → ⚠️ 那一行可能在任何一个文件里，
//	                                       包级覆盖**不够**
//
// 具体形态：过滤器哪天变窄（多一个 SkipDir、后缀条件改动、跳过 _test.go），
// 它仍可能**每个包读到一个文件** —— 于是包级覆盖照样通过，
// 而没被读到的那些文件里新增 import，缓存不会失效。
//
// ⚠️ 判据仍是两个独立来源，仍不需要有人维护数字：
// `go list` 报出的每一个 .go 都必须在读过的集合里。
//
// ⚠️ 方向是**包含**不是相等：走目录还会读到 `go list ./...` 报不出的文件
// （嵌套模块 cmd/oracle、被 build tag 排除的文件）—— 多读是安全的，
// 少读才是漏。把它写成相等会在嵌套模块上误报。
func assertReadEveryGoFile(t *testing.T, read map[string]bool) {
	t.Helper()
	out, err := exec.Command("go", "list",
		"-f", "{{.Dir}}|{{.GoFiles}}|{{.TestGoFiles}}|{{.XTestGoFiles}}", "./...").Output()
	if err != nil {
		t.Fatalf("⚠️ go list 失败：%v —— 文件级判据没法算", err)
	}
	want := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "|")
		if len(parts) < 2 {
			continue
		}
		dir := parts[0]
		for _, group := range parts[1:] {
			group = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(group), "["), "]")
			for _, name := range strings.Fields(group) {
				want++
				p := filepath.Clean(filepath.Join(dir, name))
				if !read[p] {
					t.Errorf("⚠️ 没读到 %s —— 包级覆盖挡不住这一格："+
						"过滤器变窄之后仍可能每个包读到一个文件，"+
						"而**没被读到的那些文件里新增 import，缓存不会失效**", p)
				}
			}
		}
	}
	// ⚠️ 一个文件都没报出来时下面是空真 —— 那正是最该报警的时候。
	if want == 0 {
		t.Fatal("⚠️ go list 一个 .go 都没报出来 —— 文件级判据会空真通过")
	}
	t.Logf("文件级覆盖：go list 报出 %d 个 .go，全部读到", want)
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
