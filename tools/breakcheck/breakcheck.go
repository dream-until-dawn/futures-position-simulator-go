// Command breakcheck 是**破坏验证**的可复现跑法。
//
// 规则三说「永远绿的测试等于没有测试」。破坏验证是检查这件事的手段：
// 把生产代码改坏一处，看对应的测试红不红、红得对不对。
//
// ⚠️ 但此前它只活在会话里：脚本在临时目录，结论在提交信息。
// 也就是**「这些测试有牙」这句话本身不可复现** —— 而那正是规则三要防的形状，
// 只是换到了元层面。本命令把它落进仓库。
//
//	go run ./tools/breakcheck            # 全跑
//	go run ./tools/breakcheck -only 无值  # 只跑名字含「无值」的
//	go run ./tools/breakcheck -list      # 只列，不跑
//
// # 四层，本命令覆盖前三层
//
//	零层  先确认**破坏本身发生了** —— 锚点必须恰好出现一次，否则报「零层未成立」
//	一层  要红
//	二层  要红在**断言**上，不是编译失败
//	三层  要红在**被测的那个性质**上 —— 靠 want 片段核对
//
// ⚠️ 第四层（红的理由与破坏的因果）机器判不了，靠 want 逼近，靠人看。
//
// # ⚠️ 写破坏时最容易踩的一个坑：把变量或导入一起改没了
//
// 2026-09-09 一晚踩了五次。改坏一处代码时顺手删掉了对某个变量或导入的
// **唯一一次使用**，于是 Go 报「declared and not used」/「imported and not used」——
// 红的是编译器，不是断言，第二层不成立。
//
// 典型的几种改法与对策：
//
//	把 if 条件改成 false          →  改成 `x != nil || true` 之类，保留对 x 的使用
//	删掉整个分支                  →  留一句 `_ = x`，或只改分支体不改条件
//	把唯一用到某导入的调用去掉    →  换一个仍然用到它的等价改法
//
// ⚠️ 它们都不是「破坏没写好」这么简单：一个编译不过的破坏，
// 与一个**测试确实没抓住**的破坏，在汇总行里都显示为红。
// 第二层存在的全部理由就是把这两者分开。
//
// # ⚠️ 期望「仍然绿」的破坏
//
// 有一类破坏**期望它不红**：它演示的是一处**盲区**——
// 这个错在当前的口子/样本上查不出来。
// 一条声称的盲区，只有被这样演示过一次，才算真的确认它存在。
//
// 所以 Expect 为 green 时 Why **必填**：不写清楚它在演示哪条盲区，
// 一条「期望绿」的破坏与一条坏掉的检查完全一样。
//
// ⚠️ 而这类条目哪天**红了**是个好消息（盲区消失了），
// 好消息同样需要有人被通知到 —— 所以它照样判为「未按预期」。
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

//go:embed breaks.json
var registryJSON []byte

// Break 是一次破坏。
type Break struct {
	Name string `json:"name"`
	// Dir 是跑 go test 的工作目录，相对仓库根；空表示根。
	//
	// ⚠️ 它不是可选的方便字段：cmd/oracle 是**嵌套模块**，
	// 在根目录跑 `go test ./cmd/oracle/probe/` 根本找不到那个包。
	// 少了它，那 13 条破坏会以「包不存在」的形式失败，
	// 而那个失败与「测试没红」长得不一样，却同样不说明任何事。
	Dir  string `json:"dir"`
	File string `json:"file"`
	// Old 是锚点。⚠️ 必须在文件里**恰好出现一次**：
	// 零次说明代码变了而这条没跟着改，多次说明改了不止一处 ——
	// 两种情形下「破坏本身」都没按预期发生，那是零层。
	Old string `json:"old"`
	New string `json:"new"`
	// Also 是**同一次破坏里要一起改的其余文件**。
	//
	// ⚠️ 它补的是一个被如实记过的洞：有些破坏**天然是多文件的**，
	// 单文件工具表达不出来，于是那条守卫只能验一半。
	// 典型是「主模块偷偷多一个第三方依赖」——
	//
	//	只加 import   go.mod 里没有 require → 编译失败，红的是编译器不是断言
	//	只加 require  没有 import → 依赖不进 go list -deps，什么都不会变
	//
	// 两个都改才是真场景。⚠️ 每一个 Also 文件同样走「锚点恰好一次」
	// 与「逐字节还原」两道检查 —— 多文件不是放宽，是把同样的严格铺开。
	Also []AlsoEdit `json:"also,omitempty"`
	Pkg  string     `json:"pkg"`
	Test string     `json:"test"`
	// Want 是期望在测试输出里出现的片段 —— 第三层：红在被测的那个性质上。
	Want string `json:"want"`
	// Expect 是 "red" 或 "green"。
	Expect string `json:"expect"`
	// Why 在 Expect 为 green 时**必填**：它在演示哪条盲区。
	Why string `json:"why,omitempty"`
}

