package probe

import (
	"math"
	"testing"
)

// TestTickFracIsTheConstructedFraction 钉住零层用的那个反算。
//
// ⚠️ 最要紧的是 tick <= 0 那一档返回 -1 而不是 0：
// 免费行情**不下发 price_tick**，`q.PriceTick` 恒为 0，
// 那是最容易走到这里的一条路 —— 而 0 会被读成「零头是 0，即整数倍」，
// 于是一整轮「非整数倍」的实验会显示成「全都构造成了整数倍」，
// **并且每一笔照样发得出去、照样被拒、照样打印一行结论**。
func TestTickFracIsTheConstructedFraction(t *testing.T) {
	cases := []struct {
		name        string
		price, tick float64
		want        float64
	}{
		{"整数倍", 3345, 1, 0},
		{"零头 0.45（tick=1）", 3345 + 45.0/100, 1, 0.45},
		{"零头 0.45（tick=0.5）", 700 + 0.5*45/100, 0.5, 0.45},
		{"零头 0.45（tick=10）", 71000 + 10*45.0/100, 10, 0.45},
		{"零头 1/3", 3345 + 1.0/3, 1, 1.0 / 3},
		{"tick 未知 → -1，不是 0", 3345.45, 0, -1},
		{"tick 为负 → -1", 3345.45, -1, -1},
	}
	for _, c := range cases {
		got := tickFrac(c.price, c.tick)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("⚠️ %s：tickFrac(%v, %v) = %v，想要 %v",
				c.name, c.price, c.tick, got, c.want)
		}
	}
}

// TestTickFracAndOffTickAgreeOnTheHalfBoundary 钉住两者的关系。
//
// `offTick` 用的是**柜台的**判据（零头 < 半个 tick 才算「不是整数倍」），
// 而 `tickFrac` 是它的原料。⚠️ 把这一对分开测，是因为 20260909 那次翻案
// 的根子就在「用本库的判据去核对柜台会怎么判」——
// 现在判据只有一份，这条断言让它保持只有一份。
func TestTickFracAndOffTickAgreeOnTheHalfBoundary(t *testing.T) {
	const tick = 1.0
	for _, f := range []float64{0.001, 0.1, 1.0 / 3, 0.4, 0.45, 0.4999} {
		if !offTick(3345+f, tick) {
			t.Errorf("⚠️ 零头 %.4f < 半个 tick，柜台会判「不是整数倍」，offTick 却说不是", f)
		}
	}
	for _, f := range []float64{0.5, 0.55, 0.6, 0.7, 0.9} {
		if offTick(3345+f, tick) {
			t.Errorf("⚠️ 零头 %.4f >= 半个 tick，柜台**收下**这种价（kq_facts 45），"+
				"offTick 却说它不是整数倍", f)
		}
	}
	if offTick(3345, tick) {
		t.Error("⚠️ 整数倍被判成了非整数倍")
	}
	if offTick(3345.45, 0) {
		t.Error("⚠️ tick 未知时 offTick 必须说「不是」—— " +
			"免费行情不下发 price_tick，这条路走得到")
	}
}
