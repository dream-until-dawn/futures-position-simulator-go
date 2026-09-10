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
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
//
// ⚠️ 这条只对**互相校验的双实现**成立 —— 两份必须独立，分歧才有信息。
// 它**不适用于同一个概念的多个消费者**：本仓库有三处各自实现了
// 「讲与用要分开」（fixtures_test.go 句法层、kqref_test.go 邻近关键词、
// pkgdoc_test.go 作用域），对它们套这条规则，得到的是三份会各自漂移的
// 定义，而没有任何机制会发现漂移（评审方 20260909 指出）。
// 三处各写了一句交叉引用，让漂移至少**可见**。

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
	// ⚠️ 2026-09-09 新增。fidelity.md 开头挂着「当前状态：文档阶段，尚无实现」
	// 从 09-07 一直挂到 09-09，而这期间 16 个包落地、跨日对拍跑通、
	// oracle conformance 已能连着柜台跑。
	//
	// ⚠️ 它是一条**低报**，而低报同样是过期陈述：
	// 把「已实现」说成「尚无实现」，读的人会以为整份文档写的都是计划，
	// 于是不会去核对那些**已经有结论**的条目。
	// 评审门禁① 明写「含文档里的过期陈述，**低报也算**」。
	{"packages_done", "文档阶段，尚无实现"},
	{"packages_done", "尚无实现"},
	// ⚠️ 2026-09-09 再增两条，来历比上面那两条更该记：
	// 同一条低报的**第三处**藏在 doc.go 的包注释里
	// （「本包目前只有包声明与文档守卫，核算逻辑尚未落地」），
	// 而它躲过了这张表整整两天 —— 因为扫描只看 README.md 与 docs/*.md，
	// **不看 Go 源码**。而包注释恰恰是这个库最公开的一句话：
	// go doc 与 pkg.go.dev 显示的就是它。
	// 扫描范围已加上 doc.go，见 docFiles。
	{"packages_done", "核算逻辑尚未落地"},
	{"packages_done", "只有包声明与文档守卫"},
}

// TestForbiddenScanCoversDocGo 断言禁语扫描**看得见包注释**。
//
// ⚠️ 2026-09-09 之前它看不见：同一条低报在 README.md 与 fidelity.md 上
// 都被抓到过，唯独 doc.go 里那一处躲了两天 —— 而那是 `go doc` 与
// pkg.go.dev 显示的那句话，**比 README 还先被看到**。
//
// ⚠️ 这条守卫盯的是**扫描的覆盖面**，不是扫描的结论。两者是两件事：
// 结论对不对由 TestNoStaleForbiddenPhrases 管，而一个扫不到某类文件的
// 扫描，会在那类文件上永远返回「干净」—— 那与真的干净长得一模一样。
func TestForbiddenScanCoversDocGo(t *testing.T) {
	files := docFiles(t)
	var hasDoc, hasReadme, mdCount = false, false, 0
	for _, f := range files {
		switch {
		case f == "doc.go":
			hasDoc = true
		case f == "README.md":
			hasReadme = true
		case strings.HasSuffix(f, ".md"):
			mdCount++
		}
	}
	if !hasDoc {
		t.Error("⚠️ 禁语扫描不看 doc.go —— 包注释是这个库最公开的一句话，" +
			"而它会在那里永远返回「干净」")
	}
	if !hasReadme {
		t.Error("⚠️ 禁语扫描不看 README.md")
	}
	// ⚠️ 下界用确切条数：docs 下少了几份文档，扫描照样「通过」。
	if mdCount < 7 {
		t.Errorf("⚠️ 只扫到 %d 份 docs/*.md —— 少于 7 份，多半是目录读错了", mdCount)
	}
	t.Logf("禁语扫描覆盖：doc.go %v，README.md %v，docs/*.md %d 份",
		hasDoc, hasReadme, mdCount)
}

func docFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	if _, err := os.Stat("README.md"); err == nil {
		out = append(out, "README.md")
	}
	// ⚠️ doc.go 也算「文档」。它躲过这张表整整两天：同一条低报的第三处
	// 就藏在包注释里，而**包注释是这个库最公开的一句话** ——
	// go doc 与 pkg.go.dev 显示的就是它，比 README 还先被看到。
	// ⚠️ 只加 doc.go，不加全部 .go：那会把「禁语」变成「禁词」，
	// 而代码注释里讨论这些字符串是正当的（这一段自己就是例子）。
	if _, err := os.Stat("doc.go"); err == nil {
		out = append(out, "doc.go")
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
	const want = 13 // 下界用确切条数，不是 > 0
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
		// ⚠️ `package main` 是**命令**，不是交付的库包，不进 packages_done。
		//
		// 按**种类**跳而不是按名字跳：把 `tools` 加进上面那张 skip 表也能让
		// 这条绿，但那会造成一个盲区 —— 以后谁在 tools/ 下放一个真的库包，
		// 它会连同工具一起逃掉。按 package 子句判，逃不掉。
		if isCommandDir(t, dir) {
			return nil
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

// ⚠️ 「怎么算一张计数表里的一行」——判据先写死在这里，再写检查逻辑。
//
// 这一步是被方法论第 7 条逼出来的：守卫的第一次红有很大概率是判据自己没写全，
// 而共同病因永远是「怎么算一个 X」当时根本没写。所以先写定义：
//
// 一行是**一条在册项**，当且仅当：
//
//	① 它在 sectionPrefix 开头的那个二级标题与下一个 "## " 之间；
//	② 它以 "|" 开头（是表格行）；
//	③ 第 2 个单元格去空白后是一个**正整数**（表头行、分隔行、说明行都不是）；
//	④ 第 3 个单元格**不以 "~~" 开头**——删除线表示这条已经收敛、不再在册。
//
// 第 ④ 条是关键：收敛的条目**留在表里**（历史可查），但不计数。
// 若把它们直接删掉，「这条曾经是问题」这件事就没了。
func numberedTableRows(t *testing.T, path, sectionPrefix string) []string {
	t.Helper()
	var rows []string
	in := false
	for _, l := range readLines(t, path) {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "## ") {
			in = strings.HasPrefix(trimmed, sectionPrefix)
			continue
		}
		if !in || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) < 4 {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(cells[1])); err != nil {
			continue // 表头 / 分隔行 / 别的表
		}
		if strings.HasPrefix(strings.TrimSpace(cells[2]), "~~") {
			continue // 已收敛，留档但不在册
		}
		rows = append(rows, trimmed)
	}
	return rows
}

