package order

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestRejectionPriorityMatchesCounter 钉住**实测出来的**拒绝优先级。
//
// # 它为什么必须存在
//
// 20260909 我把 CheckPriceLimit 与 CheckPriceTick 的次序整个对调，
// ⚠️ **全套测试照样绿**。八项的优先级此前**从来没有被任何一条测试钉过** ——
// 而 Validate 只报一个拒因，顺序错了两边仍然都判「拒绝」，
// 差异不会以失败的形式出现。这正是本仓库反复栽的那一类。
//
// # 每一条都对应一次真实观测
//
// 期望值不是从 cn-futures-rules.md §9 那张**文档**表抄的 —— 那张表在第一对
// 上就是错的。期望值来自 20260909 在快期模拟 SHFE.ag2702 上的实测，
// 原始夹具 testdata/probes/exp-reject-priority-20260909.json，
// 复跑见 docs/probes.md。
//
// ⚠️ 边界：**一个口子、一个合约、一家交易所**，且只覆盖了 28 对里的 5 对。
// 其余 23 对的顺序至今是猜的 —— 这条测试盖不住它们，别把绿当成「顺序对了」。
func TestRejectionPriorityMatchesCounter(t *testing.T) {
	// rb2701：昨结 3163，涨跌幅 5%，向下取整 ⇒ 涨停 3321、跌停 3004，tick=1。
	const (
		overLimitOffTick = "3400.5" // 越涨停 + 不是整数倍
		overLimitOnTick  = "3400"   // 只越涨停
		inLimitOffTick   = "3163.5" // 只不是整数倍
	)
	cases := []struct {
		name string
		req  Request
		want Check
		why  string
	}{
		{"只越涨停", mkReq(types.Buy, types.Open, d(overLimitOnTick), 1),
			CheckPriceLimit, "柜台原话「已撤单报单被拒绝价格超出涨停板」"},
		{"只不是整数倍", mkReq(types.Buy, types.Open, d(inLimitOffTick), 1),
			CheckPriceTick, "柜台原话「下单价格不是价格单位的整倍数」"},
		{"越涨停 + 不是整数倍", mkReq(types.Buy, types.Open, d(overLimitOffTick), 1),
			CheckPriceLimit, "⚠️ 与 §9 的文档表**相反**：柜台报的是涨跌停"},
		{"越涨停 + 超可平量", mkReq(types.Sell, types.CloseYesterday, d(overLimitOnTick), 99),
			CheckPriceLimit, "实测：柜台报涨跌停，不报可平量"},
		{"不是整数倍 + 超可平量", mkReq(types.Sell, types.CloseYesterday, d(inLimitOffTick), 99),
			CheckPriceTick, "实测：柜台报最小变动价位，不报可平量"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fullFacts(t)
			res := Validate(c.req, f)
			if res.Rejected == nil {
				t.Fatalf("⚠️ 这笔单应当被拒（%s），而 Validate 放行了 —— "+
					"用例本身失效了，下面的顺序断言也就什么都不说明", c.why)
			}
			if res.Rejected.Check != c.want {
				t.Errorf("⚠️ 拒因是「%s」，实测柜台报的是「%s」（%s）—— "+
					"两边都判「拒绝」，所以这个差异**只有这条测试会发现**",
					res.Rejected.Check, c.want, c.why)
			}
		})
	}
}

// TestPriorityIsSaidOnce 断言优先级只有**一处**说法。
//
// ⚠️ 它同时写在两个地方：常量的取值顺序（Validate 的排序按数值）
// 与 allChecks 的元素顺序（文档注释说它「按拒绝优先级」）。
// 两处一旦分岔，读代码的人会照 allChecks 理解，而跑起来按常量走。
func TestPriorityIsSaidOnce(t *testing.T) {
	for i := 1; i < len(allChecks); i++ {
		if allChecks[i-1] >= allChecks[i] {
			t.Errorf("⚠️ allChecks 第 %d 项（%s）不比第 %d 项（%s）优先 —— "+
				"注释说它「按拒绝优先级」，而 Validate 排序按常量取值：两处分岔了",
				i-1, allChecks[i-1], i, allChecks[i])
		}
	}
	// ⚠️ 下界用**实际条数**，不是 > 0：删到只剩一项时上面的循环照样绿。
	if len(allChecks) != 8 {
		t.Errorf("allChecks 有 %d 项，应为 8 —— 增删了校验项就要重新想优先级", len(allChecks))
	}
}

// TestPriceLimitBeforePriceTick 单独把那一对钉出来，并写清它的来历。
//
// ⚠️ 单独一条是刻意的：上面的表格里它只是五行之一，
// 而**它是唯一与文档相反的一行**。混在表里，将来有人「按文档修正」把它改回去时，
// 红的会是一行没有名字的表项；单独一条，红的是这条测试的名字。
func TestPriceLimitBeforePriceTick(t *testing.T) {
	if !(CheckPriceLimit < CheckPriceTick) {
		t.Errorf("⚠️ CheckPriceLimit(%d) 应当排在 CheckPriceTick(%d) **前面**。\n"+
			"这与 cn-futures-rules.md §9 的文档表相反，而那是实测的结果：\n"+
			"  20260909 SHFE.ag2702，同时越涨停又不是整数倍的单，\n"+
			"  柜台答「已撤单报单被拒绝价格超出涨停板」\n"+
			"  夹具 testdata/probes/exp-reject-priority-20260909.json\n"+
			"要改回文档顺序，请先拿出比这份夹具更强的证据。",
			int(CheckPriceLimit), int(CheckPriceTick))
	}
}
