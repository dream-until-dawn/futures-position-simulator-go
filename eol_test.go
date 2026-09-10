package futsim

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestNoFileMixesLineEndings 断言**没有哪个文件内部混着 CRLF 与 LF**。
//
// ⚠️ 仓库整体换行是混的（git 签出的是 CRLF，本机新建的是 LF），
// 这条**不管**那个 —— 跨文件的不一致无害，git 自己会归一。
// 它管的是**同一个文件内部**混，而那个有害，且害在一个很不显眼的地方：
//
// `tools/breakcheck` 是裸字节匹配。锚点在清单里一律写 LF，
// 它按目标文件的换行转换后再找 —— 但文件内部混着的时候它**刻意不猜**，
// 于是那条破坏报「零层未成立」。也就是说：**一条破坏悄悄不再跑了**，
// 而汇总行里它与跑过的长得一模一样。
//
// ⚠️ 这在 2026-09-08 一天之内咬了两次，第二次是往一个 CRLF 文件里
// 插入 LF 的新行造成的 —— 那是编辑工具的默认行为，不会有任何提示。
// 所以判据必须落在文件本身，不能靠人记得。
func TestNoFileMixesLineEndings(t *testing.T) {
	scanned, mixed, stray, err := scanLineEndings(".")
	if err != nil {
		t.Fatal(err)
	}
	reportLineEndings(t, scanned, mixed, stray, 50)
}

// scanLineEndings 扫一棵目录树的换行形态。
//
// ⚠️ root 是参数而不是写死的 "."，是为了让 TestStrayCRIsActuallyCaught
// 能在临时目录里造一个真的坏文件来验它 —— 仓库自己是干净的，
// 而**一条只在干净语料上跑过的检查，没有人见过它红**。
func scanLineEndings(root string) (scanned int, mixed, stray []string, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// ⚠️ .git 里有 git 自己的字节，不归我们管。
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".go", ".json", ".md", ".sh", ".yml", ".yaml":
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		scanned++
		crlf := bytes.Count(b, []byte("\r\n"))
		lf := bytes.Count(b, []byte("\n"))
		if crlf > 0 && crlf != lf {
			mixed = append(mixed, p)
		}
		// ⚠️ 上一条对**多余的回车符**是瞎的：CR CR LF 里每个换行前面都有回车，
		// 于是 crlf == lf 照样成立，文件就这么过去了。
		//
		// 20260909 真的发生了一次：一句「把 LF 全换成 CRLF」的归一化
		// 跑在了**本来就是 CRLF** 的文件上，docs/roadmap.md 的 659 行里
		// 627 行变成 CR CR LF，**而且提交了进去** —— 本条与 git 的 autocrlf
		// 两道都没吭声，是后来读文件时肉眼撞见的。
		//
		// ⚠️ 后果与上一条同形：breakcheck 是裸字节匹配，锚点里的换行
		// 只按纯 CRLF 展开，打在这种文件上的破坏会**悄悄失配**。
		// ⚠️ **第三个盲区，20260910 撞到，本条同样不查**：
		// 一份文件**整份**从 CRLF 翻成 LF 时，`crlf == 0` ——
		// 于是上面那个 `crlf > 0 && crlf != lf` 的**第一个合取项就假**，
		// 而下面这条 `cr != crlf` 是 0 != 0，也假。**两条都不响。**
		//
		//	混用          crlf > 0 且 crlf != lf   ⇒ 查
		//	多余的回车符  cr != crlf               ⇒ 查
		//	⚠️ **整份翻成 LF** crlf == 0                ⇒ **不查**
		//
		// 起因是一句 `gofmt -w`：它把 `cmd/oracle/quote_wire_test.go` 整份写成了 LF
		//（实测 CRLF 0 / LF 332），**而本条一个字都没说**。
		// 评审在 `docs/design.md` 上独立复现过，跑出来是 `ok`。
		//
		// ⚠️ **而这个局面不是静止的**（评审 20260910 实测，我在字节级复核过）：
		//
		//	仓库存的（git cat-file blob）  **CRLF**，317/317，`.gitattributes` 不存在
		//	gofmt 的输出                  ⚠️ **LF** —— eol_test.go 与 version.go 各测一次，都翻
		//	本条                          **一个字都不会说**
		//
		// ⇒ **「没有约定」不等于「中立」**：两个候选里有一个**每次 `gofmt -w` 都在推进**，
		// 另一个只靠「没人跑 gofmt -w」。20260910 已经走了一份
		//（`cmd/oracle/quote_wire_test.go`），而它被发现纯属顺手看了一眼。
		//
		// ⚠️ 记在这里是**纯事实**，不含决定 —— 但读的人要知道
		// 「先放着」不是第三个选项，**它是「归一到 LF」的慢速版本，只是没人签字**。
		//
		// ⚠️ **本条刻意不改**：判「整份 LF 算不算违规」要先定下全库的行尾约定，
		// 而那个约定此刻只存在于「现状恰好是 CRLF」里 —— **没有一处写下来**。
		// **一条守卫不该替一个从未被决定的约定做决定。**（列为待办。）
		if cr := bytes.Count(b, []byte("\r")); cr != crlf {
			stray = append(stray, fmt.Sprintf("%s（回车符 %d 个，其中只有 %d 个跟着换行）",
				p, cr, crlf))
		}
		return nil
	})
	return scanned, mixed, stray, err
}