// allNumberedTableRows 与 numberedTableRows 同源，但**连已收敛的一起数**。
//
// ⚠️ 两者的差正是「测掉了几条」。分开数是刻意的：
// `rules_pending` 要的是**还欠着几条**，`rules_listed` 要的是**一共问过几条** ——
// 而只记前者的话，一条被测掉的项会让分母缩小，
// **分子涨、分母缩，比值会朝两个方向同时变好看**。
func allNumberedTableRows(t *testing.T, path, sectionPrefix string) (all, struck []string) {
	t.Helper()
	in := false
	for _, l := range readLines(t, path) {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "## ") {
			in = strings.HasPrefix(trimmed, sectionPrefix)
			continue
		}
		if !in || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) < 4 {
			continue
		}
		if _, err := strconv.Atoi(strings.TrimSpace(cells[1])); err != nil {
			continue
		}
		all = append(all, trimmed)
		if strings.HasPrefix(strings.TrimSpace(cells[2]), "~~") {
			struck = append(struck, trimmed)
		}
	}
	return all, struck
}

// declaredCount 取 state.md 计数表里某个键的**加粗值**。
//
// ⚠️ 两条定义都是踩出来的，缺一条就取错数：
//
//	怎么算「声明该键的那一行」 → 键出现在**键列**，不是行里含有
//	怎么算「该键的值」        → 值列里 **N** 包着的那个，不是行里第一个数字
//
// 前一条是本函数第一次跑就红的原因：查 kq_facts 时它取到了 1。
// 因为 rules_measured 那一行的**备注列**里写着「理由见下方 `kq_facts`」，
// 而那行排在前面——按「行里含有」判定，先命中的是它，取回的是它的值。
// ⚠️ 这已经是同一形状的第七次：**「怎么算一个 X」当时根本没写。**
//
// 后一条防的是备注列里的历史数字（「从 7 涨到 10」）。
func declaredCount(t *testing.T, key string) int {
	t.Helper()
	for _, l := range readLines(t, filepath.Join("docs", "state.md")) {
		cells := strings.Split(l, "|")
		if len(cells) < 3 {
			continue
		}
		if strings.TrimSpace(cells[1]) != "`"+key+"`" {
			continue
		}
		v := cells[2]
		i := strings.Index(v, "**")
		if i < 0 {
			continue
		}
		j := strings.Index(v[i+2:], "**")
		if j < 0 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v[i+2 : i+2+j]))
		if err != nil {
			t.Fatalf("⚠️ %s 的值列不是加粗的整数：%q", key, v)
		}
		return n
	}
	t.Fatalf("⚠️ state.md 里找不到 `%s` 的加粗值 —— 单一状态源缺了这一项", key)
	return -1
}

// assertCountMatchesTable 是两条计数守卫共用的骨架。
func assertCountMatchesTable(t *testing.T, key, path, sectionPrefix string) {
	t.Helper()
	rows := numberedTableRows(t, path, sectionPrefix)
	if len(rows) == 0 {
		// ⚠️ 一个「找不到就通过」的检查，在章节改名或表格重排时也会通过——
		// 而那正是它最该报警的时候。
		t.Fatalf("⚠️ %s 的 %s 里一条在册项都没解析到 —— 是真的清空了，还是解析规则失效了？"+
			"两种情形下这条检查都会「通过」，所以这里必须失败", path, sectionPrefix)
	}
	if declared := declaredCount(t, key); declared != len(rows) {
		t.Errorf("⚠️ state.md 的 %s = %d，但 %s 的 %s 里在册 %d 条",
			key, declared, path, sectionPrefix, len(rows))
		for _, r := range rows {
			cells := strings.Split(r, "|")
			t.Logf("    在册：#%s %s", strings.TrimSpace(cells[1]), strings.TrimSpace(cells[2]))
		}
	}
}

// TestRulesPendingMatchesTable 断言 state.md 的 rules_pending 与 §13 表实际在册的条数一致。
//
// ⚠️ 这条守卫针对的正是 state.md 存在的理由。计数类复述栽过两次，
// 而两次都躲过了禁语扫描——**计数不是状态词，人眼扫过去根本不会停**。
// 现在这个数有了一个会在提交前红的机械核对。
func TestRulesPendingMatchesTable(t *testing.T) {
	assertCountMatchesTable(t, "rules_pending",
		filepath.Join("docs", "cn-futures-rules.md"), "## 13.")
}

// TestRejectPriorityPairsMatchTable 断言「实测判出几对」与那张表一致。
//
// ⚠️ 这条守卫是被一次真实的不一致逼出来的：20260909 当天这个数从 3 涨到 4，
// 而**五处复述里只有一处跟上了**（另有一处就在同一份 state.md 里，
// 与新值自相矛盾）。而这个数说的正是「生产代码里有多少顺序是猜的」——
// 它是那一批里最不该含糊的数字。
//
// 现在其余各处一律链接、不抄数，这里做机械核对。
func TestRejectPriorityPairsMatchTable(t *testing.T) {
	assertCountMatchesTable(t, "reject_priority_measured",
		filepath.Join("docs", "state.md"), "## `reject_priority_measured`")
}

// TestKQFactsMatchesTable 断言 state.md 的 kq_facts 与它自己那张表的条数一致。
//
// ⚠️ 这条是**在 state.md 自己身上**栽了一次之后补的：
// 表已经长到 9 条，而同一页上方的散文还写着「夜盘量到六条」。
// 唯一状态源自己也会过期，而它过期时同样不会有任何动静——
// **「唯一来源」保证的是不该有第二处，不保证那一处是对的。**
func TestKQFactsMatchesTable(t *testing.T) {
	assertCountMatchesTable(t, "kq_facts",
		filepath.Join("docs", "state.md"), "## `kq_facts`")
}

