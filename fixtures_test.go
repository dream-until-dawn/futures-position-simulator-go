package futsim

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// selfRef 标记「这一行是判据自身的一部分，扫描时跳过」。
//
// ⚠️ 自引用豁免必须**先于**判据设计好，不能等它红在自己身上再补。
// 本文件已经为此红过两次：先是判据写成「文中出现 .env」——注释里说明
// 「本层不读它」的那句话就把它点着了；改成「有没有真去读」之后，
// 存放这些调用形态的字符串列表**自己**又含有那些形态。
// 两次都不是被查对象出了问题，是判据没说清「怎么算一次读取」。
const selfRef = "// 判据自身"

// TestFixtureScanDeclaresItsBlindSpot 把这层扫描**查不了**的东西钉成一条断言。
//
// ⚠️ 它查不了「夹具里有没有出现凭据文件里的密码」——CI 上没有那个文件，
// 有也不该让测试去读。所以这一层只能查**形状**，查不了**具体值**。
//
// 这条测试的作用是：谁要是哪天把凭据读进这一层，
// 「本层不读凭据」这条约束会立刻红，而不是悄悄多出一个凭据读取点。
func TestFixtureScanDeclaresItsBlindSpot(t *testing.T) {
	src, err := os.ReadFile("fixtures_test.go")
	if err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(bytes.NewReader(src))
	for i := 1; sc.Scan(); i++ {
		line := sc.Text()
		if strings.Contains(line, selfRef) {
			continue
		}
		for _, r := range credentialReaders() {
			if strings.Contains(line, r) {
				t.Errorf("fixtures_test.go:%d 出现凭据读取 %q。"+
					"具体值的复查在 kq.Scrubbed（落盘时）做，两层用不同原理才不会一起失效；"+
					"本层只查形状", i, r)
			}
		}
	}
}

// credentialReaders 列出「真的去读凭据文件」的调用形态。
//
// 提到文件名不算，真去读才算——第一版把两者混为一谈，结果红在自己的注释上。
func credentialReaders() []string {
	return []string{
		`os.ReadFile(".env`,     // 判据自身
		`os.Open(".env`,         // 判据自身
		`ioutil.ReadFile(".env`, // 判据自身
		`probe.LoadEnv(`,        // 判据自身
		`os.Getenv(`,            // 判据自身
	}
}

// TestBlindSpotGuardDiscriminates 双向证明上面那条判据既不误伤也不漏放。
//
// ⚠️ 一个只做过「正常输入下通过」的判据什么都不说明。
// 这里两个方向各来一次：纯提及必须放过，真实读取必须逮住。
func TestBlindSpotGuardDiscriminates(t *testing.T) {
	mention := "本层不读凭据文件，只查形状"
	for _, r := range credentialReaders() {
		if strings.Contains(mention, r) {
			t.Errorf("判据 %q 命中了一句纯提及 —— 它会红在自己的说明文字上", r)
		}
	}

	// 一条连自己的说明都容忍不了的判据，会被人直接删掉，而不是被修对。
	for _, real := range []string{
		"data, _ := os.Read" + `File(".env")`,
		"pw := os.Get" + `env("KQ_PASSWORD")`,
	} {
		hit := false
		for _, r := range credentialReaders() {
			if strings.Contains(real, r) {
				hit = true
			}
		}
		if !hit {
			t.Errorf("⚠️ 判据放过了一次真实的凭据读取 %q —— 那它什么都挡不住", real)
		}
	}
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
	const canonical = "testdata/probes"
	var stray []string
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
		if dir == canonical || !strings.Contains(dir, "testdata") {
			return nil
		}
		stray = append(stray, filepath.ToSlash(p))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stray) > 0 {
		t.Errorf("⚠️ %s 之外还有 %d 份夹具：%v", canonical, len(stray), stray)
		t.Error("   落盘目录很可能配成了相对路径，随 cwd 另开了一棵树")
	}
}
