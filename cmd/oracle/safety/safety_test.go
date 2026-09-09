package safety

import (
	"strings"
	"testing"
)

// seedLeg 是这一组用例共用的受保护腿：SHFE.rb2701 的**空**头。
var seedLeg = ProtectedLeg{
	Symbol: "SHFE.rb2701", Side: Short, TradingDay: "20260909", Why: "过夜种子",
}

func closing(sym string, side Side) Intent {
	return Intent{Symbol: sym, Closing: true, ClosesSide: side,
		Volume: 1, LimitPrice: 3000, Desc: "测试用"}
}

func valve(day string) Valve {
	return Valve{AllowOrder: true, MaxVolume: 1,
		Protected: []ProtectedLeg{seedLeg}, TradingDay: day}
}

// TestBlocksClosingTheProtectedSide 是正面用例。
func TestBlocksClosingTheProtectedSide(t *testing.T) {
	err := valve("20260909").Check(closing("SHFE.rb2701", Short))
	if err == nil {
		t.Fatal("⚠️ 平掉受保护的空头腿，安全阀却放行了 —— 过夜种子平掉就要再等一个交易日")
	}
	if !strings.Contains(err.Error(), "受保护的持仓腿") || !strings.Contains(err.Error(), "过夜种子") {
		t.Errorf("⚠️ 拦是拦下了，但错误里没说清拦的是什么、为什么：%v", err)
	}
}

// TestDoesNotBlockTheOppositeSide 钉住「只拦被保护的那个方向」。
//
// ⚠️ 这里用的是**持仓**方向，与协议无关。「BUY 平的是空头」那一步的取反
// 在 kq 侧（见 TestGuardMapsBuyCloseToShortSide）—— 两件事分开测，
// 因为它们**会各自单独错**。
func TestDoesNotBlockTheOppositeSide(t *testing.T) {
	if err := valve("20260909").Check(closing("SHFE.rb2701", Long)); err != nil {
		t.Fatalf("⚠️ 平的是多头，与受保护的空头腿无关，却被拦下了：%v", err)
	}
}

// TestIgnoresOpeningAndOtherSymbols 划出这条守卫**不该**碰的范围。
func TestIgnoresOpeningAndOtherSymbols(t *testing.T) {
	open := Intent{Symbol: "SHFE.rb2701", Closing: false, Volume: 1, LimitPrice: 3000, Desc: "开仓"}
	if err := valve("20260909").Check(open); err != nil {
		t.Errorf("⚠️ 开仓不该被受保护腿拦下：%v", err)
	}
	if err := valve("20260909").Check(closing("SHFE.rb2705", Short)); err != nil {
		t.Errorf("⚠️ 另一个合约不该被拦：%v", err)
	}
}

// TestProtectionExpiresWithTradingDay 钉住到期。
//
// ⚠️ 一条永久保护会在种子早已用掉之后继续拦着收尾平仓 ——
// 与 MaxVolume 当初拦住收尾平仓是同一个故障：
// **一个用来防止扩大风险的守卫，反过来阻止了缩小风险。**
func TestProtectionExpiresWithTradingDay(t *testing.T) {
	if err := valve("20260910").Check(closing("SHFE.rb2701", Short)); err != nil {
		t.Fatalf("⚠️ 交易日已经是 20260910，只在 20260909 生效的保护仍然在拦 —— "+
			"它现在拦的是正当的收尾平仓：%v", err)
	}
}

// TestBlocksWhenTradingDayUnknown 钉住**失败方向**。
//
// ⚠️ 反过来写（不知道就放行）读起来同样自然，而它在真账户上是这样发生的：
// 连上柜台、截面还没到、收尾平仓先跑了 —— 种子当场没。
func TestBlocksWhenTradingDayUnknown(t *testing.T) {
	if err := valve("").Check(closing("SHFE.rb2701", Short)); err == nil {
		t.Fatal("⚠️ 交易日未知时安全阀放行了 —— 这条判断的失败方向必须朝着" +
			"「多拦一次」：多拦会立刻被看见，少拦一次种子就没了")
	}
}

// TestMaxVolumeLetsClosingThrough 钉住那次真实故障的修法。
//
// 一次实验意外建到 2 手，收尾平仓要发 2 手的单，被 MaxVolume=1 挡下 ——
// **账上留着仓，平不掉**。平仓只会让敞口变小或归零，不该受开仓上限约束。
func TestMaxVolumeLetsClosingThrough(t *testing.T) {
	v := Valve{AllowOrder: true, MaxVolume: 1, TradingDay: "20260909"}
	two := Intent{Symbol: "SHFE.rb2701", Closing: true, ClosesSide: Long,
		Volume: 2, LimitPrice: 3000, Desc: "收尾平仓 2 手"}
	if err := v.Check(two); err != nil {
		t.Errorf("⚠️ 2 手**平仓**被上限挡下了 —— 平仓只会让敞口变小，"+
			"一个防止扩大风险的守卫不该阻止缩小风险：%v", err)
	}
	twoOpen := two
	twoOpen.Closing, twoOpen.Desc = false, "开仓 2 手"
	if err := v.Check(twoOpen); err == nil {
		t.Error("⚠️ 2 手**开仓**没有被上限挡下 —— 那才是这个上限存在的理由")
	}
}

// TestUnknownSideMatchesNothing 钉住零值。
//
// ⚠️ Side 的零值不是 Long 也不是 Short：一个「默认拦多头」的零值，
// 会让忘了填方向的声明**看起来在保护什么**，而它保护的是随机的一边。
func TestUnknownSideMatchesNothing(t *testing.T) {
	v := Valve{AllowOrder: true, MaxVolume: 1, TradingDay: "20260909",
		Protected: []ProtectedLeg{{Symbol: "SHFE.rb2701", TradingDay: "20260909", Why: "忘了填方向"}}}
	for _, side := range []Side{Long, Short} {
		if err := v.Check(closing("SHFE.rb2701", side)); err != nil {
			t.Errorf("⚠️ 那条声明没填 Side（零值），却拦下了平 %s —— "+
				"零值应当谁也不匹配，否则它保护的是随机的一边：%v", side, err)
		}
	}
}