// TestMethodologyItemsAreContiguous 断言 silent-risks.md 的方法论条目编号
// 从 1 连续到 N，无空号、无重号。
//
// ⚠️ **先说清它抓不到什么。** 它抓的是「条目被删掉 / 被重复编号」，
// 抓不到「小节标题被删掉」——而后者刚刚真的发生过：
// 我用脚本替换锚点 `--- \n ## 怎么用这份清单` 时，替换文本的结尾没把锚点带回去，
// 于是那个小节标题连同分隔线被**静默删除**，正文项目符号照常留着，看不出异样。
// 那次是我自己核出来的，不是守卫。**这条守卫不掩盖那个缺口。**
//
// 它仍然值得存在：这份文档的价值全在这串编号上——
// 别处引用它们时写的是「见第 11 条」，编号一乱，所有引用同时失效而没有任何动静。
func TestMethodologyItemsAreContiguous(t *testing.T) {
	re := regexp.MustCompile(`^\*\*(\d+)\. `)
	var got []int
	for _, l := range readLines(t, filepath.Join("docs", "silent-risks.md")) {
		if m := re.FindStringSubmatch(l); m != nil {
			n, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("条目编号 %q 不是整数", m[1])
			}
			got = append(got, n)
		}
	}
	// ⚠️ 迭代次数下界：一条都没解析到时，下面的循环空转，本测试会「通过」。
	if len(got) < 20 {
		t.Fatalf("只解析到 %d 条方法论条目 —— 是真的这么少，还是编号格式变了？"+
			"两种情形下本条都会「通过」，所以这里必须失败", len(got))
	}
	for i, n := range got {
		if n != i+1 {
			t.Errorf("⚠️ 第 %d 个条目的编号是 %d，应为 %d —— 编号断了，"+
				"而别处「见第 N 条」的引用会同时失效且不会有任何动静", i+1, n, i+1)
			break
		}
	}

	// 引用完整性：正文里出现的「第 N 条」不得超出实际条数。
	ref := regexp.MustCompile(`第 (\d+) 条`)
	for i, l := range readLines(t, filepath.Join("docs", "silent-risks.md")) {
		for _, m := range ref.FindAllStringSubmatch(l, -1) {
			n, _ := strconv.Atoi(m[1])
			if n > len(got) {
				t.Errorf("silent-risks.md:%d 引用了「第 %d 条」，但只有 %d 条", i+1, n, len(got))
			}
		}
	}
}

// TestDocSectionCountsMatch 断言各文档的小节数与 state.md 登记的一致。
//
// ⚠️ 它针对的是一次**真实发生过的静默丢失**：脚本替换锚点时，替换文本的结尾
// 没把锚点带回来，`silent-risks.md` 的一个小节标题连同分隔线被删掉。
// 正文照常留着，渲染没有异样，全库测试全绿——是人核编号时撞见的。
//
// ⚠️ 为什么不扫「历史里出现过、现在没有的标题」：评审试过，13 个候选**全是改名**
// （多为证据等级变了导致标题跟着变）。一个今天就 100% 误报的检查会被关掉，
// 与 `restatement-count.sh` 注释里那种死法同族。
//
// **小节数则是干净的判据：改名不动它，删除会动它。**
// 代价是增删小节要同步改 state.md 一行——这是刻意的摩擦。
//
// ⚠️ **这条抓的是「净减少」，不是「有东西被删」。**
//
// 评审实测过一个补偿性改动：删掉一个标题、同时在别处加一个，小节数不变，**全绿**。
// 我复现了：`## 怎么用这份清单` 确实没了，而守卫一声不吭。
// 现实相关性不是零——我踩的那次正是脚本替换锚点区域而没把锚点带回来，
// 同一个脚本再多改一段，就会是「吞掉一个标题、引入另一个」。
//
// 不修，而且理由要写下来：堵住它得把**标题列表**而不是标题数登记进 state.md，
// 那样每次改名都要改表——而全库历史里 13 次标题变动**全部是改名**，
// 误报率正是我和评审一起否掉「历史标题扫描」的理由。
// 把同一个问题从一个机制搬到另一个机制，它会原样跟过来。
//
// ⚠️ 取舍依据是实测的变动分布，不是「聚合更简单」；
// 若哪天删除比改名更频繁，这个选择就该翻过来。见 silent-risks.md 第 27 条。
//
// ⚠️ 这条也**不覆盖**「小节被改成了错的内容」，只覆盖「小节整个没了」。
func TestDocSectionCountsMatch(t *testing.T) {
	// state.md 里那张表。按 "|" 切列、去掉反引号 —— 不用正则：
	// 这个表达式要匹配 markdown 的反引号，写成 Go 字符串字面量既难读又容易错转义。
	want := map[string]int{}
	in := false
	for _, l := range readLines(t, filepath.Join("docs", "state.md")) {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "## ") {
			in = strings.HasPrefix(trimmed, "## `doc_sections`")
			continue
		}
		if !in || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) < 3 {
			continue
		}
		name := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if !strings.HasSuffix(name, ".md") {
			continue // 表头、分隔行
		}
		n, err := strconv.Atoi(strings.TrimSpace(cells[2]))
		if err != nil {
			t.Fatalf("%s 那一行的小节数 %q 不是整数", name, strings.TrimSpace(cells[2]))
		}
		want[filepath.ToSlash(name)] = n
	}
	// ⚠️ 迭代次数下界：表没解析到时下面的循环空转，本条会「通过」。
	if len(want) < 8 {
		t.Fatalf("只从 state.md 解析到 %d 个文档的小节数 —— 是真的这么少，还是表格格式变了？"+
			"两种情形下本条都会「通过」，所以这里必须失败", len(want))
	}

	// ⚠️ 盲区，写出来：这个正则**不数 `#### `**（四级标题）。
	// 于是一整个四级小节可以被静默删掉，而这条守卫一个字都不会说。
	//
	// 20260909 加 ctp-oracle.md 时撞见的：`grep -c "^##"` 数出 14，
	// 守卫说 12，差的正是两个 `####`。
	// ⚠️ 不改成 `^#+ ` 是因为那要把**每一份文档**的登记数重新数一遍，
	// 而重数的过程本身就是一次「按现状对齐」—— 那会把此刻可能已经
	// 存在的静默删除**一起固化进去**。要改就得先单独核一遍每份文档。
	head := regexp.MustCompile(`^###? `)
	for path, n := range want {
		got := 0
		for _, l := range readLines(t, filepath.FromSlash(path)) {
			if head.MatchString(l) {
				got++
			}
		}
		if got != n {
			t.Errorf("⚠️ %s 有 %d 个小节，state.md 登记 %d 个 —— "+
				"若是有意增删，同步改 state.md 那一行；否则很可能是一次**静默删除**",
				path, got, n)
		}
	}
}

