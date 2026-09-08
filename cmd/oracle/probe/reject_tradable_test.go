package probe

import "testing"

// TestFakeMonthOf 钉住「造一个不存在的合约」这一步。
//
// ⚠️ 它是 reject-tradable 那条实验的**前提**：造出来的东西必须
//
//	同品种   换品种会同时改掉 tick 与乘数，于是「合约不存在」
//	         与「价格不合法」又搅在一起
//	真不存在 造出一个**碰巧存在**的合约，那笔单可能真的挂上去
//
// ⚠️ 第二条这里查不了 —— 它靠 9912 这个月份（2099 年 12 月）在实践上
// 不会有合约。查不了就写出来，别让「测过了」盖住它。
func TestFakeMonthOf(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ag2702", "ag9912"},
		{"rb2701", "rb9912"},
		{"i2701", "i9912"},
		{"SR2701", "SR9912"}, // 郑商所是大写品种码
		{"", ""},             // 空串：造不出来
		{"2701", ""},         // ⚠️ 没有品种码：**不能**返回 "9912"，
		{"abcd", ""},         //    那会是一个语法上合法、语义上莫名的合约
	}
	for _, c := range cases {
		if got := fakeMonthOf(c.in); got != c.want {
			t.Errorf("fakeMonthOf(%q) = %q，应为 %q", c.in, got, c.want)
		}
	}
	// ⚠️ 判别力：必须有「造得出」和「造不出」两侧，
	// 否则一个恒返回 "" 的实现也能通过。
	made, refused := 0, 0
	for _, c := range cases {
		if c.want == "" {
			refused++
		} else {
			made++
		}
	}
	if made == 0 || refused == 0 {
		t.Fatalf("⚠️ 用例只覆盖一侧（造得出 %d / 造不出 %d）", made, refused)
	}
}
