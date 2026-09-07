package probe

import "testing"

// TestSpreadSampleGuardActuallyFails 是「守卫的守卫」。
//
// silent-risks.md 第一节第 2 条要求：实验 3 的样本守卫必须存在**且真能失败**。
// 一条永远通过的守卫和没有守卫，在通过样本上长得一模一样。
// 所以这里逐个喂进本该被拒的样本，断言它们**全部**被拒。
func TestSpreadSampleGuardActuallyFails(t *testing.T) {
	bad := []struct {
		name string
		syms []string
	}{
		{"单合约（两种解释在此同值，必须拒）", []string{"SHFE.rb2701"}},
		{"同合约重复（等价于单合约）", []string{"SHFE.rb2701", "SHFE.rb2701"}},
		{"跨品种（测不出合并范围）", []string{"SHFE.rb2701", "DCE.m2701"}},
		{"三个合约", []string{"SHFE.rb2701", "SHFE.rb2610", "SHFE.rb2605"}},
		{"缺交易所前缀", []string{"rb2701", "rb2610"}},
		{"空样本", nil},
	}
	// ⚠️ 下界断言：用例条数必须与预期相等，不是 > 0。
	// `len(cases) > 0` 在用例被删到只剩一条时照样绿。
	if len(bad) != 6 {
		t.Fatalf("反例用例数应为 6，实际 %d —— 用例被增删了，请同步更新下界", len(bad))
	}
	for _, c := range bad {
		if err := checkSpreadSample(c.syms); err == nil {
			t.Errorf("守卫放过了本该被拒的样本：%s %v", c.name, c.syms)
		}
	}

	good := [][]string{
		{"SHFE.rb2701", "SHFE.rb2610"},
		{"CZCE.AP610", "CZCE.AP701"}, // 郑商所三位月份
	}
	if len(good) != 2 {
		t.Fatalf("正例用例数应为 2，实际 %d", len(good))
	}
	for _, syms := range good {
		if err := checkSpreadSample(syms); err != nil {
			t.Errorf("守卫拒绝了合法样本 %v: %v", syms, err)
		}
	}
}

func TestProductOf(t *testing.T) {
	cases := []struct{ in, prod, month string }{
		{"rb2701", "rb", "2701"},
		{"AP610", "AP", "610"}, // 郑商所：大写 + 三位年月
		{"m2701", "m", "2701"},
		{"SA701", "SA", "701"},
	}
	if len(cases) != 4 {
		t.Fatalf("用例数应为 4，实际 %d", len(cases))
	}
	for _, c := range cases {
		p, m := productOf(c.in)
		if p != c.prod || m != c.month {
			t.Errorf("productOf(%q) = (%q,%q)，期望 (%q,%q)", c.in, p, m, c.prod, c.month)
		}
	}
}