// isCommandDir 报告一个目录里的包是不是 `package main`。
//
// ⚠️ 解析包子句而不是看目录名：目录名是约定，包子句是事实。
// 而这里要的正是事实 —— 「它是不是一个命令」。
func isCommandDir(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读目录 %s 失败：%v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(),
			filepath.Join(dir, e.Name()), nil, parser.PackageClauseOnly)
		if err != nil {
			t.Fatalf("解析 %s 的包子句失败：%v", filepath.Join(dir, e.Name()), err)
		}
		// ⚠️ 一个目录里只可能有一个包子句（测试包除外），看第一个就够。
		return f.Name.Name == "main"
	}
	return false
}

// goneTests 是**文档里提到、代码里已经没有**的测试名，逐个说明理由。
//
// ⚠️ 它必须逐条写理由，且**只能收「讲历史的那种引用」**。
// 一个可以随手加名字的豁免表，比没有这张表更坏 —— 那时
// TestDocTestRefsResolve 会退化成「把红的那个加进白名单」。
// neverWereTests 是**文档里写出、但它从来就不是一条测试**的名字。
//
// ⚠️ 与 goneTests 分开，不是洁癖：goneTests 的含义是「曾经有，后来没了」，
// 把一个从没存在过的名字塞进去，会在那张表里种一句假话 ——
// 将来读的人会据此以为它曾经存在过。
//
// ⚠️ 同样只收「讲历史」的引用，同样逐条写理由。
// 而且下面额外钉一条：**表里的名字一旦真的成了测试，本条要红** ——
// 否则这张表会烂在原地，替一个已经能解析的名字继续开着口子。
var neverWereTests = map[string]string{
	"TestPositionFrozen": "⚠️ 它是 breaks.json 里破坏 70 的 `test` 字段写错的那个值 —— " +
		"probe 包里只有四个更长的名字。breakcheck 锚定跑 `-run '^…$'`，" +
		"于是它选中零条测试、退出 0，**每一次全量运行都把那条破坏报成「如预期仍然绿」**。" +
		"⚠️ 文档里那几处写出这个字符串，讲的正是**这个名字不解析**这件事本身，" +
		"改写成一个真实测试名会把整段更正抹掉。见 silent-risks.md 那一节的 20260910 更正",
}

var goneTests = map[string]string{
	"TestShortHistoryCostHasNeverBeenObserved": "⚠️ 它**完成使命之后被删掉了**：" +
		"20260909 16:20 结算后空头昨仓第一次出现，绊线如期变红，" +
		"判定写进了 kq_facts 52（拆分方向中性，假说 B 被否）。" +
		"⚠️ 文档里那几处提到它的地方讲的是**那件事怎么发生的** —— " +
		"改写成「某条现存守卫」会把「绊线红了就该删掉它」这条做法本身抹掉，" +
		"而那正是它最值得留下的部分",
	"TestIsProseDiscriminates": "方法论 18 讲的是**当时发生的那件事**" +
		"（「我验了新加的测试」不等于「我验了套件」），那个测试后来删了。" +
		"⚠️ 把这句话改写成现在的测试名会把教训的现场感抹掉 —— " +
		"它记的不是一条现存的守卫，是一次真实的误判",
}

// TestDocTestRefsResolve 断言文档里点名的每一个测试**都还在**。
//
// # 它补的洞
//
// 文档大量用「守卫 `TestXxx`」来把一条结论钉到一条测试上 ——
// 那是本仓库把「事实」与「机制」连起来的主要方式。
// ⚠️ 而**重命名一条测试不会让任何文档变红**。
//
// 20260909 当场抓到一个：probes.md §9.2 写着
// 「这条已经落成测试（`view.TestOracleLeavesTodayHisCostAtZero`），
// 它断言的是夹具而不是本库：哪天柜台开始填了，会有动静」。
//
//	柜台确实开始填了，那条测试也确实红了、也确实被修正并**改了名**
//	（现在叫 TestOracleTodayHisSplit）——
//	而 §9.2 的正文**至今还是旧结论**，指着一个不存在的测试。
//
// ⚠️ 最讽刺的地方在于：那条测试写下来的**全部理由**就是「会有动静」。
// 机制尽到了责任，而**指向机制的那句话**没有任何东西看着。
func TestDocTestRefsResolve(t *testing.T) {
	have := map[string]bool{}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range testFuncRe.FindAllSubmatch(b, -1) {
			have[string(m[1])] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(have) < 100 {
		t.Fatalf("⚠️ 只扫到 %d 个测试函数 —— 太少，这条在空转", len(have))
	}

	refs := map[string][]string{}
	for _, p := range docFilesToScan(t) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range testRefRe.FindAllSubmatch(b, -1) {
			name := string(m[0])
			refs[name] = append(refs[name], p)
		}
	}
	if len(refs) < 20 {
		t.Fatalf("⚠️ 文档里只认出 %d 个测试引用 —— 太少，正则八成不对，本条在空转", len(refs))
	}

	var missing []string
	for name, where := range refs {
		if have[name] {
			continue
		}
		if why, ok := goneTests[name]; ok {
			t.Logf("ⓘ %s 已不在代码里，按 goneTests 放行：%s", name, why)
			continue
		}
		if why, ok := neverWereTests[name]; ok {
			t.Logf("ⓘ %s 从来就不是一条测试，按 neverWereTests 放行：%s", name, why)
			continue
		}
		sort.Strings(where)
		missing = append(missing, fmt.Sprintf("%s（%s）", name, strings.Join(where, "、")))
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("⚠️ 文档点名的测试 %s **在代码里不存在** —— "+
			"多半是改了名或删了，而**重命名一条测试不会让任何文档变红**。"+
			"⚠️ 修法是把文档指到现在那条上；只有当那句话讲的是**历史**"+
			"（记一次误判、一次翻案）时，才把它加进 goneTests 并写清理由", m)
	}
	// ⚠️ 豁免表要会自己过期：名字一旦真的成了测试，这条豁免就该拆掉。
	// 不然它会替一个**已经能解析**的名字继续开着口子，而没有任何东西会说。
	for name, why := range neverWereTests {
		if have[name] {
			t.Errorf("⚠️ neverWereTests 里的 %s **现在真的是一条测试了** —— "+
				"把它从表里删掉，让 TestDocTestRefsResolve 正常解析它。"+
				"原来的理由：%s", name, why)
		}
	}
	t.Logf("文档点名的测试 %d 个，代码里有 %d 个测试函数，"+
		"已不在的 %d 个（goneTests）、从来不是测试的 %d 个（neverWereTests）",
		len(refs), len(have), len(goneTests), len(neverWereTests))
}

