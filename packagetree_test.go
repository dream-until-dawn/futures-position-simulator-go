package futsim

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestPackageTreeMatchesReality 把 design.md 的包树与 `go list ./...` 对照。
//
// ⚠️ 这条守卫是评审 F4 提的，而它抓到的那次漂移很值得记住其**形状**：
//
//	design.md 画着 settle/        —— 而实现是 position.Settle，没有 settle 目录
//	roadmap v0.2.0 也写着它        —— 「settle 的**完整**日终链路」
//	roadmap 的变更记录里查不到      —— 而 design.md 自己的规矩要求记一条
//
// ⚠️ 关键在于它与 `order/` `match/` `risk/` **不是一回事**：那三个也画着
// 但不存在，而它们是**合法的未来版本**。`settle` 的**职责已经在别处实现了** ——
// 那才叫漂移。一个「画了但不存在」的包，光看在不在是分不出这两种的。
//
// 所以判据是两条：
//
//	画了却不存在  → 必须在 roadmap.md 里点名（未来版本），或行里明写已并入别处
//	存在却没画    → 包树漏了一个，读的人会以为它不存在
//
// ⚠️ 本仓库现有的文档守卫（小节数、禁语表、计数）恰好都盖不到包边界这一块。
func TestPackageTreeMatchesReality(t *testing.T) {
	design, err := os.ReadFile(filepath.Join("docs", "design.md"))
	if err != nil {
		t.Fatal(err)
	}
	roadmap, err := os.ReadFile(filepath.Join("docs", "roadmap.md"))
	if err != nil {
		t.Fatal(err)
	}

	drawn := parseTree(t, string(design))
	real := realPackages(t)

	var missing, undrawn []string
	for name, line := range drawn {
		if real[name] {
			continue
		}
		// 画了但不存在：要么行里明写已并入别处，要么 roadmap 点名（未来版本）。
		leaf := name
		if i := strings.LastIndex(name, "/"); i >= 0 {
			leaf = name[i+1:]
		}
		merged := strings.Contains(line, "并入")
		planned := regexp.MustCompile("`" + regexp.QuoteMeta(leaf) + "`").MatchString(string(roadmap))
		if !merged && !planned {
			missing = append(missing, name)
		}
	}
	for name := range real {
		if _, ok := drawn[name]; !ok {
			undrawn = append(undrawn, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(undrawn)

	for _, n := range missing {
		t.Errorf("⚠️ design.md 的包树里画着 `%s/`，而它**不存在**，"+
			"roadmap.md 里也没点名 —— "+
			"⚠️ 这有两种可能，处理方式相反：它是**未来版本**（那就在 roadmap 里点名），"+
			"还是**职责已经在别处实现了**（那是漂移，照 design.md 自己的规矩："+
			"合并它，并在 roadmap.md 里记一条变更）", n)
	}
	for _, n := range undrawn {
		t.Errorf("⚠️ 包 `%s` 存在，而 design.md 的包树里**没画** —— "+
			"读包树的人会以为它不存在", n)
	}
	t.Logf("包树画了 %d 个、实际 %d 个；画了不存在且无归属 %d 个、存在没画 %d 个",
		len(drawn), len(real), len(missing), len(undrawn))
}

// parseTree 从 design.md 的包树里还原**全路径**。
//
// ⚠️ 包树是按缩进嵌套的：
//
//	refdata/            ← 2 空格：顶层
//	  live/             ← 4 空格：refdata/live
//
// 第一版只取叶子名，于是把 `refdata/live` 报成「画着 live/ 而它不存在」——
// **一条自己解析错了却言之凿凿的守卫**，比没有守卫更浪费时间。
func parseTree(t *testing.T, design string) map[string]string {
	t.Helper()
	lineRe := regexp.MustCompile(`^(\s+)~{0,2}([a-z][a-z0-9-]*(?:/[a-z][a-z0-9-]*)*)/~{0,2}\s{2,}(\S.*)$`)
	drawn := map[string]string{}
	parent := ""
	for _, line := range strings.Split(design, "\n") {
		line = strings.TrimRight(line, "\r")
		m := lineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		indent, name := len(m[1]), m[2]
		full := name
		if indent <= 2 {
			parent = name
		} else if parent != "" {
			full = parent + "/" + name
		}
		// 命令与工具不是核算包，另有归属。
		if strings.HasPrefix(full, "cmd") || strings.HasPrefix(full, "tools") {
			continue
		}
		drawn[full] = line
	}
	if len(drawn) < 8 {
		t.Fatalf("⚠️ 从 design.md 只解析出 %d 个包 —— "+
			"包树的画法变了，本条在几乎空的集合上跑", len(drawn))
	}
	return drawn
}

func realPackages(t *testing.T) map[string]bool {
	t.Helper()
	// ⚠️ 包列表来自子进程，缓存看不见它。先走一遍目录，让缓存看得见
	// 「多了一个包」这件事 —— 否则**在刚好发生了它要抓的那件事之后**，
	// `go test ./...` 会返回 ok（评审方 20260909 实测）。见 touchSourceTree。
	touchSourceTree(t)
	out, err := exec.Command("go", "list", "./...").Output()
	if err != nil {
		t.Fatalf("go list 失败：%v", err)
	}
	const mod = "futures-position-simulator-go/"
	real := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		p := strings.TrimSpace(line)
		i := strings.Index(p, mod)
		if i < 0 {
			continue
		}
		rel := p[i+len(mod):]
		if strings.HasPrefix(rel, "cmd/") || strings.HasPrefix(rel, "tools/") {
			continue
		}
		real[rel] = true
	}
	if len(real) < 6 {
		t.Fatalf("⚠️ go list 只给出 %d 个包 —— 本条在几乎空的集合上跑", len(real))
	}
	return real
}
