package probe

import "testing"

// TestDecideFallbackNeverRisksYesterday 穷举平仓回退判定，钉住三条不变式。
//
// ⚠️ 最要紧的一条是：**只要账上有昨仓，任何路径都不得回退到 CLOSE。**
// 原实现的失败方向朝着「悄悄平了别的仓」，而日志只会说「换一种开平标志再试」——
// 它不是没平掉，它是**平了别的**，而那批昨仓是六条实验共用、当晚不可再生的。
func TestDecideFallbackNeverRisksYesterday(t *testing.T) {
	cases := []struct {
		name       string
		volHis     float64
		done       bool
		volumeLeft int
		want       fallbackDecision
	}{
		{"全成", 0, true, 0, fallbackDone},
		{"全成·有昨仓", 2, true, 0, fallbackDone},
		{"超时·无昨仓", 0, false, 1, fallbackStopTimeout},
		{"超时·有昨仓", 2, false, 1, fallbackStopTimeout},
		{"被拒·无昨仓", 0, true, 1, fallbackAllowed},
		{"被拒·有昨仓", 2, true, 1, fallbackForbiddenYesterday},
		{"被拒·昨仓半手", 0.5, true, 1, fallbackForbiddenYesterday},
	}
	if len(cases) != 7 {
		t.Fatalf("用例有 %d 条，应为 7 —— 增删了就同步改这个数", len(cases))
	}
	for _, c := range cases {
		if got := decideFallback(c.volHis, c.done, c.volumeLeft); got != c.want {
			t.Errorf("%s：decideFallback(%v,%v,%d) = %v，应为 %v",
				c.name, c.volHis, c.done, c.volumeLeft, got, c.want)
		}
	}

	// —— 不变式一：有昨仓时，**没有任何输入**能得到「允许回退」——
	//
	// ⚠️ 这条是绝对断言，与上面的用例表分开写：用例表是我列出来的组合，
	// 而这一条穷举了输入空间。**列表会漏，穷举不会。**
	for _, done := range []bool{true, false} {
		for left := 0; left <= 3; left++ {
			for _, his := range []float64{0.001, 1, 2, 100} {
				if decideFallback(his, done, left) == fallbackAllowed {
					t.Errorf("⚠️ 昨仓 %v、done=%v、left=%d 时竟然允许回退 CLOSE —— "+
						"实测 CLOSE 在 UseHistory 交易所上被解释为平昨，这会平掉昨仓",
						his, done, left)
				}
			}
		}
	}

	// —— 不变式二：超时**永远**不回退，与昨仓无关 ——
	//
	// 超时不等于被拒。混为一谈时，「限价没被打到」也会触发回退。
	for _, his := range []float64{0, 1, 5} {
		for left := 1; left <= 3; left++ {
			if d := decideFallback(his, false, left); d != fallbackStopTimeout {
				t.Errorf("⚠️ 超时（昨仓 %v，left=%d）得到 %v，应为「撤单并停止」—— "+
					"超时不等于被拒", his, left, d)
			}
		}
	}
}
