package futsim

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// packageDocs 取全仓**每一份非测试 Go 文件的包注释**。
//
// ⚠️ 它是「派生」而不是「枚举」：包注释恰好就是**对外声明的全集**
// （pkg.go.dev 显示的就是它），所以不需要有人记得往名单里加一行。
// 这一条与只列 doc.go 的那份名单互补 —— 名单管根包，这里管每个包。
func packageDocs(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// ⚠️ 不跳 cmd/oracle：嵌套模块的包同样发布在 pkg.go.dev 上。
			if name := d.Name(); name == ".git" || name == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, p, nil, parser.ParseComments|parser.PackageClauseOnly)
		if perr != nil || f.Doc == nil {
			return nil
		}
		out[filepath.ToSlash(p)] = f.Doc.Text()
		return nil
	})
	if err != nil {
		t.Fatalf("走目录失败：%v", err)
	}
	return out
}

// TestNoStaleForbiddenPhrasesInPackageDocs 把禁语扫描推到**每个包的包注释**。
//
// # 这块缺口是怎么被发现的
//
// 20260909 把 doc.go 加进扫描范围时，理由是「包注释是这个库最公开的一句话」。
// ⚠️ 而那个理由**对每一个包都成立**：`order`、`probe`、`conformance` 各自的
// 包注释同样显示在 pkg.go.dev 上，却都不在名单里（评审方当天指出）。
//
// 修法不是往名单里再加几行，是**派生**：包注释恰好就是对外声明的全集。
//
// ⚠️ 上线当场就抓到一处：`probe` 的包注释写着「跑**六条**判别实验」，
// 而那时已经二十几条。它躲过了两天 —— 因为它既不在 docs/ 下，
// 也不是 doc.go。
//
// # 讲与用仍然分开
//
// 只看**包注释**，不看所有注释：代码注释里讨论这些字符串是正当的
// （本文件自己就是例子）。这与「只加 doc.go 不加全部 .go」是同一条界线。
//
// ⚠️ **这是本仓库第 3 份「讲/用」判据**（作用域）。另两份：
// fixtures_test.go 用**句法层**（剥引号内的片段），
// kqref_test.go 用**邻近关键词**（前后两行的「推翻/接续」）。
// **三处不共享判定**，而它们解的是同一个概念 ——
// 它们不是互相校验的双实现，是三个消费者，所以
// 「共享数据可以，共享判定不行」不适用（评审方 20260909 指出）。
// 写下这段是让漂移可见。
//
// 本处还有一层豁免：包注释里出现禁语但**指向了 docs/state.md** 时放行。
// 边界两侧各钉一条：破坏 174 不指向必红、175 指向了必绿。
func TestNoStaleForbiddenPhrasesInPackageDocs(t *testing.T) {
	docs := packageDocs(t)
	// ⚠️ **分臂各设下界，不看合计。**
	//
	// 只设合计下界挡得住「走目录整个坏掉」，挡不住「少走了一棵子树」：
	// 破坏 165 当场演示过 —— 跳掉 cmd/oracle 之后份数从 22 掉到 18，
	// **仍然过了合计下界**，于是整个嵌套模块的包注释被安静地放行。
	// 这与 TestNotModeledDeclarationsHaveNotExpired 里那条是同一个盲区
	// （silent-risks 方法论 27 的对偶：合计判据防「有东西没被算进去」，
	// 分项判据防「某一类整个不见了」）。
	main, nested := 0, 0
	for p := range docs {
		if strings.HasPrefix(p, "cmd/oracle/") {
			nested++
		} else {
			main++
		}
	}
	for _, arm := range []struct {
		name string
		n    int
		min  int
	}{
		{"主模块", main, 12},
		{"嵌套模块 cmd/oracle", nested, 3},
	} {
		if arm.n < arm.min {
			t.Fatalf("⚠️ 「%s」只找到 %d 份包注释（下界 %d）—— "+
				"那一棵子树很可能整个没走到，而它的失效会被合计掩盖。"+
				"扫不到的话那些包注释会被安静地放行（各臂：主 %d / 嵌套 %d）",
				arm.name, arm.n, arm.min, main, nested)
		}
	}
	paths := make([]string, 0, len(docs))
	for p := range docs {
		paths = append(paths, p)
	}
	hits := 0
	for _, p := range paths {
		txt := docs[p]
		// 豁免：包注释在**讲这套机制**（指向 state.md）时可以出现禁语。
		// 与 markdown 那份扫描的第三条豁免同义。
		if strings.Contains(txt, "docs/state.md") {
			continue
		}
		for _, f := range forbidden {
			if strings.Contains(txt, f.phrase) {
				hits++
				t.Errorf("⚠️ %s 的**包注释**里出现禁语「%s」（键 %s）—— "+
					"该状态已翻转，此处是过期陈述。⚠️ 包注释是 pkg.go.dev "+
					"显示的那句话，比 README 还先被看到。"+
					"若它在讲这套机制，加一条指向 docs/state.md 的链接", p, f.phrase, f.key)
			}
		}
	}
	t.Logf("扫了 %d 份包注释（主模块 %d / 嵌套 %d），命中 %d 处", len(docs), main, nested, hits)
}
