package futsim

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ⚠️ kq.Scrubbed 在**落盘那一刻**复查夹具，这条测试查的是**已经躺在仓库里**的文件。
//
// 两者查的不是同一个时刻。落盘时的复查挡不住：用旧版本代码生成的夹具、
// 手工编辑过的夹具、从别处拷进来的夹具。这些都会直接进 git，
// 而 git 里的东西一旦推上公开仓库就收不回来——重写历史也收不回，
// 旧 SHA 仍然能通过 API 取到。所以这一层要在**提交前**就红。

var (
	fixtureUUID = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	fixtureJWT  = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.`)
)

func fixturePaths(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "probes", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 一个「没有夹具时静默通过」的扫描器，在目录被改名或路径写错时也会通过。
	// 那正是它最该报警的时候。
	if len(paths) == 0 {
		t.Fatal("testdata/probes 下一个夹具都没有 —— 是真的没有，还是路径写错了？" +
			"两种情形下这条测试都会「通过」，所以这里必须失败")
	}
	return paths
}

// TestFixturesDesensitized 扫已提交的夹具，找不该出现的形状。
func TestFixturesDesensitized(t *testing.T) {
	for _, p := range fixturePaths(t) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if m := fixtureUUID.FindString(s); m != "" {
			t.Errorf("%s 含 UUID 形状的串（前 8 位 %s…）—— 账户标识可能没脱敏", p, m[:8])
		}
		if fixtureJWT.MatchString(s) {
			t.Errorf("%s 含 JWT 形状的串 —— 令牌可能没脱敏", p)
		}
	}
}

// TestFixturesNoFieldDrift 断言每份夹具的 unclassified 都是空的。
//
// unclassified 非空说明柜台给了白名单与丢弃表都没见过的键：
// 那要么是协议变了，要么是我漏判了一个字段。两种都得人来看，
// 不能等到某天有人拿这份夹具当基准时才发现。
func TestFixturesNoFieldDrift(t *testing.T) {
	for _, p := range fixturePaths(t) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Unclassified []string `json:"unclassified"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			t.Errorf("%s 不是合法 JSON：%v", p, err)
			continue
		}
		if len(f.Unclassified) > 0 {
			t.Errorf("%s 有 %d 个未分类字段：%v", p, len(f.Unclassified), f.Unclassified)
		}
	}
}

// ⚠️ 「怎么算一次凭据读取」——这条判据换过**四次**，前三次都错在同一件事上：
// 我在**文本**里找模式，而判据自己就写在文本里。
//
//	一跑  「文中出现 .env」        → 红在自己的注释上：说明「本层不读它」那句必然含它
//	二跑  「有没有真去读」          → 红在自己的模式列表上：存放调用形态的字符串就是那些形态
//	三跑  加行级豁免 `// 判据自身`  → 又红在讲这个标记的说明文字上
//	四跑  再加「纯注释行跳过」      → 还是红：测试样本里的**字符串字面量**长得像调用
//
// 前三次我都在**加豁免**，而豁免只会越加越宽，且每一条都要人记住。
// 第四次才看清：**病根不在漏了哪种豁免，在于判据的种类选错了。**
//
// 注释不调用任何东西，字符串字面量也不调用任何东西。能调用的只有**调用表达式**。
// 所以改用 go/parser 解析语法树，只看 `*ast.CallExpr`——
// 于是注释、字符串、说明文字、测试样本全部自动不在视野内，**一条豁免都不需要**。
//
// ⚠️ 一个需要不断打补丁的判据，通常不是补得不够，是问的问题不对。
func credentialReadsIn(t *testing.T, filename string, src any) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", filename, err)
	}
	var hits []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		name := pkg.Name + "." + sel.Sel.Name
		switch name {
		case "os.Getenv":
			// 读环境变量本身就是读凭据的入口，不看参数。
			hits = append(hits, fmt.Sprintf("%s:%d %s(...)",
				filename, fset.Position(call.Pos()).Line, name))
		case "os.ReadFile", "os.Open", "ioutil.ReadFile", "probe.LoadEnv":
			// ⚠️ 这几个函数本层**合法地**用着（读夹具、读自己的源码），
			// 所以只在参数指向凭据文件时才算。
			for _, a := range call.Args {
				lit, ok := a.(*ast.BasicLit)
				if ok && lit.Kind == token.STRING && strings.Contains(lit.Value, ".env") {
					hits = append(hits, fmt.Sprintf("%s:%d %s(%s)",
						filename, fset.Position(call.Pos()).Line, name, lit.Value))
				}
			}
		}
		return true
	})
	return hits
}

