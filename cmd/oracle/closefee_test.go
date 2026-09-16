package main

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// TestCloseFeeCandidates 钉住 §13 #21 两个判别实验的判定表。
//
// ⚠️ 判定只说「与谁的预言一致」，不说「谁对」：
//
//	E1 只有昨仓、通用平仓  a → 平昨档 0.2；b、c → 平今档 0.1  ⇒ 分开 a 与 {b, c}
//	E2 只有昨仓、显式平昨  a、b → 平昨档 0.2；c → 0.1        ⇒ 分开 {a, b} 与 c
//
// ⚠️ 两档费率相同的品种上这张表什么都分不开 —— 那由 closeFeeRates 提前报错拦住，不在这里。
func TestCloseFeeCandidates(t *testing.T) {
	d := decimal.RequireFromString
	today, yd := d("0.1"), d("0.2")
	for _, c := range []struct {
		name  string
		mode  closeFeeMode
		delta decimal.Decimal
		want  []string
	}{
		{"E1 收平今档 ⇒ b、c 活，a 被否", closeFeeBare, d("0.1"), []string{"b", "c"}},
		{"E1 收平昨档 ⇒ 只有 a 活", closeFeeBare, d("0.2"), []string{"a"}},
		{"E2 收平昨档 ⇒ a、b 活，c 被否", closeFeeYd, d("0.2"), []string{"a", "b"}},
		{"E2 收平今档 ⇒ 只有 c 活（a、b 都被否）", closeFeeYd, d("0.1"), []string{"c"}},
		{"E1 收了第四个数 ⇒ 谁都没预言到", closeFeeBare, d("0.15"), nil},
		{"E2 收了第四个数 ⇒ 谁都没预言到", closeFeeYd, d("0.3"), nil},
	} {
		alive, why := closeFeeCandidates(c.mode, c.delta, today, yd)
		if strings.Join(alive, ",") != strings.Join(c.want, ",") {
			t.Errorf("⚠️ %s：活着的是 %v，期望 %v", c.name, alive, c.want)
		}
		if len(c.want) == 0 && !strings.Contains(why, "谁都没预言到") {
			t.Errorf("⚠️ %s：没预言到的情形要在理由里说出来，得到 %q", c.name, why)
		}
		if why == "" {
			t.Errorf("%s：理由不许为空 —— 判定要能被人复核", c.name)
		}
	}
	// ⚠️ 反向：两个实验对同一个增量给出**不同**的存活集合，否则跑两次等于跑一次
	e1, _ := closeFeeCandidates(closeFeeBare, d("0.1"), today, yd)
	e2, _ := closeFeeCandidates(closeFeeYd, d("0.1"), today, yd)
	if strings.Join(e1, ",") == strings.Join(e2, ",") {
		t.Errorf("⚠️ 同一个增量下两个实验的存活集合相同（%v）—— 那 E2 就没有独立的判别力", e1)
	}
}

// TestCloseFeeModeOffsets 钉住两个实验发的**不是同一种**平仓标志 —— 它们的区别全在这一项上。
func TestCloseFeeModeOffsets(t *testing.T) {
	if closeFeeBare.offset() == closeFeeYd.offset() {
		t.Fatal("⚠️ E1 与 E2 发的开平标志相同 —— 两个实验分开的正是这一项")
	}
	if !strings.Contains(closeFeeBare.String(), "通用") || !strings.Contains(closeFeeYd.String(), "平昨") {
		t.Errorf("名字要说清是哪个实验：%q / %q", closeFeeBare.String(), closeFeeYd.String())
	}
	// 落盘注记由阶段派生，两份不许相同（对调注记在语法上就说不出口）
	a, b := closeFeeStage{mode: closeFeeBare}, closeFeeStage{mode: closeFeeBare, after: true}
	if a.note() == b.note() || a.tradesExpected() || !b.tradesExpected() {
		t.Errorf("⚠️ 两份落盘的注记 / 是否附成交明细要分开：%q / %q", a.note(), b.note())
	}
}