func main() {
	only := flag.String("only", "", "只跑名字含这个子串的")
	list := flag.Bool("list", false, "只列出，不跑")
	flag.Parse()

	var breaks []Break
	if err := json.Unmarshal(registryJSON, &breaks); err != nil {
		fmt.Fprintf(os.Stderr, "读破坏清单失败：%v\n", err)
		os.Exit(2)
	}
	if err := validate(breaks); err != nil {
		fmt.Fprintf(os.Stderr, "破坏清单本身不合法：%v\n", err)
		os.Exit(2)
	}
	if *list {
		for _, b := range breaks {
			mark := " "
			if b.Expect == "green" {
				mark = "◦"
			}
			fmt.Printf("%s %-46s %s :: %s\n", mark, b.Name, b.Pkg, b.Test)
		}
		fmt.Printf("\n共 %d 条（◦ 表示**期望仍然绿**，那是盲区演示）\n", len(breaks))
		return
	}

	// ⚠️ 工作树必须干净。
	//
	// 本命令会**改生产代码再改回来**。若中途被杀，改坏的那份会留在盘上，
	// 而干净的工作树让 `git checkout` 一句话就能收拾。
	// 工作树本来就脏的话，收拾时分不清哪些改动是自己的。
	var before string
	if dirty, err := gitDirty(); err != nil {
		fmt.Fprintf(os.Stderr, "查工作树状态失败：%v\n", err)
		os.Exit(2)
	} else if hasTrackedChanges(dirty) {
		fmt.Fprintf(os.Stderr,
			"⚠️ 工作树里有**已跟踪文件**的改动，拒绝运行 ——\n"+
				"   本命令会改生产代码再改回来，中途被杀时干净的工作树\n"+
				"   让 git checkout 一句话就能收拾：\n%s\n", dirty)
		os.Exit(2)
	} else {
		// ⚠️ 未跟踪的新文件**不拦**：夹具与落盘产物随时会冒出来，
		// 拦它等于要求跑破坏验证之前先清空 testdata ——
		// 那是个跟本命令无关的要求，而无关的要求会让人绕过整个工具。
		before = dirty
	}

	bad := 0
	ran := 0
	for _, b := range breaks {
		if *only != "" && !strings.Contains(b.Name, *only) {
			continue
		}
		ran++
		verdict, detail := run(b)
		fmt.Printf("%-46s %s\n", b.Name, verdict)
		if detail != "" {
			fmt.Printf("    %s\n", strings.ReplaceAll(detail, "\n", "\n    "))
		}
		if verdict != "红对了" && verdict != "如预期仍然绿" {
			bad++
		}
	}
	// ⚠️ 跑完再查一次工作树，但与**运行前的基线**比，不是要求绝对干净。
	//
	// 首版要求绝对干净，结果一次后台拉取在运行期间写出了一个新文件，
	// 于是它报「有破坏没还原」—— 而真相是没有。
	// **一个会因为无关原因报警的守卫，会训练人忽略它**，
	// 而这个守卫报的恰恰是最不能忽略的那件事。
	//
	// 逐文件的还原比对在 run() 的 defer 里做，那才是直接判据；
	// 这里只兜「清单里的文件跑完还脏着」这种意外。
	//
	// ⚠️ 20260909 评审方撞到一处**说错了原因的诊断**：并发编辑
	// docs/roadmap.md 时，这里确信地报「说明有破坏没还原」——
	// 而清单里打在那个文件上的破坏**有 0 条**，那个改动不可能来自任何一条破坏。
	//
	// ⚠️ 更该记的是：**上面那段注释早就写着**「只兜改了一个不在清单里的
	// 已跟踪文件这种意外」，而代码从头到尾没有区分过清单内外 ——
	// 上一次加并发豁免时，**注释跟着改了，代码没跟着改**。
	// 注释说对了规则，代码做的是另一件事，而两者在文件里挨着。
	//
	// ⚠️ 它建议的 `git checkout -- <文件>` 在并发场景下会**直接删掉
	// 别人正在写的东西**。现在只对清单内的文件这么建议。
	if after, err := gitDirty(); err == nil && hasTrackedChanges(after) {
		mine, foreign, failed := dirtyVerdict(after, breakFileSet(breaks))
		if failed {
			fmt.Fprintf(os.Stderr,
				"\n⚠️⚠️ 跑完之后**清单里的文件**仍有改动，说明有破坏没还原：\n%s\n"+
					"   立刻 git checkout -- <那些文件>\n", mine)
			bad++
		}
		if foreign != "" {
			// ⚠️ 集外的一律**不**报成「有破坏没还原」，也**不**建议 checkout。
			// 破坏改不到清单外的文件，所以那种改动只可能来自别的进程 ——
			// 而对一份来源不明的改动建议 `git checkout --`，
			// 是在建议**删掉别人正在写的东西**。
			fmt.Fprintf(os.Stderr,
				"\nⓘ 跑完之后有**清单之外**的已跟踪文件被改动"+
					"（运行期间别的进程写的，与破坏无关；⚠️ 不要 checkout）：\n%s\n", foreign)
		}
	} else if err == nil && after != before {
		fmt.Fprintf(os.Stderr,
			"\nⓘ 工作树多了未跟踪文件（运行期间别的进程写的，与破坏无关）：\n"+
				"  运行前 %q\n  运行后 %q\n", before, after)
	}
	fmt.Printf("\n跑了 %d 条，未按预期 %d 条\n", ran, bad)
	if bad > 0 {
		os.Exit(1)
	}
}