// TestNoStrayFixtureTrees 断言仓库里**只有一棵**夹具树。
//
// ⚠️ 起因：落盘目录配成了相对路径，从 cmd/oracle 里跑探针时，
// 夹具落在 cmd/oracle/testdata/probes 下，而日志里只写相对路径，
// 看不出它落在哪棵树上。那一份跟着 `git add -A` 进了仓库，
// 内容与主树的同名文件**不同**（采样时刻不同），而没有任何东西会说一句。
//
// 多出来的那棵树不会报错，只会安静地成为第二份「证据」——
// 将来有人拿它对拍，对的是一份来路不明的数。
func TestNoStrayFixtureTrees(t *testing.T) {
	// ⚠️ 已登记的夹具树。每加一棵都要写明**它装什么、为什么不能并进已有的那棵**。
	//
	// 这不是放宽，是把「多出来一棵树」从「默认可疑」改成「登记过的例外」——
	// 而登记这个动作本身要求人说出理由，那正是这条守卫的价值所在。
	known := map[string]string{
		"testdata/probes":  "柜台截面夹具：实测产出，脱敏后入库",
		"testdata/refdata": "上游规则数据：来自天勤合约字典，不是柜台实测，证据等级不同",
		// ⚠️ 为什么它不能并进 testdata/probes：
		// probes 是**天勤 DIFF** 的语料，而 conformance/fixture 的 loadAll 扫那个目录、
		// 把持仓当泛型 map 读。一份 CTP 夹具丢进去**不会报错** —— 它会若无其事地
		// 解析成功，然后带着一堆 CTP 字段名流进棘轮与覆盖率，而那些数字
		// **看起来完全正常**：语料变大了，覆盖变好了。坏消息伪装成好消息。
		"testdata/ctp": "CTP/SimNow 截面夹具：另一个口子的实测产出，字段名与 DIFF 无一相同",
		// ⚠️ 这一棵与上面三棵**性质相反**：那三棵是「进 git 的证据」，
		// 这一棵是「**刻意不进 git** 的证据」（使用者 20260909 裁决：
		// 柜台通知的文案要留下来，但不上 git）。
		// 它由 .gitignore 的 *.notify.json 按名挡着，守卫见
		// TestLocalOnlyEvidenceIsGitIgnored 与 TestCommittedFixturesCarryNoNotifyContent。
		// ⚠️ 登记它而不是让守卫跳过 gitignored 路径：跳过会让一棵**真的**杂散树
		// 只要恰好被忽略就再也看不见。
		"testdata/probes/local": "通知文案的本地旁档：**不进 git**，与其余三棵性质相反",
	}
	const canonical = "testdata/probes"
	var stray []string
	seenCanonical := 0
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".json" {
			return nil
		}
		dir := filepath.ToSlash(filepath.Dir(p))
		if dir == canonical {
			seenCanonical++
			return nil
		}
		if _, ok := known[dir]; ok {
			return nil
		}
		if !strings.Contains(dir, "testdata") {
			return nil
		}
		stray = append(stray, filepath.ToSlash(p))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 「没找到杂散文件」与「根本没在找」在结果上同形。
	// 所以要求这次遍历**确实走到过**规范目录：走到了才说明 walk 是活的。
	if seenCanonical == 0 {
		t.Fatalf("遍历没在 %s 下看到任何夹具 —— 是真的空了，还是 walk 的起点不对？"+
			"两种情形下本条都会「通过」，所以这里必须失败", canonical)
	}
	if len(stray) > 0 {
		t.Errorf("⚠️ 已登记的夹具树之外还有 %d 份数据：%v", len(stray), stray)
		t.Errorf("   已登记的是 %v", keysOf(known))
		t.Error("   要么落盘目录配成了相对路径随 cwd 另开了一棵树，" +
			"要么这是一棵新树 —— 后者请登记并写明它装什么、为什么不能并进已有的那棵")
	}
}

