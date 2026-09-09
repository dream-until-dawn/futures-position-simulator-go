package ctp

import (
	"strconv"
	"testing"
)

// TestOrderRefIsReadableAsANumber 是本包**唯一**能提前抓住
// 「报单引用带前缀」的守卫。
//
// ⚠️ 判据不是「等于某个字符串」，是**柜台能不能把它读成数字** ——
// 前者会随格式微调而无谓地红，后者恰好是柜台真正在做的那件事。
//
// ⚠️ 它的价值在于：这个缺陷**只在一次会话的第二笔单上**暴露，
// 而线上每个实验当时都只发一笔。离线守卫补的正是这一段盲区。
func TestOrderRefIsReadableAsANumber(t *testing.T) {
	for _, n := range []int64{0, 1, 2, 9, 10, 999, 1_000_000, 999_999_999, 1_000_000_001} {
		got := formatOrderRef(n)
		back, err := strconv.ParseInt(got, 10, 64)
		if err != nil {
			t.Fatalf("formatOrderRef(%d) = %q —— ⚠️ **柜台读不出数字**。"+
				"后果不是报错，是同一会话第二笔起一律 ErrorID=22（probes.md §6.8）：%v", n, got, err)
		}
		if back != n {
			t.Fatalf("formatOrderRef(%d) = %q，读回来是 %d —— 引用对不上号，"+
				"回报就落不到正确的委托上", n, got, back)
		}
	}
}

// TestOrderRefsAreStrictlyIncreasing 钉住第二条性质：连着取出来的引用要递增。
//
// ⚠️ 与上一条**分开写**：前者管「读得出」，后者管「不重复」。
// 合成一条的话，红了分不清是哪一个坏了 —— 而这两个坏法的修法完全不同。
func TestOrderRefsAreStrictlyIncreasing(t *testing.T) {
	prev := int64(-1)
	for n := int64(0); n < 2000; n++ {
		v, ok := parseMaxOrderRef(formatOrderRef(n))
		if !ok {
			t.Fatalf("formatOrderRef(%d) 产出的引用自己都解析不回来", n)
		}
		if v <= prev {
			t.Fatalf("第 %d 个引用 %d 没有大于上一个 %d", n, v, prev)
		}
		prev = v
	}
}

// TestParseMaxOrderRef 钉住登录应答的解析，**含那个真实的毒样本**。
func TestParseMaxOrderRef(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"1", 1, true},          // 20260910 夜盘 SimNow 实际回的就是这个
		{"  42 ", 42, true},     // 定长字段右侧补空格，CTP 常见
		{"0", 0, true},          // ⚠️ 「读懂了是 0」与「读不懂」必须分开
		{"", 0, false},          // 字段为空
		{"p000000009", 0, false}, // ⚠️ 本库自己发出去过的形状 —— 柜台读不懂它
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, ok := parseMaxOrderRef(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseMaxOrderRef(%q) = (%d,%v)，要 (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
