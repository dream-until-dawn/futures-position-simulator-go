package conformance

// CrossDay 是**跨交易日对拍**上已登记的差异归类。
//
// ⚠️ 它此前只活在 `conformance/fixture/crossday_test.go` 里，而两条路要用它：
// 离线对拍（测试）与实时对拍（`oracle conformance`）。
// 只有测试有它的话，实时那条路会把每一处已知差异都报成失败，于是**永远红**。
//
// # ⚠️ 这张表贴错过标签，错法值得记住
//
// 原来有 6 个字段标着「NoUseHistory 不滚今昨」，而它们**全部出现在
// UseHistory 合约上**。真正的 NoUseHistory 合约因为拿不到大商所的结算价
// 被跳过了，所以那一类**一次都没有被观测到** —— 而「每一类都必须出现」
// 那条守卫正是被这些错标签喂饱的。
//
// ⚠️ 让错标签活下来的条件是：**这批字段的观测撑不起任何机制断言**。
// 两边都是 0 时，「柜台不填」「路径依赖」「压根没有这个仓」给出同一个观测。
// 所以凡是**从未取到过非零值**的字段，一律只能是「跨日-?」——
// 那条规则由 `TestCrossDayConformance` 机械地断言，不靠人记得。
func CrossDay() Registry {
	const (
		clsA = "跨日-A"
		clsB = "跨日-B"
		clsC = "跨日-C"
		clsD = "跨日-D"
		clsQ = "跨日-?"
	)
	whyA := "本库的逐日盯市基线用**结算价**（中国期货的资金结算走这一套），" +
		"柜台用**收盘价**。20260908 的 rb2701：结算 3163、收盘 3177，差 14。" +
		"⚠️ 收盘价这一端由**上期所日行情**独立确认，不是柜台自己的数"
	whyB := "NoUseHistory 的合约结算后持仓**留在今仓**，而本库按结算滚今昨。" +
		"⚠️ 本条在跨日对拍里**观测不到**：唯一的 NoUseHistory 合约拿不到" +
		"大商所的结算价（日行情 412 未打通），被跳过了"
	whyC := "**时序**，不是规则差异：账户侧已滚到新交易日，而行情侧仍停在昨天收盘，" +
		"于是柜台的保证金取的是**旧的**昨结算价。21:00 行情滚后消失"
	whyD := "今昨拆分**柜台不填**：open_cost_* 的拆分从来不是真实数字；" +
		"position_cost_* 的拆分只在**结算时**写，写哪一侧由 PositionDateType 定"
	whyQ := "⚠️ **样本区分不了**：这个字段一次都没有取到过非零值。" +
		"两边都是 0 时，几种机制给出同一个观测，此时任何因果类别都是猜的"

	return Registry{
		"position_price_long":    {clsA, whyA, "kq_facts 26/35/37"},
		"position_price_short":   {clsA, whyA, "kq_facts 26/35/37"},
		"position_cost_long":     {clsA, whyA, "kq_facts 26/37"},
		"position_cost_long_his": {clsA, whyA, "kq_facts 37"},
		"position_profit_long":   {clsA, whyA, "kq_facts 36"},
		"position_profit":        {clsA, whyA, "kq_facts 36"},

		"position_cost_long_today": {clsD, whyD, "kq_facts 28/33"},

		"volume_long_today":  {clsB, whyB, "kq_facts 24"},
		"volume_long_his":    {clsB, whyB, "kq_facts 24"},
		"volume_short_today": {clsB, whyB, "kq_facts 24"},

		"margin":      {clsC, whyC, "kq_facts 29/41"},
		"margin_long": {clsC, whyC, "kq_facts 29/41"},

		// —— 以下全部是「样本区分不了」——
		//
		// ⚠️ 空头那几个的根因是：持仓记录里 volume_short_his > 0
		// （20260908 当时 419 条，20260909 涨到 463 条，两次都是）
		// 出现 **0 次**，空头昨仓从来没有存在过。
		// 多头那两个 open_cost_* 也一样从未非零 —— 而它们是**机械断言**
		// 找出来的，我手工分类时漏掉了自己以为最熟的那几个。
		"open_cost_long_his":        {clsQ, whyQ, ""},
		"open_cost_long_today":      {clsQ, whyQ, ""},
		"open_cost_short_his":       {clsQ, whyQ, ""},
		"open_cost_short_today":     {clsQ, whyQ, ""},
		"position_cost_short_his":   {clsQ, whyQ, ""},
		"position_cost_short_today": {clsQ, whyQ, ""},
		"position_cost_short":       {clsQ, whyQ, ""},
		"position_profit_short":     {clsQ, whyQ, ""},
		"volume_short_his":          {clsQ, whyQ, ""},
	}
}

// Live 是**实时对拍**上已登记的差异归类。
//
// 它在 CrossDay 的基础上多两条 —— 那两条只在「同一天之内、持仓被部分平掉」
// 时才出现，跨日对拍碰不到：
//
//	open_price / open_cost   柜台的成本按**持仓均价**冲减，于是平仓后均价不动；
//	                         本库保留逐笔明细，消耗的是具体那一笔
//	volume_*_yd              「实际上是前一交易日开的」与本库的 OpenDay 口径
//
// ⚠️ 第一条是 design.md 决策 10 与柜台实现的分岔，不是算错 ——
// **均价是有损压缩**，柜台丢掉信息之后它的 float_profit 在部分平仓后
// 不再是真正的逐笔对冲口径。见 simnow_pending#10。
func Live() Registry {
	r := CrossDay()
	const clsE = "实时-E"
	whyE := "柜台的 open_cost 按**持仓均价**冲减，于是 open_price 平仓后不动；" +
		"本库保留**逐笔明细**，消耗的是具体那一笔。⚠️ 这是 design.md 决策 10 " +
		"与柜台实现的分岔，不是算错 —— 均价是有损压缩，柜台丢掉信息之后" +
		"它的 float_profit 在部分平仓后不再是真正的逐笔对冲口径"
	for _, k := range []string{
		"open_price_long", "open_price_short",
		"open_cost_long", "open_cost_short",
		"float_profit", "float_profit_long", "float_profit_short",
	} {
		r[k] = KnownClass{clsE, whyE, "simnow_pending#10"}
	}
	const clsF = "实时-F"
	whyF := "`volume_*_yd` 是「实际上是前一交易日开的」，与 `_his`（按平今平昨" +
		"规则算的昨仓）是两套口径。本库的 Lot 同时带 Settled 与 OpenDay，" +
		"而 view 目前从 OpenDay 推 `_yd` —— 结转出来的持仓 OpenDay 是重放出来的，" +
		"与柜台记的原始开仓日不一定一致"
	for _, k := range []string{"volume_long_yd", "volume_short_yd"} {
		r[k] = KnownClass{clsF, whyF, "kq_facts 27"}
	}
	return r
}