// TestFixtureScanDeclaresItsBlindSpot 把这层扫描**查不了**的东西钉成一条断言。
//
// ⚠️ 它查不了「夹具里有没有出现凭据文件里的密码」——CI 上没有那个文件，
// 有也不该让测试去读。所以这一层只能查**形状**，查不了**具体值**。
//
// 这条测试的作用是：谁要是哪天把凭据读进这一层，
// 「本层不读凭据」这条约束会立刻红，而不是悄悄多出一个凭据读取点。
func TestFixtureScanDeclaresItsBlindSpot(t *testing.T) {
	for _, hit := range credentialReadsIn(t, "fixtures_test.go", nil) {
		t.Errorf("⚠️ %s —— 本层出现凭据读取。具体值的复查在 kq.Scrubbed（落盘时）做，"+
			"两层用不同原理才不会一起失效；本层只查形状", hit)
	}
}

// TestCredentialReadDetectorDiscriminates 双向钉住上面那个探测器。
//
// ⚠️ 样本是**合成源码**，解析成独立的文件，所以它们既不会被当成本文件的一部分，
// 也不需要任何豁免。这正是换成语法树之后省下的那一整套机制。
func TestCredentialReadDetectorDiscriminates(t *testing.T) {
	// 只是**提到**：注释、字符串字面量、变量名。一次调用都没有。
	mentions := `package p

import "os"

// 本层不读 .env，也不调用 os.Getenv(...)。
var patterns = []string{` + "`os.Getenv(`, `os.ReadFile(\".env`" + `}

func f() {
	_ = patterns
	_, _ = os.ReadFile("fixtures_test.go") // 读自己的源码是合法的
}
`
	// 真的读：环境变量、凭据文件。
	reads := `package p

import "os"

func f() {
	_ = os.Getenv("KQ_PASSWORD")
	_, _ = os.ReadFile(".env")
	_, _ = os.Open(".env")
}
`
	if hits := credentialReadsIn(t, "mentions.go", mentions); len(hits) != 0 {
		t.Errorf("⚠️ 探测器把 %d 处**提及**当成了读取：%v —— "+
			"它会红在说明文字和测试样本上，然后被人删掉", len(hits), hits)
	}
	hits := credentialReadsIn(t, "reads.go", reads)
	if len(hits) != 3 {
		t.Errorf("⚠️ 三处真实的凭据读取只逮到 %d 处：%v —— 漏掉的那些它什么都挡不住",
			len(hits), hits)
	}
}

