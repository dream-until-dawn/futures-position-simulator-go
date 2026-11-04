package refdata

import "testing"

// TestTickOutwardExact 验证通用算术能力；所有样本均为合成，不代表交易所规则。
func TestTickOutwardExact(t *testing.T) {
	for _, c := range []struct{ name, pre, ratio, tick, up, lo string }{
		{"分数跳", "3429", "0.06", "1", "3635", "3223"},
		{"区别两边向上", "100", "0.062", "0.5", "106.5", "93.5"},
		{"区别四舍五入", "100", "0.061", "0.5", "106.5", "93.5"},
		{"精确整除", "100", "0.06", "0.5", "106", "94"},
		{"零比例仍对齐", "100.1", "0", "0.5", "100.5", "100"},
		{"极细余数", "1", "0.00000000000000000001", "1", "2", "0"},
		{"非十进制整商", "1", "0.01", "0.03", "1.02", "0.99"},
		{"负理论下界", "1", "1.01", "0.03", "2.01", "-0.03"},
	} {
		t.Run(c.name, func(t *testing.T) {
			i := Instrument{PriceTick: d(c.tick), PriceLimitRatio: d(c.ratio), HasPriceLimitRatio: true}
			up, lo, ok := i.PriceLimits(d(c.pre), true, TickRounding(6))
			if !ok || !up.Equal(d(c.up)) || !lo.Equal(d(c.lo)) {
				t.Fatalf("向外精确结果错误: got=%s/%s ok=%v want=%s/%s", up, lo, ok, c.up, c.lo)
			}
		})
	}
}

func TestTickOutwardCompatibility(t *testing.T) {
	if TickOutward != TickRounding(6) {
		t.Fatal("向外枚举必须追加为6")
	}
	for n, r := range []TickRounding{TickRoundingUnknown, TickFloor, TickCeil, TickHalfUp, TickNone, TickInward} {
		if int(r) != n {
			t.Fatalf("旧枚举值改变: index=%d got=%d", n, r)
		}
	}
	if !TickRounding(6).Valid() {
		t.Fatal("向外枚举未识别")
	}
	if TickRounding(6).String() != "往外扩" {
		t.Fatal("向外名称错误")
	}
	if TickRounding(7).Valid() || TickRoundingUnknown.Valid() {
		t.Fatal("未知取整获准")
	}
	i := Instrument{PriceTick: d("1"), PriceLimitRatio: d("0.06"), HasPriceLimitRatio: true}
	for _, c := range []struct {
		name string
		inst Instrument
		pre  string
		has  bool
	}{
		{"缺比例", Instrument{PriceTick: d("1")}, "100", true},
		{"缺跳", Instrument{PriceLimitRatio: d("0.06"), HasPriceLimitRatio: true}, "100", true},
		{"负跳", Instrument{PriceTick: d("-1"), PriceLimitRatio: d("0.06"), HasPriceLimitRatio: true}, "100", true},
		{"缺昨结", i, "100", false}, {"零昨结", i, "0", true}, {"负昨结", i, "-1", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, _, ok := c.inst.PriceLimits(d(c.pre), c.has, TickRounding(6)); ok {
				t.Fatal("缺失或无效输入却向外成功")
			}
		})
	}
}