var (
	testFuncRe = regexp.MustCompile(`func (Test[A-Za-z0-9_]+)\s*\(`)
	testRefRe  = regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9_]+`)
)

// docFilesToScan 是要扫的文档集合：docs 下的 .md、README、以及包文档。
//
// ⚠️ doc.go 也算：它是 `go doc` 会显示的那一份，读它的人**看不到 docs/**。
func docFilesToScan(t *testing.T) []string {
	t.Helper()
	var out []string
	ms, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, ms...)
	out = append(out, "README.md", "doc.go")
	sort.Strings(out)
	return out
}

// TestDocPathRefsResolve 断言文档里指的**路径**都指得到东西。
//
// # 两类引用，同一个病
//
//	[适用边界](docs/fidelity.md)     markdown 相对链接
//	`cmd/oracle/kq/order.go`         反引号里的路径
//
// ⚠️ 两类都是「文档指着仓库里的某个东西」，而**移动或改名一个文件
// 不会让任何文档变红** —— 与 TestDocTestRefsResolve 补的是同一个洞，
// 只是那边指的是测试名，这边指的是路径。
//
// 20260909 第一次跑就抓到一个：state.md 第 30 条写 `kq/order.go`，
// 而那个文件在 `cmd/oracle/kq/order.go`。⚠️ 它**不是坏链接，是省略的链接** ——
// 写的人知道上下文，读的人得自己猜。两者在文档里长得一样。
//
// ⚠️ 反引号那一类刻意只查**带斜杠**的：裸文件名（`order.go`）在多个包里都有，
// 查它会得到一堆假阳性，而**一条不断误报的守卫最后一定会被关掉**。
// 这个取舍写出来：`README.md` 这种裸名字因此不在保护范围内。
func TestDocPathRefsResolve(t *testing.T) {
	links, paths := 0, 0
	var bad []string
	for _, p := range docFilesToScan(t) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(p)
		for _, m := range mdLinkRe.FindAllSubmatch(b, -1) {
			target := string(m[1])
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			if i := strings.IndexByte(target, '#'); i >= 0 {
				target = target[:i]
			}
			if target == "" {
				continue
			}
			links++
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(target))); err != nil {
				bad = append(bad, p+" 的链接 "+target)
			}
		}
		for _, m := range tickPathRe.FindAllSubmatch(b, -1) {
			target := string(m[1])
			if !strings.Contains(target, "/") {
				continue // 裸文件名不查，理由见上
			}
			paths++
			if _, err := os.Stat(filepath.FromSlash(target)); err != nil {
				bad = append(bad, p+" 的路径 `"+target+"`（⚠️ 也可能只是**省略了前缀**，"+
					"那同样要补全：写的人知道上下文，读的人得猜）")
			}
		}
	}
	sort.Strings(bad)
	for _, x := range bad {
		t.Errorf("⚠️ %s —— 指不到东西", x)
	}
	// ⚠️ 两个下界分开卡：两类引用各自的正则都可能单独失效，
	// 而合在一起数的话，一类归零会被另一类盖住。
	if links < 50 {
		t.Fatalf("⚠️ 只认出 %d 条 markdown 相对链接 —— 本条在空转", links)
	}
	// ⚠️ 下界不是「应该有这么多」，是「归零了就说明正则不再匹配」。
	// 现值 17 条（20260909），卡在 10 —— 留出正常增删的余地，
	// 而正则一旦失效得到的是 0，离 10 很远。
	// ⚠️ 把下界贴着现值写会让每一次正常删除都变红，而**一条老是误报的守卫
	// 最后一定会被关掉** —— 那比没有它更坏。
	if paths < 10 {
		t.Fatalf("⚠️ 只认出 %d 条反引号路径 —— 本条在空转", paths)
	}
	t.Logf("markdown 相对链接 %d 条、反引号路径 %d 条，全部指得到", links, paths)
}

var (
	mdLinkRe = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)
	// ⚠️ 这个模式里有反引号，写不成 Go 的原始字符串，只能用带转义的那种 ——
	// 于是点号要写成两个反斜杠加点。写错一次的表现是**编译不过**，
	// 那反而是好消息：换成少一个反斜杠而仍然合法的写法，它会安静地匹配错。
	tickPathRe = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.(?:go|json|md|sh|yml))`")
)

