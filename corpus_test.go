package futsim

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestCTPFixturesStayOutOfTheDIFFCorpus 断言 CTP 夹具**没有混进** DIFF 那个语料。
//
// # ⚠️ 它堵的是一次「解析成功」的事故，不是一次报错
//
// `conformance/fixture` 的 loadAll 扫的是 `testdata/probes/*.json`，
// 而它把持仓字段当成一个**泛型 map** 读。于是一份 CTP 夹具丢进那个目录：
//
//	不会报错 —— 它会**若无其事地解析成功**
//	然后带着一堆 CTP 字段名（Position / UseMargin / PosiDirection…）
//	流进棘轮、流进「对得上但未触发」的计数、流进覆盖率
//
// ⚠️ 而那些数字**看起来完全正常**：语料变大了，覆盖变好了。
// 这正是本仓库反复在防的那种形状 —— **坏消息伪装成好消息**。
//
// 判据是 CTP 夹具顶层的 `source` 标记。⚠️ 反过来查（要求 DIFF 夹具都带
// 某个标记）做不到：一百来份既有夹具都没有那个字段，
// 补进去等于重写全部语料，而重写语料这件事本身就会掩盖它想防的问题。
func TestCTPFixturesStayOutOfTheDIFFCorpus(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "probes", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 50 {
		t.Fatalf("⚠️ testdata/probes 下只有 %d 份 —— 太少，本条在空转", len(paths))
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var head struct {
			Source string `json:"source"`
		}
		if json.Unmarshal(b, &head) != nil {
			continue
		}
		if head.Source != "" {
			t.Errorf("⚠️ %s 带着 source=%q，而 testdata/probes 是**天勤 DIFF** 的语料 —— "+
				"CTP 夹具要落在 testdata/ctp/。"+
				"⚠️ 放在这里不会报错：DIFF 的加载器会若无其事地解析成功，"+
				"然后带着一堆 CTP 字段名流进棘轮与覆盖率，而那些数字看起来完全正常",
				p, head.Source)
		}
	}
	t.Logf("testdata/probes 下 %d 份，无一带 source 标记（即无 CTP 夹具混入）", len(paths))
}
