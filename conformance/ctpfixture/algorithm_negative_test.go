package ctpfixture

import "testing"

// TestExcludedUnrealizedRefusesUnknown 验证未知值必须拒绝；合成输入不代表新柜台配置实测。
func TestExcludedUnrealizedRefusesUnknown(t *testing.T) {
	for _, algorithm := range []string{"", "0", "5", "bogus"} {
		if _, err := excludedUnrealized(algorithm, 150); err == nil {
			t.Fatalf("未知算法未拒绝：%q", algorithm)
		}
	}
	for _, profit := range []float64{-40, 0, 150} {
		got, err := excludedUnrealized("2", profit)
		want := profit
		if want < 0 {
			want = 0
		}
		if err != nil || got != want {
			t.Fatalf("已知算法正对照失败：%v %v", got, err)
		}
	}
}
