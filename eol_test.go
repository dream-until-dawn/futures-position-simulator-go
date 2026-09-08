package futsim

import (
	"bytes"
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
	var mixed []string
	scanned := 0
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
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
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 扫不到文件时这条恒真。卡个下界，否则它会在
	// 「过滤条件写错了」的那一天悄悄变成一条空断言。
	if scanned < 50 {
		t.Fatalf("只扫了 %d 个文件 —— 太少，过滤条件八成写错了，本条在空转", scanned)
	}
	sort.Strings(mixed)
	for _, p := range mixed {
		t.Errorf("⚠️ %s **内部**混着 CRLF 与 LF —— "+
			"breakcheck 是裸字节匹配，对混合换行的文件刻意不猜，"+
			"于是打在这个文件上的破坏会**悄悄不再跑**，而汇总行里看不出来。"+
			"把整个文件归一到一种换行", p)
	}
	t.Logf("扫了 %d 个文件，内部换行混着的 %d 个", scanned, len(mixed))
}
