package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func decs(ss ...string) []decimal.Decimal {
	var out []decimal.Decimal
	for _, s := range ss {
		out = append(out, decimal.RequireFromString(s))
	}
	return out
}

// TestSettleRoundingResiduals 用手算过的逐笔费钉住五个候选的残差。
//
// ⚠️ 样本挑的是能把五个都分开的：9.603（截断 −、四舍五入 −）、9.606（截断 −、四舍五入进位 +）、9.609。
func TestSettleRoundingResiduals(t *testing.T) {
	got := settleRoundingResiduals(decs("9.603", "9.606", "9.609", "0.2"))
	want := map[string]string{
		"iii": "0",
		"i-t": "0.018",  // 0.003 + 0.006 + 0.009 + 0
		"i-r": "-0.002", // 0.003 − 0.004 − 0.001 + 0
		"i-T": "0.008",  // 29.018 − 29.01
		"i-R": "-0.002", // 29.018 − 29.02
	}
	for k, w := range want {
		if !got[k].Equal(decimal.RequireFromString(w)) {
			t.Errorf("⚠️ %s：得到 %s，应为 %s", k, got[k], w)
		}
	}
	// 这组样本里 i-r 与 i-R 恰好同值 —— indistinguishable 必须把它说出来
	g := indistinguishable(got)
	if len(g) != 1 || strings.Join(g[0], ",") != "i-r,i-R" {
		t.Errorf("⚠️ 同值组应恰好是 [i-r i-R]，得到 %v", g)
	}
}

// TestSettleRoundingAllIntegerCentsCollapse 钉住：逐笔费全是整分时五个候选**全部同值** ——
// 这正是登记里「交易日 20260917 已有的四笔全是整分，今天要造带小数的成交」的理由。
func TestSettleRoundingAllIntegerCentsCollapse(t *testing.T) {
	g := indistinguishable(settleRoundingResiduals(decs("0.2", "0.2", "2", "2")))
	if len(g) != 1 || len(g[0]) != 5 {
		t.Errorf("⚠️ 全整分时五个候选应全部同值（一组 5 个），得到 %v —— 否则「今天要造小数」就没有理由", g)
	}
}

// TestCommissionDeltaIsDecimalExact 钉住逐笔增量按十进制算，不带 float 尾巴。
func TestCommissionDeltaIsDecimalExact(t *testing.T) {
	if d := commissionDelta(4.4, 14.003); !d.Equal(decimal.RequireFromString("9.603")) {
		t.Errorf("⚠️ 14.003 − 4.4 得到 %s，应为 9.603 —— 判的正是小数位，float 尾巴会冒充一个取整残差", d)
	}
}

// TestSettleRoundingCandidatesMatchPreRegistration 钉住候选的名字与顺序与 state.md 事前登记一致。
func TestSettleRoundingCandidatesMatchPreRegistration(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "state.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, name := range settleRoundingCandidates {
		if !strings.Contains(doc, "("+name+")") {
			t.Errorf("⚠️ 候选 (%s) 不在 state.md 的事前登记里 —— 代码与登记分岔了", name)
		}
	}
	if len(settleRoundingCandidates) != 5 {
		t.Errorf("⚠️ 登记了 5 个取整候选（外加兜底的 (ii)），代码里有 %d 个", len(settleRoundingCandidates))
	}
}
