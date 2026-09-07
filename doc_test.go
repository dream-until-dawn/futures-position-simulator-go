package futsim

// 文档守卫：把 docs/state.md 里的「派生禁语」规则落成机械检查。
//
// 只用 stdlib，不引入任何依赖——它跑在主模块里，而主模块的依赖树硬约束是
// 只有 shopspring/decimal 一个。
//
// ⚠️ 这份文件里**同一条规则被独立实现了两遍**（深度计数 / 布尔开关），
// 并断言两者给出同一个答案。理由见 TestForbiddenPhraseRuleIsWellDefined：
//
//	一份自然语言规则，只有当两个独立实现给出同一个答案时，才算被验证过。
//	不一致的时候，先别问哪一版对——先认定规则本身还没写完。

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	archiveStart = "<!-- 历史留档:start -->"
	archiveEnd   = "<!-- 历史留档:end -->"
	sourceOfTrue = "state.md"
)

// isMarker 判定一行**整行就是**某个标记。
//
// ⚠️ 用「整行相等」而不是「行内含有」，是被守卫的守卫当场逼出来的：
// state.md 在**定义**标记时必然要**提到**它们，
//
//	> ② 不在 `<!-- 历史留档:start -->` … `<!-- 历史留档:end -->` 块内；
//
// 这一行同时含有 start 与 end，按「含有」判定会被读成一个未闭合的 start，
// 于是 state.md 结束时 depth=1。
//
// 这与「字典必然包含它定义的每一个词」是同一个形状，只是从禁语层跑到了标记层。
// 而它暴露的是规则的另一处未定义：**「怎么算一个标记」当时根本没写。**
// 两个实现可以在「含有」与「整行是」上分道扬镳，而两条都读得通。
func isMarker(line, marker string) bool { return strings.TrimSpace(line) == marker }

// forbidden 是 docs/state.md「派生禁语」表的机械副本。
//
// ⚠️ 它必须与那张表同步。禁语由**状态键翻转时被删掉的字符串**生成，
// 不由想象力生成——新增一条的时机是「改状态那次提交」，不是「想起来的时候」。
var forbidden = []struct{ key, phrase string }{
	{"kq_login/simnow_login", "尚未实际登录"},
	{"kq_login/simnow_login", "待注册"},
	{"kq_login/simnow_login", "柜台账号还没有"},
	{"kq_login/simnow_login", "需要账号"},
	{"kq_login/simnow_login", "待用户提供"},
	{"kq_login/simnow_login", "外部依赖：已解除"},
	{"rules_pending", "六条"},
	{"rules_pending", "6 条待实测"},
	{"position_fields", "持仓 28 字段"},
}

func docFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	if _, err := os.Stat("README.md"); err == nil {
		out = append(out, "README.md")
	}
	entries, err := os.ReadDir("docs")
	if err != nil {
		t.Fatalf("读不到 docs 目录: %v", err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, filepath.Join("docs", e.Name()))
		}
	}
	sort.Strings(out)
	if len(out) < 7 {
		// ⚠️ 下界用确切下限而非 > 0：文档被误删时这条先红。
		t.Fatalf("扫到的文档只有 %d 份，少于预期的 7 份 —— 是不是路径错了或文件被删了？", len(out))
	}
	return out
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打不开 %s: %v", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("读 %s 失败: %v", path, err)
	}
	return lines
}

// TestArchiveMarkersBalance 是「守卫的守卫」。
//
// ⚠️ 标记写错时，失败方向朝着「什么也不报」：深度计数式实现下，
// 一个多余的 start 会让该文件之后的内容——甚至按字母序排在它后面的**其他文件**
// ——被整体豁免。0 处不是因为干净，是因为检查在半路闭了嘴。
//
// **一个豁免机制出 bug，比检查本身出 bug 更糟：它让检查更安静，不是更吵。**
func TestArchiveMarkersBalance(t *testing.T) {
	files := docFiles(t)
	for _, path := range files {
		depth, startLines := 0, []int{}
		for i, l := range readLines(t, path) {
			switch {
			case isMarker(l, archiveStart):
				depth++
				startLines = append(startLines, i+1)
				if depth > 1 {
					t.Errorf("%s:%d 历史留档块嵌套（depth=%d），规则不允许嵌套", path, i+1, depth)
				}
			case isMarker(l, archiveEnd):
				depth--
				if depth < 0 {
					t.Errorf("%s:%d 出现多余的 end（没有对应的 start）", path, i+1)
					depth = 0
				}
			}
		}
		if depth != 0 {
			t.Errorf("%s 文件结束时历史留档块未闭合（depth=%d，start 在 %v）——"+
				"多余的 start 会让检查静默地闭嘴", path, depth, startLines)
		}
	}
}

