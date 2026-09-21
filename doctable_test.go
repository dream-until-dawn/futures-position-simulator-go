package futsim

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// splitTableRows 找出被真换行拆开的表格行：以 '|' 开头、不以 '|' 结尾，且下一行非空、又不以 '|' 开头。纯函数。
//
// ⚠️ 只看「不以 '|' 结尾」会误报：GFM 允许省略行尾的 '|'（state.md 计数表里就有这样的行，后面紧跟下一行表格或空行，表格没断）。
// 被拆开的行的特征是**后面跟着一段不是表格行的文字** —— 渲染时表格在那里断掉，余下的几段变成正文。
// 代码块（``` 围起来的）里不查。
func splitTableRows(text string) []int {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var bad []int
	fence := false
	for i, l := range lines {
		if strings.HasPrefix(l, "```") {
			fence = !fence
			continue
		}
		if fence || !strings.HasPrefix(l, "|") || strings.HasSuffix(strings.TrimRight(l, " \t"), "|") || i+1 >= len(lines) {
			continue
		}
		next := strings.TrimSpace(lines[i+1])
		if next != "" && !strings.HasPrefix(next, "|") && !strings.HasPrefix(lines[i+1], "```") {
			bad = append(bad, i+1)
		}
	}
	return bad
}

// TestMarkdownTableRowsNotSplit：文档里的表格行不许被真换行拆开。
//
// 20260918 往 cn-futures-rules §13 #5 那一格追加结算单发现时，追加的文本里带了真换行，把一行拆成了六行，
// 随那一批合进了 main，所有测试照样全绿 —— 表格在渲染时就在那里断掉。20260921 发现并接回。
func TestMarkdownTableRowsNotSplit(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("docs", "*.md"))
	if err != nil {
		t.Fatal(err)
	}
	files = append(files, "README.md")
	if len(files) < 5 {
		t.Fatalf("⚠️ 只找到 %d 份文档 —— 本条在空转", len(files))
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range splitTableRows(string(b)) {
			t.Errorf("⚠️ %s:%d 这一行表格被拆开了（不以 '|' 结尾、下一行是正文）—— 格子里要换行用 <br>", f, n)
		}
	}
}

func TestSplitTableRowsDiscriminates(t *testing.T) {
	ok := "| a | b |\n| 1 | 2\n| 3 | 4 |\n\n正文\n```\n| x\ny\n```\n"
	if got := splitTableRows(ok); len(got) != 0 {
		t.Errorf("省略行尾 '|'、代码块里的都不算：得到 %v", got)
	}
	broken := "| a | b |\n| 1 | 第一段\n第二段 |\n"
	if got := splitTableRows(broken); len(got) != 1 || got[0] != 2 {
		t.Errorf("被拆开的第 2 行要报出来：得到 %v", got)
	}
}
