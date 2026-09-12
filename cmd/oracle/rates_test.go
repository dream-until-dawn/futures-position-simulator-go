package main

import (
	"strings"
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

// TestInvestorRangeNamesEveryValueAndRefusesToGuess 钉住那一列**不会留空**。
//
// ⚠️ 它守的是本批里我自己找出来的那个洞：§13 第 19 条断言「柜台声明的每手是 0，
// 而行为是 0.005 ⇒ 声明不完整」，**而当时有一个没排除的替代解释** ——
// 我读到的可能不是实际适用的那条费率记录（CTP 的记录带 `InvestorRange`）。
//
//	⚠️ 那一轮的原始数据里**根本没有能排除它的信息**：
//	这个字段既没被打印、也没落盘。
//
// ⇒ 现在它必须出现在每一行上，而**认不得的取值要原样报出来** ——
// 留空与「柜台给了一个我没见过的取值」在这一列上分不开。
func TestInvestorRangeNamesEveryValueAndRefusesToGuess(t *testing.T) {
	mk := func(rng byte, investor string) *def.CThostFtdcInstrumentCommissionRateField {
		r := &def.CThostFtdcInstrumentCommissionRateField{
			InvestorRange: def.TThostFtdcInvestorRangeType(rng),
		}
		copy(r.InvestorID[:], investor)
		return r
	}
	for _, c := range []struct {
		rng  byte
		want string
	}{
		{'1', "所有/无投资者代码"},
		{'2', "投资者组/无投资者代码"},
		{'3', "单一投资者/无投资者代码"},
	} {
		if got := investorRange(mk(c.rng, "")); got != c.want {
			t.Errorf("InvestorRange %q ⇒ %q，要的是 %q", string(rune(c.rng)), got, c.want)
		}
	}
	// ⚠️ 认不得的取值：**不许留空，也不许悄悄归到某一档**。
	got := investorRange(mk('9', ""))
	if got == "" || !strings.Contains(got, "未知取值") {
		t.Errorf("认不得的取值给出 %q —— 它必须自报「未知」，"+
			"否则一个没见过的取值会伪装成三档之一", got)
	}
	// ⚠️ 「带没带投资者代码」这一个比特要真的被读到 ——
	// 而**不能**把投资者代码本身打出来：那是凭据一类的值。
	withID := investorRange(mk('3', "12345678"))
	if !strings.Contains(withID, "带投资者代码") {
		t.Errorf("带了投资者代码却报 %q", withID)
	}
	if strings.Contains(withID, "12345678") {
		t.Fatalf("⚠️⚠️ **投资者代码被打进了这一列**：%q —— 它会进日志、进留底文件", withID)
	}
}
