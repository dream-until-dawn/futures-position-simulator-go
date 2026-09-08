package futsim

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var kqRefRe = regexp.MustCompile(`kq_facts\s+(\d+)`)

// kqFactRows 读 state.md 那张表：编号 → 这一条是不是**已被推翻**（划了线）。
func kqFactRows(t *testing.T) map[int]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("docs", "state.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, "## `kq_facts`")
	j := strings.Index(s, "## `simnow_pending`")
	if i < 0 || j < 0 || j <= i {
		t.Fatal("⚠️ 在 state.md 里定位不到 kq_facts 那一节 —— 章节改名了？")
	}
	rows := map[int]bool{}
	for _, line := range strings.Split(s[i:j], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(cells[1]))
		if err != nil {
			continue
		}
		rows[n] = strings.HasPrefix(strings.TrimSpace(cells[2]), "~~")
	}
	return rows
}

// TestKQFactRefsResolve 断言每一处 `kq_facts N` 的引用**指向一条还成立的事实**。
//
// # 它堵的洞
//
// 一条事实被推翻之后，引用它的地方**不会有任何动静**。
// 20260909 当场抓到两处引的是 `kq_facts 14`，而第 14 条已被第 28 条推翻
// （结算后 `position_cost_*` 的拆分确实被填上了）——
// 其中一处在**分类表**里当依据，另一处在 probes 的结果表里。
//
// > ⚠️ 一条已被推翻的事实还在当依据，是「文档里的过期陈述」搬进了代码注释。
//
// # 讲与用要分开
//
// 引用一条**被推翻的**事实并说明它被推翻了，是正当的（本仓库到处这么写）。
// 判据因此是：引用行或它前后两行里出现「推翻」或「接续」，就算在讲它。
//
// ⚠️ **这是本仓库第 2 份「讲/用」判据**（邻近关键词）。另两份：
// fixtures_test.go 用**句法层**（剥掉引号内的片段），
// pkgdoc_test.go 用**作用域**（只扫包注释）。**三处不共享判定。**
// 这三处不是互相校验的双实现，是**同一个概念的三个消费者** ——
// 「共享数据可以，共享判定不行」是为前者立的，套到后者上只会得到
// 三份各自漂移的定义（评审方 20260909 指出）。写下这段是让漂移可见。
//
// ⚠️ 本处的豁免**已经出过一次事**：破坏 171 的第一版插在解释
// 「第 14 条已被推翻」的注释旁边，被这个 ±2 行的窗口一起豁免掉了，假绿。
// 边界两侧现在各钉一条：171 远离必红、173 紧邻必绿。
//
// # 它是「改陈述时记下支撑新措辞的观测」这条做法的机械那一半
//
// 完整的做法（评审方 20260909 提出）是：每改一处陈述，在旁边记下支撑
// **新措辞**的那次观测；记不出来就说明新旧同档，那就别写成肯定句。
// 那一整条没法机械化，但**「引到的编号必须还成立」这一半可以**。
func TestKQFactRefsResolve(t *testing.T) {
	rows := kqFactRows(t)
	struck := 0
	for _, s := range rows {
		if s {
			struck++
		}
	}
	// ⚠️ 判别力：表里必须**有**被推翻的条目，否则「不许引被推翻的」是空话。
	if len(rows) < 20 || struck == 0 {
		t.Fatalf("⚠️ 表里 %d 条、被推翻的 %d 条 —— "+
			"没有被推翻的条目时，这条测试的主判据是空真", len(rows), struck)
	}

	type ref struct {
		file string
		line int
		n    int
	}
	var refs []ref
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
		if !strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, ".md") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			for _, m := range kqRefRe.FindAllStringSubmatch(line, -1) {
				n, _ := strconv.Atoi(m[1])
				// 「讲」的豁免：前后两行里说了它被推翻/被接续。
				lo, hi := i-2, i+2
				if lo < 0 {
					lo = 0
				}
				if hi >= len(lines) {
					hi = len(lines) - 1
				}
				ctx := strings.Join(lines[lo:hi+1], "")
				if strings.Contains(ctx, "推翻") || strings.Contains(ctx, "接续") {
					continue
				}
				refs = append(refs, ref{filepath.ToSlash(p), i + 1, n})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走目录失败：%v", err)
	}
	// ⚠️ 下界用确切条数：走目录坏掉时这条会对空集返回「干净」。
	if len(refs) < 20 {
		t.Fatalf("⚠️ 只扫到 %d 处 kq_facts 引用 —— 太少，走目录很可能坏了", len(refs))
	}
	seen := map[int]bool{}
	for _, r := range refs {
		seen[r.n] = true
		switch {
		case !rows[r.n] && !kqExists(rows, r.n):
			t.Errorf("⚠️ %s:%d 引了 kq_facts %d，而表里**没有这一条** —— "+
				"一个指向不存在条目的引用，与「查过了」只差一步", r.file, r.line, r.n)
		case rows[r.n]:
			t.Errorf("⚠️ %s:%d 引了 kq_facts %d，而**那一条已被推翻**（表里划了线）—— "+
				"一条已被推翻的事实还在当依据，是「文档里的过期陈述」搬进了代码注释。"+
				"若这里是在**讲**它被推翻，把「推翻」二字写在附近两行内",
				r.file, r.line, r.n)
		}
	}
	nums := make([]int, 0, len(seen))
	for n := range seen {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	t.Logf("表里 %d 条（被推翻 %d）；全仓 %d 处引用，涉及 %d 个编号",
		len(rows), struck, len(refs), len(nums))
}

func kqExists(rows map[int]bool, n int) bool {
	_, ok := rows[n]
	return ok
}
