package kq

import "testing"

// TestGuardMaxVolumeFailsSafe 断言手数上限的**失败方向朝着「拦」**，不是朝着「放行」。
//
// ⚠️ 这是真账户上的安全阀，所以判据的形状比判据的内容更要紧。
//
// 它原先写作「是开仓才拦」（`r.Offset == Open`）。Offset 的底层类型是 string，
// 零值是 ""，而常量只有 OPEN / CLOSE / CLOSETODAY 三个——
// 于是**任何未设置或拼错的 Offset 都绕过了上限**。
//
// 更糟的是这个洞的来历：上限原本对所有委托一视同仁，一次实测里
// 2 手仓因此平不掉（防止扩大风险的守卫阻止了缩小风险）。
// 修那次故障时人会本能地把阀门往松了调，而**松的方向恰好是危险的方向**。
//
// 现在判据翻成「不是**已识别的**平仓就拦」：未知取值从「不拦」变成「多拦一次」，
// 而多拦一次会立刻被看见。将来加 CloseYesterday 时漏改这里，后果同样是多拦。
func TestGuardMaxVolumeFailsSafe(t *testing.T) {
	g := Guard{AllowOrder: true, MaxVolume: 1}
	req := func(off Offset, vol int) OrderReq {
		return OrderReq{Exchange: "SHFE", Instrument: "rb2701",
			Direction: Buy, Offset: off, Volume: vol, LimitPrice: 3000}
	}

	// ① 对照组：上限本身有判别力。这一条不通过，下面几条什么都不说明。
	if err := g.Check(req(Open, 99)); err == nil {
		t.Fatal("⚠️ 开仓 99 手没被上限拦下 —— 上限根本没生效，后面的用例全部失去意义")
	}
	if err := g.Check(req(Open, 1)); err != nil {
		t.Fatalf("⚠️ 开仓 1 手（未超限）被拦：%v —— 上限拦得过宽", err)
	}

	// ② 平仓不受上限约束：这是那次故障的修复本身。
	// ⚠️ 确切条数，不是下界。评审在别处实测过：一个「至少 N 条」的断言
	// 对「从 N+1 条删到 N 条」毫无判别力，而那才是判据被削弱的实际路径——
	// 没人会一次删空，只会一次删一条。加了 CloseYesterday 就同步改这个数。
	closeOffsets := []Offset{Close, CloseToday}
	if len(closeOffsets) != 2 {
		t.Fatalf("已识别的平仓标志有 %d 个，应为 2 —— 增删了就同步改这个数", len(closeOffsets))
	}
	for _, off := range closeOffsets {
		if err := g.Check(req(off, 99)); err != nil {
			t.Errorf("⚠️ %s 99 手被上限拦下：%v —— 平仓只会缩小敞口，不该受开仓上限约束", off, err)
		}
	}

	// ③ **失败方向**：未设置 / 拼错的 Offset 必须被拦。
	//
	// ⚠️ 这三条是本测试真正的目的。它们在旧判据下**全部放行**。
	badOffsets := []Offset{
		"",            // 零值：调用方忘了设
		"BUYOPEN",     // 笔误
		"open",        // 大小写错
		"CLOSE_TODAY", // 猜错了写法
	}
	if len(badOffsets) != 4 {
		t.Fatalf("未识别的开平标志样本有 %d 个，应为 4 —— 删空了本条会空转通过", len(badOffsets))
	}
	for _, off := range badOffsets {
		if err := g.Check(req(off, 99)); err == nil {
			t.Errorf("⚠️ Offset=%q 的 99 手委托**被放行** —— "+
				"安全阀对未识别的开平标志失效，失败方向朝着「不拦」", off)
		}
	}
}