// TestFixtureNameMatchesTradingDay 断言夹具文件名里的日期就是它内部的 `trading_day`。
//
// ⚠️ 这条守的是一个此前**存在但从未写下来**的约定：
// 夹具名用的是**交易日**，不是自然日。20 份夹具全都遵守它，靠的是我记得。
//
// 为什么它今晚会撞车：交易日 20260908 的夜盘物理上发生在**自然日 09-07 晚**，
// 而今晚（自然日 09-08）的夜盘属于**交易日 20260909**。
// 「2026-09-08 夜盘」这个标签因此同时指向两场不同的实验，
// 而**没有任何字段能把它们分开**。
//
// ⚠️ 靠记得「这是交易日不是自然日」是**条目层**的解法；
// 把它钉在文件名与内容的一致性上是**动作层**的解法。
// 本仓库这一周已经因为这个复合标签制造过两次真实的日期错误
// （实现方与评审方各一次），所以这里不用条目，用守卫。
func TestFixtureNameMatchesTradingDay(t *testing.T) {
	dateInName := regexp.MustCompile(`-(\d{8})(?:-\d+)?\.json$`)
	checked := 0
	for _, p := range fixturePaths(t) {
		m := dateInName.FindStringSubmatch(filepath.ToSlash(p))
		if m == nil {
			t.Errorf("⚠️ 夹具 %s 的文件名里没有 8 位日期 —— 命名约定被破坏了", p)
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			TradingDay string `json:"trading_day"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			t.Errorf("%s 不是合法 JSON：%v", p, err)
			continue
		}
		if f.TradingDay == "" {
			t.Errorf("⚠️ %s 里没有 trading_day —— 那这份证据属于哪一天无从判断", p)
			continue
		}
		if m[1] != f.TradingDay {
			t.Errorf("⚠️ %s 的文件名日期是 %s，而内部 trading_day 是 %s —— "+
				"夹具名一律用**交易日**；若这里用了自然日，两场不同的实验会共用一个标签",
				p, m[1], f.TradingDay)
		}
		checked++
	}
	if checked < 15 {
		t.Fatalf("只核到 %d 份夹具 —— 太少，本条可能在空转", checked)
	}
}

// TestSessionLabelsAreQualified 断言文档里凡是用 `YYYY-MM-DD` 给一场盘命名的，
// **同一行**必须写明它是交易日还是自然日。
//
// ⚠️ 判据刻意窄：只卡「日期 + 夜盘/日盘」这一种搭配，因为歧义正是从这里来的。
// 一个宽到会误报的检查会被关掉——那是 restatement-count.sh 注释里写过的死法。
func TestSessionLabelsAreQualified(t *testing.T) {
	// ⚠️ 判据不是「这一行提没提交易日」——第一版是那么写的，
	// 于是 `probes.md` 那行「2026-09-08 夜盘（…，交易日 20260908）」被放过了：
	// 限定词「交易日」贴在**另一个日期**上，而带横线那个仍然是无限定的自然日格式。
	//
	// 正确的判据是位置性的：**带横线的 YYYY-MM-DD 紧接「夜盘/日盘」时，
	// 它前面必须直接写着「自然日」。** 交易日一律用紧凑形式（`交易日 20260908`），
	// 两种格式因此在字面上就分得开，不靠读的人记得。
	// ⚠️ 连接部分是 `[^0-9]{0,4}`，不是 `\s*`。
	//
	// 第一版写的是 `\s*`，把注释里说的「紧接」操作化成了**只允许空白**。
	// 于是语料里真实存在的 `2026-09-08 的夜盘`（中间一个「的」）**整行隐身**——
	// 而且是**两个方向都隐身**：带限定词时不匹配（结果对、理由错），
	// 去掉限定词时也不匹配（结果就错了）。实测把那行的「自然日 」删掉，守卫全绿。
	//
	// ⚠️ 更要紧的是它是怎么被漏掉的：**合成样本继承了写它的人的措辞习惯。**
	// 我脑子里的形状是「日期 + 空格 + 夜盘」，于是正则写成 `\s*`、
	// 样本也全是那个形状——**样本与判据同源，所以样本永远盖不住判据的盲区。**
	// 真实语料是唯一能提供「你想不到的写法」的来源。
	// 下面的样本因此**从真实漏网的那一句派生**（用「的」），不是再造一个。
	label := regexp.MustCompile(`(自然日[^0-9]{0,4})?\d{4}-\d{2}-\d{2}[^0-9]{0,4}(夜盘|日盘)`)
	files, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "README.md")
	if len(files) < 5 {
		t.Fatalf("只找到 %d 份文档 —— 本条可能在空转", len(files))
	}
	// ⚠️ 引号与反引号里的日期是**在讲这个形式**，不是在用它。
	//
	// ⚠️ **这是本仓库第 1 份「讲/用」判据**（句法层：剥掉引号内的片段）。
	// 另两份：kqref_test.go 用**邻近关键词**（前后两行出现「推翻/接续」），
	// pkgdoc_test.go 用**作用域**（只扫包注释）。**三处不共享判定。**
	//
	// ⚠️ 这与「共享数据可以，共享判定不行」不冲突，但要分清：那条是为
	// **互相校验的双实现**立的（两份必须独立，分歧才有信息）；而这三处
	// 不是互校的双实现，是**同一个概念的三个消费者** —— 对消费者套那条规则，
	// 得到的是三份会各自漂移的定义，且没有任何机制会发现漂移
	// （评审方 20260909 指出）。不合并是当下的选择，写下这段是让漂移**可见**。
	//
	// 本处的边界两侧都钉了，见下面合成用例里那两条带引号的。
	//
	// 判据第一版没分这两者，于是它红在了自己的说明文字上——
	// 那段正是要引用坏形式当反例的。这是本仓库第九次同一个形状，
	// 而解法早就有了：**讲和用必须分开**（silent-risks.md 第 17 条）。
	// 这里不加豁免标记，直接把「被引用的片段」从扫描视野里去掉。
	quoted := regexp.MustCompile("「[^」]*」|`[^`]*`|“[^”]*”")
	hits := 0
	for _, f := range files {
		for i, l := range readLines(t, f) {
			l = quoted.ReplaceAllString(l, "")
			if !label.MatchString(l) {
				continue
			}
			for _, m := range label.FindAllStringSubmatch(l, -1) {
				hits++
				if m[1] == "" {
					t.Errorf("⚠️ %s:%d 用带横线的日期直接命名一场盘（%q），"+
						"而没有在它前面写「自然日」。交易日请用紧凑形式（交易日 20260908），"+
						"两者在字面上就该分得开：%q",
						f, i+1, strings.TrimSpace(m[0]), strings.TrimSpace(l))
				}
			}
		}
	}
	t.Logf("扫到 %d 处「日期 + 盘」的搭配", hits)

	// ⚠️ 上面那个数今天是 0：全库现在**没有一行**匹配这个搭配。
	// 于是「自然日 + 日期 + 夜盘」这条**合法路径一次也没被走过**——
	// 判据在真实文档上只证明了「不误报」，没证明「认得出合法写法」。
	// 用合成样本把两个方向都走一遍。
	for _, c := range []struct {
		line string
		ok   bool
	}{
		{"自然日 2026-09-07 夜盘的记录", true},
		{"于 自然日 2026-09-07 日盘 建仓", true},
		{"2026-09-07 夜盘的记录", false},
		// ⚠️ 下面两条派生自 probes.md:622 —— 判据第一版对它们**两个方向都看不见**。
		{"自然日 2026-09-08 的夜盘 → 属于交易日 20260909", true},
		{"2026-09-08 的夜盘 → 属于交易日 20260909", false},
		{"2026-09-08，当晚夜盘", false},
		{"交易日 20260908 的夜盘", true},      // 紧凑形式压根不匹配，视为合法
		{"见「2026-09-07 夜盘」这个坏例子", true}, // 引号里是**讲**，不是**用**
		{"见 `2026-09-07 夜盘` 这个坏例子", true},
	} {
		stripped := quoted.ReplaceAllString(c.line, "")
		bad := false
		for _, m := range label.FindAllStringSubmatch(stripped, -1) {
			if m[1] == "" {
				bad = true
			}
		}
		if bad == c.ok {
			t.Errorf("⚠️ 判据对 %q 的判断反了：期望%s，实际%s", c.line,
				map[bool]string{true: "放行", false: "拦下"}[c.ok],
				map[bool]string{true: "拦下", false: "放行"}[bad])
		}
	}
}

// TestCommittedFixturesStillExist 断言**提交过的**夹具没有被删掉。
//
// ⚠️ 这一条换过一次判据，两次都不是「写错了」，是问题问得不对：
//
//	一版  下界 `>= 15`（实际 20 份）   → 删 2 份仍全绿，对「删除」毫无判别力
//	二版  确切数，登记在 state.md      → 有牙齿，但**新增也会红**
//	                                     而探针整天在产出新证据，
//	                                     于是测试在两次提交之间长期是红的 ——
//	                                     **一个长期红的守卫会被关掉**，
//	                                     这正是我在 settle-watch 注释里写过的那种死法
//
// 三版问的是真正该问的那个问题：**提交过的夹具还在不在。**
// 新增零摩擦（未提交的新文件不在名单里），删除全牙齿（名单来自 git，不靠人维护）。
//
// ⚠️ git 不可用时**失败而不是跳过**：本仓库的文档类守卫本来就读工作树，
// 非 git 检出不是本项目支持的状态。一个「找不到就跳过」的检查，
// 在它最该报警的时候也会通过。
func TestCommittedFixturesStillExist(t *testing.T) {
	touchGitState(t) // ⚠️ 见它的注释：不读一遍 git 状态，这条测试会被缓存端出旧判决
	out, err := exec.Command("git", "ls-files", "testdata/probes").Output()
	if err != nil {
		t.Fatalf("跑不了 git ls-files：%v —— 本条靠 git 提供「提交过哪些夹具」的名单，"+
			"拿不到名单就判不出删除，所以这里失败而不是跳过", err)
	}
	var committed []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasSuffix(line, ".json") {
			committed = append(committed, line)
		}
	}
	// ⚠️ 迭代次数下界：名单为空时下面的循环空转，本条会「通过」。
	if len(committed) < 15 {
		t.Fatalf("git 只列出 %d 份已提交夹具 —— 是真的这么少，还是路径写错了？"+
			"两种情形下本条都会「通过」，所以这里必须失败", len(committed))
	}
	missing := 0
	for _, p := range committed {
		if _, err := os.Stat(filepath.FromSlash(p)); err != nil {
			t.Errorf("⚠️ 已提交的夹具 %s 在工作树里不见了 —— "+
				"夹具只因**被证明是错的**而删（probes.md §7.4）；"+
				"判别力是随样本组合涨的，而删除不可逆", p)
			missing++
		}
	}
	t.Logf("已提交夹具 %d 份，缺失 %d 份", len(committed), missing)
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