// TestRulesListedMatchesTable 断言 `rules_listed` 与 §13 表里**所有**编号行一致。
//
// # 为什么要有第二个分母
//
// 2026-09-09 使用者裁决升格 `kq_facts` 37 时，暴露了一件记账的事：
// 那条规则**从没被写进 §13**——它不是解决了一条挂着的未知，
// 是回答了一个**没人问过**的未知。
//
// 而 `rules_pending` 只数**还欠着的**（已收敛的行划掉、不在册）。于是：
//
//	只记「解决」不记「发现」  →  分子涨、分母不动
//	而已收敛的行会离开分母    →  分子涨、分母**缩**
//
// ⚠️ 两个方向叠在一起，比值会凭空变好看，而每一步单看都合理。
// `rules_listed` 就是那个**只增不减**的分母：一共问过几条。
//
//	rules_listed = rules_pending + 已收敛
//
// 这条恒等式在下面被断言 —— 它是三个数**互相咬住**的地方，
// 单独钉住任何一个都挡不住「三个数各自漂开」。
func TestRulesListedMatchesTable(t *testing.T) {
	path := filepath.Join("docs", "cn-futures-rules.md")
	all, struck := allNumberedTableRows(t, path, "## 13.")
	if len(all) == 0 {
		t.Fatal("⚠️ §13 一条编号行都没解析到 —— 章节改名了？本条在空转")
	}
	pending := numberedTableRows(t, path, "## 13.")

	if got := declaredCount(t, "rules_listed"); got != len(all) {
		t.Errorf("⚠️ state.md 的 rules_listed = %d，而 §13 一共 %d 条编号行（含已收敛 %d 条）",
			got, len(all), len(struck))
	}
	// ⚠️ 恒等式：一共问过的 = 还欠着的 + 已收敛的。
	if len(all) != len(pending)+len(struck) {
		t.Errorf("⚠️ 对不上：一共 %d 条、在册 %d 条、已收敛 %d 条 —— "+
			"两个解析规则分岔了（多半是划掉的写法变了）",
			len(all), len(pending), len(struck))
	}
	// ⚠️ rules_measured 不从表里派生：一条**已收敛**只说明它被测掉了，
	// 不说明它有第二个独立来源 —— 后者才是升格的门槛（见 state.md 的升格判据）。
	// 但**上界**是硬的：有第二来源的必然已收敛，所以它不能比已收敛还多。
	// 这条挡的是「分子被单独抬高」。
	if m := declaredCount(t, "rules_measured"); m > len(struck) {
		t.Errorf("⚠️ rules_measured = %d，而 §13 里已收敛的只有 %d 条 —— "+
			"一条有第二个独立来源的规则必然已经收敛，所以分子不可能比它大。"+
			"⚠️ 要么是升格记错了，要么是某条收敛了却没在表里划掉", m, len(struck))
	}
	t.Logf("§13：一共问过 %d 条，还欠 %d 条，已收敛 %d 条；rules_measured %d",
		len(all), len(pending), len(struck), declaredCount(t, "rules_measured"))
}

// TestDocSectionsTableCoversEveryDoc 断言 `doc_sections` 那张表**没漏掉任何一份文档**。
//
// # ⚠️ 它补的洞
//
// `TestDocSectionCountsMatch` 是**按表驱动**的：它遍历 state.md 里登记的行，
// 逐个去核对小节数。于是 —— **一份没被登记的文档，它一个字都不会说。**
//
//	新加一份 docs/xxx.md 而忘了登记
//	  → 小节数守卫不查它
//	  → 它可以被静默删掉一整节，而没有任何东西变红
//
// ⚠️ 而「忘了登记」正是新加文档时最容易发生的一步：写完文档的人
// 想的是文档的内容，不是某张计数表。
//
// 2026-09-09 加 `docs/ctp-oracle.md` 时当场发现的：那份文档**已经登记了**，
// 但如果不登记，上面那条守卫照样全绿。
func TestDocSectionsTableCoversEveryDoc(t *testing.T) {
	listed := map[string]bool{}
	in := false
	for _, l := range readLines(t, filepath.Join("docs", "state.md")) {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "## ") {
			in = strings.HasPrefix(trimmed, "## `doc_sections`")
			continue
		}
		if !in || !strings.HasPrefix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		if len(cells) < 3 {
			continue
		}
		name := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if strings.HasSuffix(name, ".md") {
			listed[filepath.ToSlash(name)] = true
		}
	}
	if len(listed) < 5 {
		t.Fatalf("⚠️ 只从 doc_sections 表里解析到 %d 份文档 —— 太少，本条在空转", len(listed))
	}

	found, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) < 5 {
		t.Fatalf("⚠️ docs/ 下只找到 %d 份 .md —— 太少，本条在空转", len(found))
	}
	for _, f := range found {
		key := filepath.ToSlash(f)
		if !listed[key] {
			t.Errorf("⚠️ %s **没有登记进 doc_sections** —— "+
				"于是 TestDocSectionCountsMatch 一个字都不会说它："+
				"那条守卫是按表驱动的，没登记就不查。"+
				"⚠️ 它可以被静默删掉一整节而没有任何东西变红。"+
				"把它加进 state.md 的 doc_sections 表", key)
		}
	}
	t.Logf("doc_sections 登记 %d 份，docs/ 下实有 %d 份", len(listed), len(found))
}