func validate(bs []Break) error {
	if len(bs) == 0 {
		return fmt.Errorf("清单是空的")
	}
	seen := map[string]bool{}
	greens := 0
	for i, b := range bs {
		if b.Name == "" || b.File == "" || b.Old == "" || b.Pkg == "" || b.Test == "" {
			return fmt.Errorf("第 %d 条缺必填字段", i+1)
		}
		if seen[b.Name] {
			return fmt.Errorf("破坏名 %q 重复 —— 报告里会分不清哪条是哪条", b.Name)
		}
		seen[b.Name] = true
		switch b.Expect {
		case "red":
			if b.Want == "" {
				return fmt.Errorf("%q 期望红，却没写 want —— "+
					"那样只能验到「红了」，验不到「红在被测的那个性质上」", b.Name)
			}
		case "green":
			// ⚠️ 期望绿而不说明演示的是哪条盲区，
			// 与一条坏掉的检查完全一样。
			if b.Why == "" {
				return fmt.Errorf("%q 期望**仍然绿**，却没写 why —— "+
					"一条不说明自己在演示哪条盲区的「期望绿」，"+
					"与一条坏掉的检查完全一样", b.Name)
			}
			greens++
		default:
			return fmt.Errorf("%q 的 expect 是 %q，只能是 red 或 green", b.Name, b.Expect)
		}
	}
	// ⚠️ 至少要有一条期望绿的：那一类是盲区的证据，
	// 而一份只有「期望红」的清单，说明没人去演示过盲区。
	if greens == 0 {
		return fmt.Errorf("清单里一条「期望仍然绿」都没有 —— " +
			"那一类是盲区的证据；没有它，所有关于盲区的说法都只是一句话")
	}
	return nil
}

