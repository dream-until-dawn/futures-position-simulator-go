package futsim

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestSimnowRefsResolve 断言代码与文档里每一处 `simnow_pending#N`
// **都能在 state.md 的那张表里找到对应的一条**。
//
// ⚠️ 这条守卫是被自己抓出来的需求。`conformance.Deviation.Validate` 要求
// 「已知口子差异」三样齐全，其中一样是**裁决者**——但它只查那个字段非空，
// **查不出引用是否真的解析得到**。
//
// 2026-09-09 我给一处新差异填了 `simnow_pending#2`，而 #2 说的是
// `CloseProfitByDate/ByTrade`，与那处差异毫无关系。
// 三样齐全、Validate 通过、对拍判「已知差异」——**而那个豁免是凭空的**。
//
// ⚠️ 一个可以指向不存在条目的豁免档，与「可以随口声明」只差一步：
// 前者至少写了个编号，而编号看起来就像查过了。
func TestSimnowRefsResolve(t *testing.T) {
	state, err := os.ReadFile(filepath.Join("docs", "state.md"))
	if err != nil {
		t.Fatal(err)
	}
	// 表里的行形如：`| 10 | 等什么 | 谁在等 | 版本 |`
	rowRe := regexp.MustCompile(`(?m)^\|\s*(\d+)\s*\|`)
	sec := string(state)
	i := strings.Index(sec, "## `simnow_pending`")
	if i < 0 {
		t.Fatal("⚠️ state.md 里找不到 simnow_pending 那一节 —— 本条的整个基准没了")
	}
	sec = sec[i:]
	if j := strings.Index(sec, "\n## "); j > 0 {
		sec = sec[:j]
	}
	known := map[string]bool{}
	for _, m := range rowRe.FindAllStringSubmatch(sec, -1) {
		known[m[1]] = true
	}
	if len(known) < 5 {
		t.Fatalf("⚠️ 只从 simnow_pending 里解析出 %d 条 —— "+
			"解析坏了，下面的检查会在一个几乎空的集合上跑（那会把真引用全判成假）",
			len(known))
	}

	refRe := regexp.MustCompile(`simnow_pending#(\d+)`)
	refs := map[string][]string{} // 编号 → 出现处
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
		switch strings.ToLower(filepath.Ext(p)) {
		case ".go", ".md":
		default:
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range refRe.FindAllStringSubmatch(string(b), -1) {
			refs[m[1]] = append(refs[m[1]], p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 一处引用都没扫到时这条恒真。卡下界。
	if len(refs) == 0 {
		t.Fatal("⚠️ 全仓一处 simnow_pending#N 都没扫到 —— 本条在空转")
	}

	nums := make([]string, 0, len(refs))
	for n := range refs {
		nums = append(nums, n)
	}
	sort.Strings(nums)
	for _, n := range nums {
		if known[n] {
			continue
		}
		where := refs[n]
		sort.Strings(where)
		t.Errorf("⚠️ 引用了 **simnow_pending#%s，而 state.md 的表里没有这一条** —— "+
			"出现在：%s。\n"+
			"⚠️ 一个指向不存在条目的裁决者引用，与「可以随口声明豁免」只差一步："+
			"编号看起来就像查过了。要么补上那一条，要么改成正确的编号",
			n, strings.Join(where, "、"))
	}
	t.Logf("simnow_pending 在册 %d 条；代码与文档里引用了 %d 个不同编号：%s",
		len(known), len(refs), fmt.Sprint(nums))
}
