package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// TestSettleGranularityCandidatesMatchRegistration：候选名单与 design.md 的补测登记逐项、按序一致。
func TestSettleGranularityCandidatesMatchRegistration(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "design.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	i := strings.Index(doc, "##### 前置条件：补测")
	if i < 0 {
		t.Fatal("⚠️ design.md 里找不到 F10 补测的登记 —— 本条在空转")
	}
	block := doc[i:]
	if j := strings.Index(block[5:], "\n##"); j >= 0 {
		block = block[:j+5]
	}
	last := -1
	for _, name := range settleGranularityCandidates {
		k := strings.Index(block, "("+name+")")
		if k < 0 {
			t.Errorf("⚠️ 候选 (%s) 不在登记里", name)
			continue
		}
		if k < last {
			t.Errorf("⚠️ 候选 (%s) 在登记里的顺序与代码不同", name)
		}
		last = k
	}
	if len(settleGranularityCandidates) != 7 {
		t.Errorf("登记了 7 个候选，代码里 %d 个", len(settleGranularityCandidates))
	}
}

func TestSettleGranularityResiduals(t *testing.T) {
	d := decimal.RequireFromString
	// 两手单 24.91（每手 12.455）：每笔截断 0、每手截断 2×(12.455−12.45)=0.01
	// 一手单 12.455：每笔、每手都是 0.005
	res, err := settleGranularityResiduals([]settleOrder{{Fee: d("24.91"), Volume: 2, Trades: 1}, {Fee: d("12.455"), Volume: 1, Trades: 1}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"iii": "0", "i-t": "0.005", "i-o": "0.005", "i-l": "0.015", "i-r": "-0.005", "i-T": "0.005", "i-R": "-0.005"}
	for k, v := range want {
		if !res[k].Equal(d(v)) {
			t.Errorf("(%s) 残差 %s，期望 %s", k, res[k], v)
		}
	}
	groups := indistinguishableAmong(settleGranularityCandidates, res)
	if !reflect.DeepEqual(groups, [][]string{{"i-r", "i-R"}, {"i-t", "i-o", "i-T"}}) {
		t.Errorf("同值组 %v", groups)
	}
	if _, err := settleGranularityResiduals([]settleOrder{{Fee: d("24.91"), Volume: 2, Trades: 2}}); err == nil || !strings.Contains(err.Error(), "拆成了 2 笔") {
		t.Errorf("拆成多笔成交的单应当报错：%v", err)
	}
	if _, err := settleGranularityResiduals([]settleOrder{{Fee: d("1"), Volume: 0, Trades: 1}}); err == nil {
		t.Error("手数 0 应当报错")
	}
}
