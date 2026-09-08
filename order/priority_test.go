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
// 期望值来自 20260909 在快期模拟 SHFE.ag2702 与 DCE.i2701 上的实测，
// 原始夹具 testdata/probes/exp-reject-tick-vs-limit-20260909*.json，
// 复跑见 docs/probes.md §14。它与 cn-futures-rules.md §9 的文档表一致。
//
// ⚠️ 中途我据一次实测把前两项对调过，那次是错的：构造「偏离整数倍」用的
// 零头是 0.7 个 tick，而快期**不把它当偏离**（kq_facts 45）——
// 那笔单只违反了一项，「同时违反两项」从一开始就不成立。
//
// ⚠️ 边界：两个合约、两家交易所、一个口子，且只覆盖了 28 对里的 3 对。
// 其余 25 对的顺序至今是猜的 —— 这条测试盖不住它们，别把绿当成「顺序对了」。
func TestRejectionPriorityMatchesCounter(t *testing.T) {
	// rb2701：昨结 3163，涨跌幅 5%，向下取整 ⇒ 涨停 3321、跌停 3004，tick=1。
	// ⚠️ 零头一律取 **1/3 个 tick**，不取 0.5：0.5 个 tick 在快期上
	// **不算偏离**（kq_facts 45），拿它当「同时违反两项」的样本，
	// 那一项从一开始就不成立 —— 这正是本轮翻案的原因。
	const (
		overLimitOffTick = "3400.3333" // 越涨停 + 不是整数倍
		overLimitOnTick  = "3400"      // 只越涨停
		inLimitOffTick   = "3163.3333" // 只不是整数倍
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
			CheckPriceTick, "实测：零头 1/3 个 tick 时柜台报的是「不是整倍数」"},
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

// TestPriceTickBeforePriceLimit 单独把那一对钉出来，并写清它被翻过一次案。
//
// ⚠️ 单独一条是刻意的：混在上面的表里，将来有人再翻一次案时，
// 红的会是一行没有名字的表项；单独一条，红的是这条测试的名字，
// 而这段注释就在旁边。
func TestPriceTickBeforePriceLimit(t *testing.T) {
	if !(CheckPriceTick < CheckPriceLimit) {
		t.Errorf("⚠️ CheckPriceTick(%d) 应当排在 CheckPriceLimit(%d) **前面**。\n"+
			"这一对被翻过一次案，翻回来的理由写在这里，请先读完再改："+
			"  20260909 02:0x 我据一次实测把两项对调，那次是错的。\n"+
			"  构造「偏离整数倍」用的零头是 0.7 个 tick，而快期**不把它当偏离**\n"+
			"  （零头 ≥ 半个 tick 照单全收，kq_facts 45）——\n"+
			"  那笔单只违反了涨跌停一项，对照组从一开始就不成立。\n"+
			"  同一时刻把零头扫成 1/10、1/3、1/2、7/10、9/10 才看出这条边界，\n"+
			"  两个合约两家交易所各一遍：\n"+
			"  testdata/probes/exp-reject-tick-vs-limit-20260909*.json\n"+
			"要再改，请先拿出零头 **< 半个 tick** 的反例。",
			int(CheckPriceTick), int(CheckPriceLimit))
	}
}