// TestDateRoleMatchesFormat 断言**说了角色词的**日期，格式与角色一致。
//
//	交易日 → `20260910`（无分隔符）
//	自然日 → `2026-09-09`（带横杠）
//
// 约定本身写在 cn-futures-rules.md 的「⚠️ 交易日 ≠ 自然日」一节。
//
// # ⚠️ 它**抓不到**引发它的那个错，这一点必须写在最前面
//
// 这条守卫的起因是一句写错的署名：「双方在 **20260910** 的排查中确立」——
// 那天的自然日是 2026-09-09，交易日才是 20260910，结算后两者差一天。
//
// 而那句话里**根本没有角色词**，于是：
//
//	现有守卫（TestSessionLabelsAreQualified）  不匹配
//	本守卫                                     ⚠️ **也不匹配**
//
// > **判据是按「能机械化的形状」设计的，不是按「实际发生的那个错」设计的。**
//
// 这与 `margin.PriceBasis` 那次是同一个毛病：候选集是想出来的 ——
// 上次是排查原因，这次是设计守卫。
//
// ⚠️ 所以**别把它读成「日期格式这块已经保住了」**：
// 它守的是**已经写对角色词**的那些，而没写角色词的那一大类一个都不碰，
// 而错恰恰出在那一类里。**一个只守住简单一半的守卫，比没有守卫更容易让人放松。**
//
// # 它仍然值得有
//
// 现状是错配 0 处 —— 它是个**棘轮**：今天加进去零成本、零返工，
// 只锁住已经对的状态，防的是以后有人写反。
func TestDateRoleMatchesFormat(t *testing.T) {
	// ⚠️ 判据（正则）与数出来的数是**一体的**，见方法论 65：
	// 同一份文档，宽一点的正则会数出完全不同的量。所以这里把正则写在断言旁边，
	// 而不是把某个数字写进注释。
	compact := regexp.MustCompile(`交易日[^0-9\n]{0,4}(20[0-9]{2}-[0-9]{2}-[0-9]{2})`)
	dashed := regexp.MustCompile(`自然日[^0-9\n]{0,4}(20[0-9]{6})`)

	files, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "README.md")
	if len(files) < 5 {
		t.Fatalf("⚠️ 只扫到 %d 份文档 —— 太少，本条在空转", len(files))
	}
	okPairs, bad := 0, 0
	okCompact := regexp.MustCompile(`交易日[^0-9\n]{0,4}20[0-9]{6}`)
	okDashed := regexp.MustCompile(`自然日[^0-9\n]{0,4}20[0-9]{2}-[0-9]{2}-[0-9]{2}`)
	for _, f := range files {
		for i, line := range readLines(t, f) {
			for _, m := range compact.FindAllStringSubmatch(line, -1) {
				bad++
				t.Errorf("⚠️ %s:%d 写的是「交易日 %s」—— 交易日要用**无分隔符**格式（如 20260910）。"+
					"⚠️ 两者在夜盘之后会差一天，而差的那一天恰好是持仓性质翻转的那一天",
					f, i+1, m[1])
			}
			for _, m := range dashed.FindAllStringSubmatch(line, -1) {
				bad++
				t.Errorf("⚠️ %s:%d 写的是「自然日 %s」—— 自然日要用**带横杠**格式（如 2026-09-09）",
					f, i+1, m[1])
			}
			okPairs += len(okCompact.FindAllString(line, -1)) + len(okDashed.FindAllString(line, -1))
		}
	}
	// ⚠️ 反空转：一处都没配过角色词的话，上面两个正则永远不可能命中，
	// 而那时本条是恒绿的。
	if okPairs < 20 {
		t.Fatalf("⚠️ 全部文档里只找到 %d 处「角色词 + 日期」的搭配 —— "+
			"太少，本条很可能在空转（是不是约定的写法变了？）", okPairs)
	}
	t.Logf("角色词与格式一致 %d 处，错配 %d 处；⚠️ 光秃秃的日期（无角色词）本条**不查**",
		okPairs, bad)
}

// methodologyNumRe 认的是方法论条目的行首编号：`**80. ⚠️ …**`。
var methodologyNumRe = regexp.MustCompile(`(?m)^\*\*(\d+)\. `)

// TestMethodologyNumbersAreContiguous 钉住方法论编号**不重、不缺、从 1 起**。
//
// ⚠️ 这一条补的是仓库自己记过的一个洞：state.md 里写着某次编号错
// 「是我后来**核方法论编号时撞见的**，不是任何守卫抓到的」。
//
// 编号是这份文档的**引用地址** —— 代码注释里到处是「方法论第 28 条」。
// 重号之后两条内容抢同一个地址，而**被引用的那一方不会有任何变化**：
// 读的人跳过去，看见一条讲得通的条目，就停下了。
//
//	⚠️ 引错地址的失败模式，是**读到了另一条同样成立的话**。
//
// ⚠️ 而产生重号的动作极其平常：手工在末尾追加一条，编号自己敲。
// 本条就是在一次连加两条（80、81）之后立刻补的。
func TestMethodologyNumbersAreContiguous(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("docs", "silent-risks.md"))
	if err != nil {
		t.Fatal(err)
	}
	ms := methodologyNumRe.FindAllSubmatch(b, -1)
	// ⚠️ 判别力：正则一旦不匹配，下面每一条断言都在空集合上成立
	// （方法论 80）。条数下界让「正则写坏了」以红的形式出现。
	if len(ms) < 60 {
		t.Fatalf("⚠️ 只认出 %d 条方法论 —— 太少，正则八成不对，本条在空转", len(ms))
	}
	seen := map[int]int{}
	max := 0
	for _, m := range ms {
		n, err := strconv.Atoi(string(m[1]))
		if err != nil {
			t.Fatal(err)
		}
		seen[n]++
		if n > max {
			max = n
		}
	}
	for n, c := range seen {
		if c > 1 {
			t.Errorf("⚠️ 方法论第 %d 条**编号重复**（出现 %d 次）—— "+
				"代码注释里「方法论第 %d 条」从此指向两条内容，"+
				"而跳过去的人会读到其中一条、觉得讲得通，然后停下", n, c, n)
		}
	}
	var gaps []int
	for n := 1; n <= max; n++ {
		if seen[n] == 0 {
			gaps = append(gaps, n)
		}
	}
	if len(gaps) > 0 {
		t.Errorf("⚠️ 方法论**缺号** %v（最大 %d）—— "+
			"要么是删条目时没重排，要么是新加的那条敲错了数字", gaps, max)
	}
	t.Logf("方法论 %d 条，编号 1..%d 连续无重", len(ms), max)
}

// breakRefRe 认的是文档与注释里对破坏的引用：`破坏 265`。
var breakRefRe = regexp.MustCompile(`破坏 (\d+)`)