// AlsoEdit 是一次破坏里**同一批**的另一处改动。
type AlsoEdit struct {
	File string `json:"file"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

func run(b Break) (verdict, detail string) {
	// ⚠️ File 一律相对**仓库根**，而 go test 在 Dir 里跑。
	// 两者的基准不同是刻意的：破坏改的是源码（按仓库定位），
	// 测试跑的是模块（按模块定位）。混成一个会在嵌套模块上错。
	edits := append([]AlsoEdit{{File: b.File, Old: b.Old, New: b.New}}, b.Also...)

	// ⚠️ **先全部读、全部校验锚点，再动手写。**
	//
	// 一边写一边校验的话，第二个文件的锚点对不上时，第一个文件已经被改坏了 ——
	// 那时要么靠 defer 还原（而还原路径此刻还没建立），要么留下一个改了一半的
	// 工作树。多文件破坏里「改了一半」是最坏的状态：它既不是破坏也不是原状。
	type staged struct {
		file   string
		orig   []byte
		broken []byte
	}
	var plan []staged
	for _, e := range edits {
		orig, err := os.ReadFile(e.File)
		if err != nil {
			return "零层未成立", fmt.Sprintf("读不到 %s：%v", e.File, err)
		}
		// ⚠️ 锚点在清单里一律写 LF，而**本仓库的换行是混的**：
		// git 签出的文件是 CRLF，后来新建的是 LF。裸字节比对会让
		// CRLF 文件上的每一条多行锚点都匹配不上 —— 而那报出来是「零层未成立」，
		// 看起来像锚点写错了。实测踩过一次，查了很久才想到是换行。
		old, broken := adaptEOL(string(orig), e.Old), adaptEOL(string(orig), e.New)
		if n := strings.Count(string(orig), old); n != 1 {
			hint := ""
			if old != e.Old {
				hint = "（该文件通篇 CRLF，锚点已按它转换过再找）"
			}
			return "零层未成立", fmt.Sprintf(
				"锚点在 %s 里出现 %d 次（要恰好 1 次）%s—— **破坏本身没发生**，"+
					"下面无论红绿都不说明任何事", e.File, n, hint)
		}
		plan = append(plan, staged{e.File, orig,
			[]byte(strings.Replace(string(orig), old, broken, 1))})
	}

	// ⚠️ defer **先装好再写**：装在写之后的话，第二个文件写失败时
	// 第一个文件没有任何东西负责还原它。
	defer func() {
		for _, s := range plan {
			if err := os.WriteFile(s.file, s.orig, 0o644); err != nil {
				verdict = "⚠️ 还原失败"
				detail = fmt.Sprintf("%s 没还原回去：%v —— 立刻 git checkout", s.file, err)
				return
			}
			// ⚠️ 写回去了不等于还原了 —— 读回来逐字节比一次。
			//
			// 这一条比看起来重要：还原失败会让**后面每一条破坏**都跑在
			// 一份被改坏的代码上，而它们的红绿从此不说明任何事。
			back, rerr := os.ReadFile(s.file)
			if rerr != nil || string(back) != string(s.orig) {
				verdict = "⚠️ 还原失败"
				detail = fmt.Sprintf("%s 写回去了但内容对不上（%v）—— "+
					"⚠️ 后面每一条破坏都会跑在被改坏的代码上，立刻 git checkout",
					s.file, rerr)
				return
			}
		}
	}()
	for _, s := range plan {
		if err := os.WriteFile(s.file, s.broken, 0o644); err != nil {
			return "零层未成立", err.Error()
		}
	}

	cmd := exec.Command("go", "test", b.Pkg, "-run", "^"+b.Test+"$", "-v")
	cmd.Dir = b.Dir // 空串表示当前目录
	out, runErr := cmd.CombinedOutput()
	text := string(out)
	green := runErr == nil

	if b.Expect == "green" {
		if green {
			return "如预期仍然绿", "盲区：" + b.Why
		}
		return "⚠️ 竟然红了", "盲区消失了？那是好消息，但清单要跟着改。\n" +
			"原以为的盲区：" + b.Why + "\n" + tail(text, 500)
	}
	if green {
		return "仍然绿", "破坏后照样通过 —— 这条测试没在测它声称测的东西"
	}
	if strings.Contains(text, "build failed") {
		return "红错了理由", "编译失败，不是断言失败（第二层不成立）：\n" + tail(text, 500)
	}
	if !strings.Contains(text, b.Want) {
		return "红错了理由", fmt.Sprintf(
			"断言失败了，但输出里没有 %q（第三层不成立）：\n%s", b.Want, tail(text, 600))
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, b.Want) {
			return "红对了", strings.TrimSpace(line)
		}
	}
	return "红对了", ""
}

func gitDirty() (string, error) {
	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// hasTrackedChanges 报告 porcelain 输出里有没有**已跟踪文件**的改动。
//
// ⚠️ 只有它们才需要拦：未跟踪的新文件（夹具、落盘产物）与本命令无关，
// 拦它等于要求跑破坏验证之前先清空 testdata，
// 而一个提出无关要求的工具，会被人整个绕过去。
func hasTrackedChanges(porcelain string) bool {
	for _, line := range strings.Split(porcelain, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if !strings.HasPrefix(line, "??") {
			return true
		}
	}
	return false
}

// adaptEOL 把锚点的换行改成**目标文件实际用的**那种。
//
// ⚠️ 只在文件通篇是 CRLF 时才转。换行混着的文件一律不猜 ——
// 那时转与不转都可能匹配不上，而上面那条「恰好 1 次」会把它拦下来。
// 拦下来比蒙对一半强：蒙对的那一半会让另一半静默失效，
// 而「静默失效的破坏」正是这个工具存在的理由。
//
// ⚠️ 还原走的是没动过的 orig 字节，所以这里的转换不影响逐字节还原那一条。
func adaptEOL(content, anchor string) string {
	if anchor == "" || strings.Contains(anchor, "\r\n") {
		return anchor
	}
	crlf := strings.Count(content, "\r\n")
	if crlf == 0 || crlf != strings.Count(content, "\n") {
		return anchor // 全 LF，或换行是混的
	}
	return strings.ReplaceAll(anchor, "\n", "\r\n")
}

// breakFileSet 是破坏清单**可能改到**的全部文件。
//
// ⚠️ 收尾诊断靠它区分「清单内没还原」与「清单外的并发改动」——
// 破坏改不到清单外的文件，所以集外的改动只可能来自别的进程。
func breakFileSet(bs []Break) map[string]bool {
	out := map[string]bool{}
	for _, b := range bs {
		// ⚠️ File 一律相对**仓库根**（见 run 的注释），与 porcelain 同基准，不拼 Dir。
		if b.File != "" {
			out[b.File] = true
		}
		for _, a := range b.Also {
			if a.File != "" {
				out[a.File] = true
			}
		}
	}
	return out
}

// splitByBreakFiles 把 porcelain 的已跟踪改动按「在不在清单里」分开。
func splitByBreakFiles(porcelain string, set map[string]bool) (mine, foreign string) {
	var m, f []string
	for _, line := range strings.Split(porcelain, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "??") {
			continue
		}
		// porcelain 是 `XY path`，路径从第 4 个字符起。
		path := strings.TrimSpace(line)
		if len(line) > 3 {
			path = strings.TrimSpace(line[3:])
		}
		path = strings.ReplaceAll(strings.Trim(path, "\""), "\\", "/")
		if set[path] {
			m = append(m, line)
		} else {
			f = append(f, line)
		}
	}
	return strings.Join(m, "\n"), strings.Join(f, "\n")
}

// dirtyVerdict 判断「跑完之后树是脏的」算不算**未按预期**。
//
// ⚠️ 单独抽成纯函数，是为了让「**清单外的改动不计入那个数**」这件事可测。
// 那个数（「未按预期 N 条」）是这套体系里被引用最多的一句话，
// 而它此前**可以被任何一次并发写入污染成失败** ——
// 打印一句说错原因的话是一回事，把头条数字改掉是另一回事。
//
// 20260909 评审方那次「165 条，未按预期 1 条」，逐条筛过之后
// 没有任何一条破坏落在预期之外：**整个那个 1 就是这一行加出来的**。
func dirtyVerdict(after string, set map[string]bool) (mine, foreign string, failed bool) {
	mine, foreign = splitByBreakFiles(after, set)
	return mine, foreign, mine != ""
}
