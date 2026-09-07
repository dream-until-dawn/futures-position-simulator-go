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
	"io/fs"
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

// ⚠️⚠️ 共享面清单 —— 双实现机制的**盲区清单**
//
// 双实现的判别力**全部来自两个实现的独立性**。任何被它们共享的东西，
// 都在一致性检查的射程之外：共享的代码一起变，两版一致地错，
// 而一致性检查只看是否一致。
//
//	可以共享（输入）：forbidden 表、archiveStart/End 常量、readLines、docFiles
//	                  —— 共享它们正是为了让两版跑在同一批数据上
//	不可共享（判定）：「什么算一个标记」、「什么算围栏」、豁免的四条判据
//	                  —— 各写各的，哪怕只有一行
//
// 这条界线是被实测逼出来的：曾有一个共享的 isMarker(line, marker) helper，
// 把它从「整行相等」改成「行内含有」，**两个实现一起退化、测试全绿**。
// 而合成样本里偏偏有一条就叫「标记只是被提到、不独占整行」——
// **名字精确对准了这个缺陷，结构上却不可能因它而失败。**
// 一个这样的用例比没有这条用例更糟：它让人以为这块被覆盖了。
//
// 界线：**共享数据可以，共享判定不行。**

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
			case strings.TrimSpace(l) == archiveStart:
				depth++
				startLines = append(startLines, i+1)
				if depth > 1 {
					t.Errorf("%s:%d 历史留档块嵌套（depth=%d），规则不允许嵌套", path, i+1, depth)
				}
			case strings.TrimSpace(l) == archiveEnd:
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
		if trimmed == archiveStart { // 判定之一：整行相等
			depth++
			exempt[i+1] = true
			continue
		}
		if trimmed == archiveEnd {
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
		// 判定之二：把它当 HTML 注释解析出内文再比，与 scanDepth 的写法刻意不同。
		if inner, ok := htmlCommentBody(l); ok && inner == "历史留档:start" {
			inArchive = true
			exempt[n] = true
			continue
		}
		if inner, ok := htmlCommentBody(l); ok && inner == "历史留档:end" {
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

// htmlCommentBody 把一整行解析成 HTML 注释的内文。
//
// 它是 scanFlag 侧对「什么算一个标记」的**独立判断**，与 scanDepth 的
// 「整行等于常量」不共用任何代码——见上方共享面清单。
func htmlCommentBody(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "<!--") || !strings.HasSuffix(t, "-->") {
		return "", false
	}
	return strings.TrimSpace(t[len("<!--") : len(t)-len("-->")]), true
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

// TestMarkerJudgmentIsAbsolute 是**绝对断言**，而且它打在扫描器的**可观测输出**上。
//
// ⚠️ 第一版不是这样写的，它抄了一份判定逻辑来断言，结果守错了东西：
//
//	gotStart1 := strings.TrimSpace(c.line) == archiveStart  // 测试自己重抄了一遍
//	inner, ok := htmlCommentBody(c.line)                    // 调的是真函数
//
// 前者是**副本**：改扫描器，副本不动，照样绿。
// 后者调了真货，但只要扫描器**不再调它**，函数还在、还是对的、还被测着，
// **只是没人用了**。
//
// 于是那一版证明的是「这两份判定逻辑是对的」，
// **不是「两个扫描器跑的是那两份判定」**——中间那根线没有任何东西在守。
// 实测：两侧扫描器同时退回「行内含有」、断言一个字不改 → 全绿。
//
// 这与前几轮抓到的是同一族，只是又高了一层：
//
//	一开始   有东西没被检查
//	上一轮   检查在某处悄悄降级成更弱的检查
//	这一轮   检查还在、还是对的，只是被测的代码已经不走它了
//
// 共同点仍是：**降级 / 脱钩本身不产生任何信号。**
//
// 界线因此有两条，方向相反：
//
//	两个实现之间：共享数据可以，共享判定不行
//	断言与被测代码之间：断言必须打在真实代码路径上
//
// **抄一份逻辑来断言，守的是副本；调一个函数来断言，守的是函数；
// 只有喂进入口，守的才是行为。**
func TestMarkerJudgmentIsAbsolute(t *testing.T) {
	cases := []struct {
		line    string
		isStart bool
		isEnd   bool
		why     string
	}{
		{"<!-- 历史留档:start -->", true, false, "标准写法"},
		{"  <!-- 历史留档:start -->  ", true, false, "两侧空白应被容忍"},
		{"<!-- 历史留档:end -->", false, true, "结束标记"},
		{"讲 <!-- 历史留档:start --> 这个标记", false, false, "⚠️ 只是被提到，不独占整行"},
		{"> ② 不在 `<!-- 历史留档:start -->` … `<!-- 历史留档:end -->` 块内；", false, false,
			"⚠️ state.md 定义标记的那一行，同时含 start 与 end"},
		{"<!-- 其它注释 -->", false, false, "别的 HTML 注释"},
		{"历史留档:start", false, false, "缺注释包裹"},
		{"", false, false, "空行"},
	}
	// 下界用确切条数，不是 > 0。
	if len(cases) != 8 {
		t.Fatalf("用例数应为 8，实际 %d —— 增删了就同步更新下界", len(cases))
	}

	scanners := []struct {
		name string
		fn   func([]string) map[int]bool
	}{{"深度计数版", scanDepth}, {"布尔开关版", scanFlag}}

	for _, c := range cases {
		// 每条样本造成两行文档：第 1 行是待判定的那一行，第 2 行是一句裸禁语。
		// 断言第 2 行**是否被豁免**——即扫描器有没有把第 1 行认成开始标记。
		// 期望值仍是手写死的，但走的是**真实代码路径**。
		openDoc := []string{c.line, "六条"}
		for _, sc := range scanners {
			if got := sc.fn(openDoc)[2]; got != c.isStart {
				t.Errorf("%s：把 %q 之后的一行判为豁免=%v，期望 %v（%s）",
					sc.name, c.line, got, c.isStart, c.why)
			}
		}

		// 对称地测结束标记：先真开一个块，再看这一行能不能把它关上。
		closeDoc := []string{archiveStart, c.line, "六条"}
		for _, sc := range scanners {
			// 第 3 行仍在块内 ⟺ 第 2 行**没有**关掉块。
			stillInside := sc.fn(closeDoc)[3]
			if stillInside == c.isEnd {
				t.Errorf("%s：%q 关闭留档块的能力判为 %v，期望 %v（%s）",
					sc.name, c.line, !stillInside, c.isEnd, c.why)
			}
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

// TestPackagesDoneMatchesReality 断言 state.md 的 packages_done 与磁盘一致。
//
// ⚠️ 这条针对的是「文档里写着、代码里没有」这类债 —— 上游数据层把它叫做
// 「从没被代码验证过的类型名与签名」。文档里的规划可以先于代码，
// 但**声称已完成的那部分必须能被机械核对**，否则「已落地」会悄悄变成「打算做」。
//
// 判据只覆盖主模块的顶层包（cmd/ 是嵌套模块，另算）。
func TestPackagesDoneMatchesReality(t *testing.T) {
	// 磁盘上：**递归**找含 .go 文件的目录，键是相对仓库根的路径。
	//
	// ⚠️ 第一版只扫顶层目录，于是 internal/decimalx 落在了它的视野之外：
	// state.md 一登记 decimalx 就报「磁盘上没有」。抓到的又是判据 ——
	// 「怎么算一个包」当时只想到了顶层。这是同一天第四次「守卫的第一次红
	// 是守卫自己的判据没写全」。
	onDisk := map[string]bool{}
	skip := map[string]bool{"docs": true, "testdata": true, "cmd": true}
	err := filepath.WalkDir(".", func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			base := e.Name()
			if path != "." && (strings.HasPrefix(base, ".") || skip[base]) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(e.Name(), ".go") {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		if dir == "." {
			return nil // 根包由 doc.go 代表，不算独立的包目录
		}
		onDisk[dir] = true
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败: %v", err)
	}
	if len(onDisk) == 0 {
		t.Fatal("⚠️ 磁盘上一个含 .go 的顶层包都没扫到 —— 判据本身可能坏了")
	}

	// state.md 里：packages_done 那一行
	var declared map[string]bool
	for _, l := range readLines(t, filepath.Join("docs", "state.md")) {
		if !strings.Contains(l, "packages_done") {
			continue
		}
		// ⚠️ 只取表格的**值列**（第 2 个单元格），不扫整行。
		//
		// 首次跑这条守卫时它报了两个假阳性：备注列里写着「（`ctperr` / `refdata`
		// 未开始）」，而按整行扫反引号会把它们当成「已落地」。
		// 抓到的是**解析规则没写清楚**，不是文档写错——与标记判定那次同族：
		// 「怎么算一个值」当时没有定义。
		cells := strings.Split(l, "|")
		if len(cells) < 3 {
			t.Fatalf("⚠️ packages_done 那一行不是三列表格：%q", l)
		}
		declared = map[string]bool{}
		for _, tok := range strings.Split(cells[2], "`") {
			if tok = strings.TrimSpace(tok); tok != "" && isPackageName(tok) {
				declared[tok] = true
			}
		}
		break
	}
	if declared == nil {
		t.Fatal("⚠️ state.md 里找不到 packages_done —— 单一状态源缺了这一项")
	}

	for pkg := range onDisk {
		if !declared[pkg] {
			t.Errorf("包 %s 已在磁盘上，但 state.md 的 packages_done 没写它 —— "+
				"单一状态源落后于代码", pkg)
		}
	}
	for pkg := range declared {
		if !onDisk[pkg] {
			t.Errorf("⚠️ state.md 声称 %s 已落地，磁盘上却没有含 .go 的该目录 —— "+
				"「已落地」不能是打算做", pkg)
		}
	}
}

// isPackageName 过滤掉值列里那些不是包名的记号。
//
// 包名是相对仓库根的路径，允许 `/`（如 internal/decimalx）。
func isPackageName(tok string) bool {
	for _, r := range tok {
		if !(r >= 'a' && r <= 'z' || r == '_' || r == '/') {
			return false
		}
	}
	return tok != ""
}