// reportLineEndings 把扫描结果变成断言。minFiles 是空转下界。
func reportLineEndings(t *testing.T, scanned int, mixed, stray []string, minFiles int) {
	t.Helper()
	// ⚠️ 扫不到文件时这条恒真。卡个下界，否则它会在
	// 「过滤条件写错了」的那一天悄悄变成一条空断言。
	if scanned < minFiles {
		t.Fatalf("只扫了 %d 个文件（下界 %d）—— 太少，过滤条件八成写错了，本条在空转", scanned, minFiles)
	}
	sort.Strings(mixed)
	for _, p := range mixed {
		t.Errorf("⚠️ %s **内部**混着 CRLF 与 LF —— "+
			"breakcheck 是裸字节匹配，对混合换行的文件刻意不猜，"+
			"于是打在这个文件上的破坏会**悄悄不再跑**，而汇总行里看不出来。"+
			"把整个文件归一到一种换行", p)
	}
	sort.Strings(stray)
	for _, p := range stray {
		t.Errorf("⚠️ %s 里有**不跟着换行的回车符** —— 多半是一次归一化"+
			"重复跑在了已经是 CRLF 的文件上（CRLF 变成 CR CR LF）。"+
			"⚠️ 它躲得过上面那条：每个换行前面都有回车，crlf == lf 照样成立。"+
			"把整个文件归一到一种换行", p)
	}
	t.Logf("扫了 %d 个文件，内部换行混着的 %d 个、有多余回车符的 %d 个",
		scanned, len(mixed), len(stray))
}