// scanDepth 是实现之一：把 start/end 当**深度计数**，逐文件重置。
func scanDepth(lines []string) map[int]bool {
	exempt := map[int]bool{}
	fence, depth := false, 0
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "```") {
			fence = !fence
			exempt[i+1] = true
			continue
		}
		if isMarker(l, archiveStart) {
			depth++
			exempt[i+1] = true
			continue
		}
		if isMarker(l, archiveEnd) {
			if depth > 0 {
				depth--
			}
			exempt[i+1] = true
			continue
		}
		exempt[i+1] = fence || depth > 0 ||
			strings.Contains(l, "~~") || strings.Contains(l, sourceOfTrue)
	}
	return exempt
}

// scanFlag 是实现之二：把 start/end 当**布尔开关**，写法与 scanDepth 刻意不同。
//
// 两个实现都合理，而 2026-09-07 它们在同一份文档上给出过不同答案（0 处 vs 4 处）
// ——那次分歧的根源不是任何一版有 bug，是**规则缺一条**（漏了字典自己）。
func scanFlag(lines []string) map[int]bool {
	exempt := make(map[int]bool, len(lines))
	inFence, inArchive := false, false
	for n := 1; n <= len(lines); n++ {
		l := lines[n-1]
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			inFence = !inFence
			exempt[n] = true
			continue
		}
		if isMarker(l, archiveStart) {
			inArchive = true
			exempt[n] = true
			continue
		}
		if isMarker(l, archiveEnd) {
			inArchive = false
			exempt[n] = true
			continue
		}
		switch {
		case inFence, inArchive:
			exempt[n] = true
		case strings.Contains(l, "~~"):
			exempt[n] = true
		case strings.Contains(l, sourceOfTrue):
			exempt[n] = true
		default:
			exempt[n] = false
		}
	}
	return exempt
}

type hit struct {
	file, phrase string
	line         int
}

func scanWith(t *testing.T, files []string, exemptOf func([]string) map[int]bool) []hit {
	t.Helper()
	var hits []hit
	for _, path := range files {
		lines := readLines(t, path)
		// ⚠️ 逐文件重置：块状态绝不跨文件带。
		exempt := exemptOf(lines)
		// 规则 ④ 前半句：state.md 是定义处，字典必然包含它定义的每一个词。
		if filepath.Base(path) == sourceOfTrue {
			continue
		}
		for i, l := range lines {
			if exempt[i+1] {
				continue
			}
			for _, f := range forbidden {
				if strings.Contains(l, f.phrase) {
					hits = append(hits, hit{path, f.phrase, i + 1})
				}
			}
		}
	}
	return hits
}