// retiredBreakNums 是**文字里提到、而清单里已经没有**的破坏编号，逐个写理由。
//
// ⚠️ 与 goneTests 同一条规矩：只收「讲历史的那种引用」，逐条写理由。
// 一个可以随手加名字的豁免表，比没有这张表更坏。
//
// ⚠️ 而且下面额外钉一条：**编号一旦回到清单里，本条要红** ——
// 否则这张表会烂在原地，替一个已经能解析的编号继续开着口子。
var retiredBreakNums = map[string]string{
	"1": "⚠️ 讲的是 20260910 重编号**之前**那件事：前缀 1 当时被 12 条共用。" +
		"state.md 那一段记的正是「我写下『零处歧义引用』，而加进歧义引用的是同一个提交」——" +
		"把它改写成新号 356 会把「当时它是歧义的」这件事本身抹掉",
	"4": "⚠️ 同上：前缀 4 当时被 11 条共用（新号 359）。" +
		"testnames_test.go 里那句「一句『破坏 4』指向十一条内容」是这条守卫存在的理由，" +
		"改写成一个唯一编号会让那句话变成废话",
}

// TestBreakRefsResolve 断言**文字里点名的每一个破坏编号都真的存在**。
//
// ⚠️ 它是被一次自己打脸逼出来的：我核完 `docs/` 与 `*.go`，写下
// 「仓库里零处歧义引用」，而**加进两处歧义引用的正是写下它的那个提交** ——
// 那两处在 `breaks.json` 自己的破坏名里。
//
//	我把清单当成「被描述的东西」，没当成「引用住的地方」。
//	而它两样都是：破坏名里既讲别的破坏，又被别人讲。
//
// ⇒ 所以本条**不挑地方**：`.md` / `.go` / `.json` 一律扫。
// 「核查的范围由我此刻想到的地方决定，而漏掉的那个地方不会举手」——
// 这条守卫就是用来替掉「此刻想到」的。
//
// ⚠️ 它防的是**悬空**，不是歧义。歧义那一半由
// TestBreakNumbersAreUniqueAndPresent 在清单侧保证编号唯一来防。
func TestBreakRefsResolve(t *testing.T) {
	breaksPath := filepath.Join("tools", "breakcheck", "breaks.json")
	raw, err := os.ReadFile(breaksPath)
	if err != nil {
		t.Fatal(err)
	}
	var breaks []struct {
		Name string `json:"name"`
		Why  string `json:"why"`
	}
	if err := json.Unmarshal(raw, &breaks); err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, b := range breaks {
		if m := regexp.MustCompile(`^(\d+) `).FindStringSubmatch(b.Name); m != nil {
			have[m[1]] = true
		}
	}
	if len(have) < 100 {
		t.Fatalf("⚠️ 只认出 %d 个破坏编号 —— 太少，本条在空转", len(have))
	}

	refs := map[string][]string{}
	err = filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		// ⚠️ 破坏清单**按字段扫**，不扫原文 —— 见下面那一段。
		if p == breaksPath {
			return nil
		}
		if !strings.HasSuffix(p, ".md") && !strings.HasSuffix(p, ".go") &&
			!strings.HasSuffix(p, ".json") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range breakRefRe.FindAllSubmatch(b, -1) {
			refs[string(m[1])] = append(refs[string(m[1])], p)
		}
		return nil
	})
	// ⚠️ **`old` / `new` 是补丁载荷，不是散文。**
	//
	// 一条破坏的 new 字段里出现一个「破坏 + 某个不存在的号」，
	// 那是**要写进别的文件的字节**，不是一句引用。
	// 按原文扫清单会把它当成悬空引用 ——
	// 而那条破坏的全部作用正是**制造**一处悬空引用去验本条。
	//
	// ⚠️ 顺带记一次现场：这段注释的第一版里**写出了那个号的数字**，
	// 于是 doc_test.go 自己成了一处悬空引用，本条当场把自己弄红。
	// **写关于引用的话，本身就造了一次引用** —— 与「讲/用」同一族。
	//
	//	⚠️ 于是本条会被「验它的那条破坏」自己弄红：每成功一次死一次（方法论 74）。
	//
	// ⇒ 清单只扫 name 与 why：那两个字段是写给人读的，其余是给机器用的。
	for _, b := range breaks {
		for _, text := range []string{b.Name, b.Why} {
			for _, m := range breakRefRe.FindAllStringSubmatch(text, -1) {
				refs[m[1]] = append(refs[m[1]], breaksPath+"（name/why）")
			}
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) < 10 {
		t.Fatalf("⚠️ 只认出 %d 个破坏引用 —— 太少，正则八成不对，本条在空转", len(refs))
	}

	for num, where := range refs {
		if have[num] {
			continue
		}
		if why, ok := retiredBreakNums[num]; ok {
			t.Logf("ⓘ 破坏 %s 已不在清单里，按 retiredBreakNums 放行：%s", num, why)
			continue
		}
		sort.Strings(where)
		t.Errorf("⚠️ 文字里点名的**破坏 %s 在清单里不存在**（%s）—— "+
			"多半是重编号或删掉了，而**改一个破坏的编号不会让任何文档变红**。"+
			"修法是把文字指到现在那条上；只有当那句话讲的是**历史**时，"+
			"才把它加进 retiredBreakNums 并写清理由",
			num, strings.Join(uniq(where), "、"))
	}
	// ⚠️ 豁免表要会自己过期：编号一旦回到清单，这条豁免就该拆掉。
	for num, why := range retiredBreakNums {
		if have[num] {
			t.Errorf("⚠️ retiredBreakNums 里的破坏 %s **又回到清单里了** —— "+
				"把它从表里删掉，让本条正常解析它。原来的理由：%s", num, why)
		}
	}
	t.Logf("文字里点名的破坏编号 %d 个，清单里有 %d 条，已退休的 %d 个",
		len(refs), len(have), len(retiredBreakNums))
}

// uniq 去重且保序，用来让报错里的文件列表不重复。
func uniq(ss []string) []string {
	seen := map[string]bool{}
	out := ss[:0:0]
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
