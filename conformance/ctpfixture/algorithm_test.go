package ctpfixture

import (
	"fmt"
	"math"
	"testing"
)

// excludedUnrealized 按柜台**声明**的盈亏算法，算出「不计入可用的那部分浮动盈亏」。
//
// # ⚠️ 它为什么存在
//
// 20260911 夜盘，`TestAccountIdentityAgainstCTP` 被一份浮盈为正的夹具推翻，
// 我从**行为**上量出「浮盈不计入可用、浮亏立即扣」，并把 `max(浮盈, 0)` 写死进了断言。
//
// ⚠️ **而柜台早就把这件事声明出来了**：`BrokerParams.Algorithm == "2"`
// （`THOST_FTDC_AG_OnlyLost`，只计浮动亏损）。它**自 20260909 起落在 25 份夹具里**，
// 由 `ctp-params` 一字不差地打印过很多次。
//
//	⚠️ 那条恒等式写于 20260910，与这句声明**矛盾了整整一天** ——
//	而没有任何东西负责把「夹具里的声明」与「断言里的模型」对上。
//	⇒ **一条与已有断言矛盾的声明，就摆在每一份夹具里，而它不会自己站出来。**
//
// ⚠️ 这与 §13 #1 `MarginPriceType "4"` 那次**不是同一种错**，值得并排记：
//
//	#1   声明被**读宽了** —— 它只管今仓，而我读成「对所有持仓都成立」
//	本条 声明**根本没被读** —— 它一直是对的，一直在那里
//
// ⇒ 于是把常数换成一个由声明求值的函数：断言变成
// **「柜台的声明能不能预测它的行为」**，而不是「我量到的那个数对不对」。
// 哪天换一个 `Algorithm` 配置的账户，这条会自己跟上；而认不得的取值一律**报错**。
func excludedUnrealized(algorithm string, positionProfit float64) (float64, error) {
	switch algorithm {
	case "1": // THOST_FTDC_AG_All 全部计算
		return 0, nil
	case "2": // THOST_FTDC_AG_OnlyLost 只计浮动亏损 ⇒ 浮盈被排除
		return math.Max(positionProfit, 0), nil
	case "3": // THOST_FTDC_AG_OnlyGain 只计浮动盈利 ⇒ 浮亏被排除
		return math.Min(positionProfit, 0), nil
	case "4": // THOST_FTDC_AG_None 都不计
		return positionProfit, nil
	}
	// ⚠️ 认不得的取值**不回落到 0**：那等于悄悄假设「全部计算」，
	// 而那正是被推翻的那个模型。没有结论就说没有结论。
	return 0, fmt.Errorf("认不得的盈亏算法 %q —— CTP 只定义了 1/2/3/4", algorithm)
}

// TestIdentityAgreesWithDeclaredAlgorithm 把可用资金恒等式**从声明推出来**，
// 再拿柜台给的 `Available` 去核。
//
// ⚠️ 它与 `TestAccountIdentityAgainstCTP` 不是重复：那一条把
// 「浮盈不计入」当**已知常数**写死，本条把它当**由声明决定的未知**。
// ⇒ 两条同时红时，说明的是不同的事：前者说柜台变了，后者说**声明与行为不一致**。
func TestIdentityAgreesWithDeclaredAlgorithm(t *testing.T) {
	fx := loadCTP(t)
	n, withParams, positives := 0, 0, 0
	seen := map[string]int{}
	for name, f := range fx {
		if len(f.Account) == 0 {
			continue
		}
		n++
		alg, ok := f.BrokerParams["Algorithm"].(string)
		if !ok {
			// ⚠️ 早期夹具可能没有这一段：那是「没拍到」，不是「柜台没声明」。
			continue
		}
		withParams++
		seen[alg]++
		pp := flt(t, f.Account, "PositionProfit")
		excl, err := excludedUnrealized(alg, pp)
		if err != nil {
			t.Errorf("⚠️ %s：%v —— **没有结论**，而悄悄按「全部计算」处理会复活被推翻的那个模型",
				name, err)
			continue
		}
		got := flt(t, f.Account, "Balance") - flt(t, f.Account, "CurrMargin") -
			flt(t, f.Account, "FrozenMargin") - flt(t, f.Account, "FrozenCommission") - excl
		if want := flt(t, f.Account, "Available"); got != want {
			t.Errorf("⚠️ %s：按声明 Algorithm=%q 推出的可用是 %v，柜台给 %v（差 %v，浮盈 %v）"+
				" —— **声明与行为不一致**，那比「我的模型错了」更值得查",
				name, alg, got, want, want-got, pp)
		}
		if pp > 0 {
			positives++
		}
	}
	if withParams < 5 {
		t.Fatalf("⚠️ 只有 %d 份夹具带 broker_params（共 %d 份带账户）—— 本条在空集上跑",
			withParams, n)
	}
	// ⚠️ **判别力，两层。**
	//
	// 一、`Algorithm` 若全是 "1"（全部计算），`excl` 恒为 0，
	//     本条与那条**被推翻的旧恒等式**逐字等价。
	// 二、即便声明是 "2"，只要没有一份夹具浮盈为正，`max(浮盈,0)` 也恒为 0 ——
	//     **同一个盲区从另一侧又回来了。**
	//
	// ⇒ 两层都要点名，而不是只查一层。
	if seen["2"] == 0 {
		t.Errorf("⚠️ 没有一份夹具声明 Algorithm=\"2\" —— 当前在册：%v。"+
			"若全是 \"1\"，本条与被推翻的旧恒等式**逐字等价**", seen)
	}
	if positives == 0 {
		t.Fatalf("⚠️ %d 份夹具里没有一份 PositionProfit > 0 —— "+
			"声明是 \"2\" 也好 \"1\" 也好，排除项都恒为 0，**本条测不出两者的差别**。"+
			"⇒ 去拍一份赢着的截面（`oracle ctp-slices` 造两片今仓即可）", withParams)
	}
}