// TestForbiddenPhraseRuleIsWellDefined 断言两个独立实现给出同一个答案。
//
// ⚠️ 有用的不是任何一遍的结果，是**它们一致**。
// 一份两个合理实现会读出不同答案的规则，还不是规则——落成脚本时，
// 落成哪一版取决于写它的人当天怎么想。
//
// 这与「构造分歧样本」是同一件事，只是对象从数据换成了规则：
// 分歧样本证伪的是实现，双实现证伪的是**规范**。
func TestForbiddenPhraseRuleIsWellDefined(t *testing.T) {
	// ⚠️ 先用**合成样本**逐条豁免地测，再测真实文档。
	//
	// 只测真实文档是**空转的**：某条豁免在当下的文档里若没有唯一触发点
	// （例如唯一一处 `~~` 恰好也在历史留档块内），把那条豁免从其中一个实现里
	// 整个删掉，两版仍然一致——测试照样绿，而它本该抓到这个分歧。
	//
	// 这是本仓库反复在防的那个形状：**判别力取决于样本恰好长什么样，
	// 而不是取决于测试本身。** 合成样本让四条豁免每条都有唯一触发点。
	synth := []struct {
		name  string
		lines []string
	}{
		{"围栏内", []string{"```", "六条", "```"}},
		{"留档块内", []string{archiveStart, "六条", archiveEnd}},
		{"删除线", []string{"~~六条~~"}},
		{"含 state.md 链接", []string{"六条，见 [state.md](./state.md)"}},
		{"正文裸禁语（应命中）", []string{"六条"}},
		{"围栏未闭合", []string{"```", "六条"}},
		{"留档未闭合", []string{archiveStart, "六条"}},
		{"多余的 end", []string{archiveEnd, "六条"}},
		{"标记只是被提到、不独占整行", []string{"讲 " + archiveStart + " 这个标记", "六条"}},
	}
	if len(synth) != 9 {
		t.Fatalf("合成样本应为 9 组，实际 %d —— 增删了就同步更新下界", len(synth))
	}
	for _, c := range synth {
		da, db := scanDepth(c.lines), scanFlag(c.lines)
		for n := 1; n <= len(c.lines); n++ {
			if da[n] != db[n] {
				t.Errorf("合成样本「%s」第 %d 行：深度计数版豁免=%v，布尔开关版豁免=%v；"+
					"⚠️ 先别问哪一版对——先认定 docs/state.md 的豁免规则还没写完。",
					c.name, n, da[n], db[n])
			}
		}
	}

	files := docFiles(t)
	a, b := scanWith(t, files, scanDepth), scanWith(t, files, scanFlag)
	if len(a) != len(b) {
		t.Fatalf("两个独立实现给出不同答案：深度计数版 %d 处，布尔开关版 %d 处。\n"+
			"  ⚠️ 先别问哪一版对——先认定 docs/state.md 的豁免规则还没写完。\n"+
			"  深度计数版: %v\n  布尔开关版: %v", len(a), len(b), a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("第 %d 处不一致：%+v vs %+v", i, a[i], b[i])
		}
	}
}

// TestNoStaleForbiddenPhrases 是检查本身：正文叙述行里不得出现禁语。
func TestNoStaleForbiddenPhrases(t *testing.T) {
	hits := scanWith(t, docFiles(t), scanDepth)
	for _, h := range hits {
		t.Errorf("%s:%d 出现禁语「%s」——该状态已翻转，此处是过期陈述。\n"+
			"    若它确实是历史留档，用 <!-- %s --> / <!-- %s --> 包起来；\n"+
			"    若它在讲这套机制，加一条指向 docs/state.md 的链接。",
			h.file, h.line, h.phrase, archiveStart, archiveEnd)
	}
}

// TestForbiddenTableIsNotEmpty 是禁语表自己的下界断言。
//
// ⚠️ 一条没人添加的禁语等于没有检查。但「表非空」只防空表、不防漏表——
// 漏一条和只写一条在这个断言下长得一模一样。**这条断言的局限必须写在这里**，
// 免得后人看它绿了就以为禁语表是全的。
func TestForbiddenTableIsNotEmpty(t *testing.T) {
	const want = 9 // 下界用确切条数，不是 > 0
	if len(forbidden) != want {
		t.Fatalf("禁语表应有 %d 条，实际 %d —— 增删了就同步更新这个下界，"+
			"并确认 docs/state.md 的表也改了", want, len(forbidden))
	}
	keys := map[string]int{}
	for _, f := range forbidden {
		keys[f.key]++
	}
	for _, k := range []string{"kq_login/simnow_login", "rules_pending", "position_fields"} {
		if keys[k] == 0 {
			t.Errorf("状态键 %s 一条禁语都没有 —— 它翻转后不会有任何东西报警", k)
		}
	}
	fmt.Fprintf(os.Stderr, "禁语表：%d 条，覆盖 %d 个状态键\n", len(forbidden), len(keys))
}
