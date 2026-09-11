package main

import (
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// TestRateDigitsCountsSignificantDecimals 钉住 `rateDigits` 数的是**有效**小数位。
//
// ⚠️ 它是 #5 那条判据的一半，而 #5 的形状是：
// 「不取整」与「取到三位或更细」**在 rb 的费率结构下给出同一个数** ——
// 那是一个盲区，不是两个，再多测同一个品种也分不开。
// ⇒ 要换一个更细的费率，而「更细」得有个数得出来的定义。
func TestRateDigitsCountsSignificantDecimals(t *testing.T) {
	cases := []struct {
		v    float64
		want int
	}{
		{0.0001, 4},      // rb：门槛上，而 ×10 抵掉一位 ⇒ 仍然分不开
		{0.00012, 5},     // 这种才真的可能分开
		{0.000023, 6},    //
		{0.01, 2},        //
		{0.000100000, 4}, // ⚠️ 末尾的零不算 —— 否则 %.12f 会让每一个费率都报 12 位
		{0, 0},           // 费率为零：不是「无穷细」，是读不出来
		{-1, 0},          //
	}
	for _, c := range cases {
		if got := rateDigits(c.v); got != c.want {
			t.Errorf("rateDigits(%g) = %d，要的是 %d", c.v, got, c.want)
		}
	}
	// ⚠️ 反空转：若实现退化成「一律返回 4」，上面第 2、3、4 条就会红 ——
	// 这里再钉一次「不同输入给出不同输出」，
	// 因为**一个常量实现能让一张只有一种期望值的表全绿**。
	if rateDigits(0.0001) == rateDigits(0.000023) {
		t.Fatal("⚠️ 两个粗细差着两位的费率被数成同一个位数 —— 判据坏了")
	}
}

// TestSameTierComparesAllSixNumbers 钉住「三档同费率」这个判断**六个数全比**。
//
// ⚠️ §13 #8 实测到的是「开仓档 = 平昨档 = 平今档」，而那是**两项都同**
// （按额与每手）。一个只比按额那两个的判据，
// 在「按额同、每手不同」的柜台上会说「一样」——
// **而那种柜台正是这条实验要找的那种**。
func TestSameTierComparesAllSixNumbers(t *testing.T) {
	mk := func(om, ov, cm, cv, tm, tv float64) *def.CThostFtdcInstrumentCommissionRateField {
		return &def.CThostFtdcInstrumentCommissionRateField{
			OpenRatioByMoney:        def.TThostFtdcRatioType(om),
			OpenRatioByVolume:       def.TThostFtdcRatioType(ov),
			CloseRatioByMoney:       def.TThostFtdcRatioType(cm),
			CloseRatioByVolume:      def.TThostFtdcRatioType(cv),
			CloseTodayRatioByMoney:  def.TThostFtdcRatioType(tm),
			CloseTodayRatioByVolume: def.TThostFtdcRatioType(tv),
		}
	}
	if !sameTier(mk(1e-4, 5e-3, 1e-4, 5e-3, 1e-4, 5e-3)) {
		t.Error("六个数全同却说三档不同")
	}
	// ⚠️ 六个位置**逐个**破一次：只破一个位置的测试，
	// 与一个只比那个位置的实现，在绿的时候长得一模一样。
	for i, r := range []*def.CThostFtdcInstrumentCommissionRateField{
		mk(9e-9, 5e-3, 1e-4, 5e-3, 1e-4, 5e-3),
		mk(1e-4, 9e-9, 1e-4, 5e-3, 1e-4, 5e-3),
		mk(1e-4, 5e-3, 9e-9, 5e-3, 1e-4, 5e-3),
		mk(1e-4, 5e-3, 1e-4, 9e-9, 1e-4, 5e-3),
		mk(1e-4, 5e-3, 1e-4, 5e-3, 9e-9, 5e-3),
		mk(1e-4, 5e-3, 1e-4, 5e-3, 1e-4, 9e-9),
	} {
		if sameTier(r) {
			t.Errorf("第 %d 个位置被改了却仍报「三档相同」—— 那个位置没被比到", i)
		}
	}
}