// TestStrayCRIsActuallyCaught 在临时目录里造一个真的坏文件，验那条检查会红。
//
// ⚠️ 它补的是一个具体的洞：2026-09-09 一次归一化重复跑在已经是 CRLF 的
// docs/roadmap.md 上，659 行里 627 行变成「回车回车换行」，**提交了进去**，
// 而当时的 TestNoFileMixesLineEndings 是绿的 —— 它只比 crlf 与 lf 的**条数**，
// 而那种文件里每个换行前面都有回车，两个数恰好相等。
//
// ⚠️ 三个用例缺一不可：只留坏文件那一条的话，一个「永远报坏」的实现也全绿。
func TestStrayCRIsActuallyCaught(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"bad.md":   []byte("一\r\r\n二\r\r\n"),
		"good.md":  []byte("一\r\n二\r\n"),
		"plain.md": []byte("一\n二\n"),
	}
	for name, b := range files {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	scanned, mixed, stray, err := scanLineEndings(dir)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != len(files) {
		t.Fatalf("扫到 %d 个文件，造了 %d 个", scanned, len(files))
	}
	if len(mixed) != 0 {
		t.Errorf("⚠️ 三个文件各自内部都只有一种换行，却报了「混着」：%v —— "+
			"这条检查在**多报**，而多报会逼下一个人去给它加例外", mixed)
	}
	if len(stray) != 1 || !strings.Contains(stray[0], "bad.md") {
		t.Fatalf("⚠️ 期望**只有** bad.md 被判出多余回车符，得到 %v —— "+
			"bad.md 正是 20260909 那次真实事故的形状：每个换行前面都有回车，"+
			"条数相等，因此躲得过「混着 CRLF 与 LF」那一条", stray)
	}
}

// TestNoFileUsesCRLF 钉住 **2026-09-10 使用者裁决的行尾约定：全库 LF**。
//
// # ⚠️ 为什么它是新加的一维，而不是改上面那条
//
// `TestNoFileMixesLineEndings` 查两件事：**混用**、**多余的回车符**。
// ⚠️ 两者对「一份文件**整份**从 CRLF 翻成 LF」都是瞎的（`crlf == 0` 时
// 两个判据的第一个合取项都假）—— 而在**约定未定**的年代，那个瞎是对的：
// **一条守卫不该替一个从未被决定的约定做决定。**
//
// 约定定下来之后，那个瞎就该补上了。而补法是**加一维**，不是改那两维 ——
// 那两维有自己的单元测试与破坏（189/190）钉着，改它们会把它们打死。
//
// # ⚠️ 这条约定为什么必须有守卫
//
//	仓库此前存的  CRLF，317/317，而**没有任何一处写下过这个约定**
//	gofmt 的输出  **LF** —— 每个 Go 文件、每次 `gofmt -w` 都翻一份
//
// ⇒ 定成 CRLF 意味着每个人每次格式化后都要**记得**还原，而本仓库
// 反复证明过纪律挡不住；定成 LF 则与工具链一致，`gofmt` 从此不再制造漂移。
// 见 `.gitattributes` 里的完整理由。
func TestNoFileUsesCRLF(t *testing.T) {
	var withCRLF []string
	n := 0
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
		switch strings.ToLower(filepath.Ext(p)) {
		case ".go", ".json", ".md", ".sh", ".yml", ".yaml":
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		n++
		if c := bytes.Count(b, []byte("\r\n")); c > 0 {
			withCRLF = append(withCRLF, fmt.Sprintf("%s（%d 处 CRLF）", p, c))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ **把两条扫描的范围钉在一起。** 两处各写一份过滤规则，
	// 它们会悄悄分叉 —— 而分叉之后「两条都绿」说明不了任何事：
	// 可能只是各自扫了自己那一半。
	scanned, _, _, err := scanLineEndings(".")
	if err != nil {
		t.Fatal(err)
	}
	if n != scanned {
		t.Fatalf("⚠️ 本条扫到 %d 个文件，而 TestNoFileMixesLineEndings 扫到 %d 个 —— "+
			"**两处的过滤规则分叉了**，此后「两条都绿」说明不了任何事", n, scanned)
	}
	if n < 50 {
		t.Fatalf("⚠️ 只扫了 %d 个文件（下界 50）—— 过滤条件八成写错了，本条在空转", n)
	}
	if len(withCRLF) > 0 {
		t.Errorf("⚠️ 这些文件用了 CRLF，而 `.gitattributes` 定的是 **LF**（2026-09-10 裁决）：\n  %s\n"+
			"⚠️ 多半是一次 `gofmt -w` 之外的写入 —— 归一回 LF。"+
			"**约定已经写下来了，所以这条现在有东西可执行**", strings.Join(withCRLF, "\n  "))
	}
}
