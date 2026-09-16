package probe

import "testing"

// TestKQDeclaredDivergenceCoversExactlyTheClosableVsPricePairs 钉住「已声明的快期口径差」恰好是可平量 × 价格类那几对。
//
// ⚠️ 两个方向都要钉：
//
//	漏了一对   那一对在快期上相反时会报「❌ order.Check 的取值顺序在这个口子上是错的」—— 裁决之后是假话
//	多了一对   比如把 tick × limit 也算进去 —— 那一对两个柜台**一致**（最小变动价位先），
//	           它相反才是真回归，而此时会被归进「已声明」静静吞掉
func TestKQDeclaredDivergenceCoversExactlyTheClosableVsPricePairs(t *testing.T) {
	want := map[[2]string]bool{
		{"tick", "limit"}:       false, // 两个柜台一致，相反就是真回归
		{"tick", "close"}:       true,
		{"tick", "closetoday"}:  true,
		{"limit", "close"}:      true,
		{"limit", "closetoday"}: true,
	}
	if len(priorityPairs) != len(want) {
		t.Fatalf("⚠️ priorityPairs 有 %d 对，本测试只列了 %d 对 —— 新加的那一对要先想清楚它算不算已声明口径差",
			len(priorityPairs), len(want))
	}
	for _, p := range priorityPairs {
		w, ok := want[p]
		if !ok {
			t.Errorf("⚠️ priorityPairs 里的 %v 不在本测试的表里", p)
			continue
		}
		if got := kqDeclaredDivergence(p[0], p[1]); got != w {
			t.Errorf("⚠️ %v：kqDeclaredDivergence=%v，应为 %v", p, got, w)
		}
		// 与参数顺序无关
		if kqDeclaredDivergence(p[0], p[1]) != kqDeclaredDivergence(p[1], p[0]) {
			t.Errorf("⚠️ %v 换个顺序答案就变了", p)
		}
	}
}
