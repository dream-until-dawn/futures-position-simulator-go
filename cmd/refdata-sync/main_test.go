package main

import "testing"

// TestMatcherDoesNotCrossProducts 断言品种前缀不会串到别的品种上。
//
// ⚠️ 这不是假想的边界：大商所同时有 c（玉米）与 cs（淀粉）。
// 光用 HasPrefix 的话 DCE.c 会把 DCE.cs2701 也拉进来，
// 而它不会报错 —— 多出来的品种带着自己的时段表进汇总，
// 「同品种内一致」那道检查也查不出它，因为它是**另一个**品种。
func TestMatcherDoesNotCrossProducts(t *testing.T) {
	m := matcher([]string{"DCE.c", "SHFE.a"})
	cases := []struct {
		id   string
		want bool
	}{
		{"DCE.c2701", true},   // 玉米，要
		{"DCE.cs2701", false}, // ⚠️ 淀粉，不要 —— 光 HasPrefix 会中
		{"SHFE.a2701", true},
		{"SHFE.ag2702", false}, // ⚠️ 白银，不要
		{"DCE.c", false},       // 只有品种没有月份
		{"DCE.m2701", false},   // 没要的品种
		{"KQ.i@DCE.c", false},  // 合成指数，不要
	}
	if len(cases) != 7 {
		t.Fatalf("用例 %d 条，应为 7 —— 增删了就同步改这个数", len(cases))
	}
	for _, c := range cases {
		if got := m(c.id); got != c.want {
			t.Errorf("⚠️ %s：判为 %v，应为 %v", c.id, got, c.want)
		}
	}
}

// TestMatcherEmptyWantMatchesNothing 断言空清单不会变成「全都要」。
func TestMatcherEmptyWantMatchesNothing(t *testing.T) {
	m := matcher(nil)
	for _, id := range []string{"DCE.c2701", "SHFE.rb2701", ""} {
		if m(id) {
			t.Errorf("⚠️ 空品种清单却匹配上了 %q —— 空不该等于全要", id)
		}
	}
}
