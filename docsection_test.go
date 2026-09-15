package futsim

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// headingNumRe 认带编号的标题：`## 13. …`、`### 10.2 …`、`#### 11. F7：…`。
var headingNumRe = regexp.MustCompile(`^#+\s+(\d+(?:\.\d+)*)[.\s、：:]`)

// sectionRefRe 认「某文档 … §编号」：文档名与 § 之间不超过 24 个字符、且中间没有别的 § 或 .md（见 sectionRefs）。
var sectionRefRe = regexp.MustCompile(`([\w\-]+\.md)`)

var sectionNumRe = regexp.MustCompile(`^§(\d+(?:\.\d+)*)`)

type sectionRef struct{ doc, num string }

// checkSectionRef 判一条引用：文件名不在 docs/ 与 README 里、或那份文档里没有这个编号的标题，返回原因；解析得了返回空。
//
// ⚠️ 文件名不认识也报（评审 20260915）：原先直接放过，于是文件名写错（少个 s）或某份文档改名 / 删掉之后，
// 所有「旧名.md §N」都静默通过 —— 那正是悬空引用的一种。F7 设计时全仓这类引用 0 条，今天零误报；真要引仓库外的文档，再加白名单。
func checkSectionRef(heads map[string]map[string]bool, r sectionRef) string {
	nums, known := heads[r.doc]
	if !known {
		return "文件名 " + r.doc + " 不在 docs/ 与 README 里 —— 写错了、改名了，或删了"
	}
	if !nums[r.num] {
		return "那份文档里没有这个编号的标题 —— 改了编号、删了节，或引的是别的分支才有的节"
	}
	return ""
}

// sectionRefs 从一行里取出「文档 + 节号」引用：每个 § 归给它**前面最近**的那个 .md 文件名（24 个字符以内、中间没有别的 §）。
//
// ⚠️ 归给最近的那个，不是第一个：「fidelity.md 与 cn-futures-rules.md 第 10 节」那种写法里的节号是 cn-futures-rules 的（第一版脚本就这样误报过三条）。
func sectionRefs(line string) []sectionRef {
	var out []sectionRef
	docs := sectionRefRe.FindAllStringIndex(line, -1)
	for i := 0; i < len(line); i++ {
		if !strings.HasPrefix(line[i:], "§") {
			continue
		}
		m := sectionNumRe.FindStringSubmatch(line[i:])
		if m == nil {
			continue
		}
		// 最近的、结束在 § 之前的 .md
		best := -1
		for j, d := range docs {
			if d[1] <= i {
				best = j
			}
		}
		if best < 0 {
			continue
		}
		gap := line[docs[best][1]:i]
		if len([]rune(gap)) > 24 || strings.Contains(gap, "§") {
			continue
		}
		out = append(out, sectionRef{doc: line[docs[best][0]:docs[best][1]], num: m[1]})
	}
	return out
}

// TestDocSectionRefsResolve 断言「某文档 §编号」的引用在那份文档里有同编号的标题。
//
// ⚠️ 起因（评审 20260915）：一处注释引了 design.md 的第 11 小节，而那一节只在另一个分支上 —— 合进 main 就指向一个不存在的节，
// 路径检查（TestDocPathRefsResolve）与测试名检查（TestDocTestRefsResolve）都看不见节号。
// 第一次跑就抓到一条从写下那天起就悬空的：`types/order.go` 引 probes.md 第 10 节的第 5 小节，而那一节只到 10.4。
//
// ⚠️ 只查「这个编号的标题在那份文档里存在」，不查「在引用者说的那个大节下面」：design.md 在不同大节里各自从 1 编号，
// 查归属要先定义大节的边界，而一条误报不断的守卫最后一定会被关掉。这个取舍的盲区：编号在别的大节里碰巧存在时漏报。
// 扫的是 docs/*.md、README.md 与全部 .go（testdata 与 .git 除外）—— 那条悬空引用就在 Go 注释里。
func TestDocSectionRefsResolve(t *testing.T) {
	// 判别力：解析器自己先过一遍
	for _, c := range []struct {
		line string
		want []sectionRef
	}{
		// ⚠️ § 与编号拆开写：这个文件自己也在扫描范围里
		{"见 design.md「门面的形状」§" + "11", []sectionRef{{"design.md", "11"}}},
		{"[fidelity.md](./fidelity.md) 与 cn-futures-rules.md §" + "10。", []sectionRef{{"cn-futures-rules.md", "10"}}},
		{"cn-futures-rules.md §" + "13 #5 与 §" + "4", []sectionRef{{"cn-futures-rules.md", "13"}}},
		{"probes.md 里讲过（离得太远的不归它）………………………………………………………… §" + "2", nil},
	} {
		got := sectionRefs(c.line)
		if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
			t.Errorf("sectionRefs(%q) = %v，期望 %v", c.line, got, c.want)
		}
	}

	heads := map[string]map[string]bool{}
	defer func() {
		// 判别力：判定函数自己两个方向都过一遍（语料里今天没有写错文件名的引用，只靠全仓扫描的话这一支永远走不到）
		if why := checkSectionRef(heads, sectionRef{"silent-risk.md", "3"}); !strings.Contains(why, "不在 docs/") {
			t.Errorf("⚠️ 写错的文件名 silent-risk.md 没被报出来：%q", why)
		}
		if why := checkSectionRef(heads, sectionRef{"cn-futures-rules.md", "13"}); why != "" {
			t.Errorf("反向：cn-futures-rules.md 第 13 节存在，却报了 %q", why)
		}
	}()
	docs, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range append(docs, "README.md") {
		b, err := os.ReadFile(d)
		if err != nil {
			t.Fatal(err)
		}
		nums := map[string]bool{}
		for _, l := range strings.Split(string(b), "\n") {
			if m := headingNumRe.FindStringSubmatch(l); m != nil {
				nums[m[1]] = true
			}
		}
		heads[filepath.Base(d)] = nums
	}

	var files []string
	for _, d := range docs {
		files = append(files, d)
	}
	files = append(files, "README.md")
	err = filepath.WalkDir(".", func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && (e.Name() == ".git" || e.Name() == "testdata") {
			return filepath.SkipDir
		}
		if !e.IsDir() && strings.HasSuffix(p, ".go") {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)

	refs := 0
	var bad []string
	for _, p := range files {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for n := 1; sc.Scan(); n++ {
			for _, r := range sectionRefs(sc.Text()) {
				refs++
				if why := checkSectionRef(heads, r); why != "" {
					bad = append(bad, p+":"+strconv.Itoa(n)+" 引 "+r.doc+" §"+r.num+"："+why)
				}
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range bad {
		t.Errorf("⚠️ %s", b)
	}
	t.Logf("节号引用 %d 条，解析不了 %d 条", refs, len(bad))
	// ⚠️ 扫描没在空转：F7 设计时全仓有两百来条；少到这个量级以下多半是正则或文件列表坏了
	if refs < 150 {
		t.Errorf("⚠️ 只认出 %d 条节号引用 —— 正则或扫描范围坏了？", refs)
	}
}
