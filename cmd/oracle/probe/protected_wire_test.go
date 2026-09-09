package probe

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
)

// TestGuardCarriesProtectedLegs 是一条**接线测试**。
//
// # 为什么单独写一条
//
// kq 那边已经有五条用例钉住 ProtectedLeg 的行为，它们全绿也说明不了
// 这个包**用上了**它 —— 而这正是这个包刚栽过的跟头：
//
//	decideFallback 抽成了纯函数、配了 7 条用例 2 条不变式 3 次破坏验证，
//	**然后忘了接线**。守卫仍内联在 flatten 里，纯函数全仓没有非测试调用者。
//	评审把内联守卫换成 `if false`，**全库仍然全绿**。
//
// ⚠️ **接线那一步在被省略时是不可见的**：不报错、不告警，`unused` 也不报，
// 因为测试用了它。所以它需要一条**从 r.guard() 出发**的端到端断言。
//
// ⚠️ 刻意不去比 g.Protected 与 protectedLegs 相等：那样只证明字段被赋了值。
// 这里一路走到 Check，断言那一笔**真的被拦下来**。
func TestGuardCarriesProtectedLegs(t *testing.T) {
	if len(protectedLegs) == 0 {
		t.Skip("protectedLegs 是空的 —— 没有需要保护的腿时这条测试无事可做")
	}
	leg := protectedLegs[0]

	r := &Runner{Env: Env{AllowOrder: true, MaxVolume: 1}}
	g := r.guard()

	// 平一条 Sell 腿要发 Buy 委托，反之亦然。
	closeDir := kq.Sell
	if leg.Side == safety.Short {
		closeDir = kq.Buy
	}
	ex, inst := splitSymbol(leg.Symbol)
	err := g.Check(kq.OrderReq{Exchange: ex, Instrument: inst,
		Direction: closeDir, Offset: kq.CloseToday, Volume: 1, LimitPrice: 3000})
	if err == nil {
		t.Fatalf("⚠️ r.guard() 造出来的安全阀**没有拦下** %s %s 的平仓委托 —— "+
			"protectedLegs 声明了它，但那份声明没有接到下单口上。"+
			"⚠️ 这与「守卫写错了」不是一回事：这里守卫是对的，只是没人调用它",
			leg.Symbol, leg.Side)
	}
	if !strings.Contains(err.Error(), "受保护的持仓腿") {
		t.Fatalf("⚠️ 拦是拦下了，但不是被这条守卫拦的：%v", err)
	}
	t.Logf("受保护的腿 %d 条，第一条 %s %s（交易日 %s）",
		len(protectedLegs), leg.Symbol, leg.Side, leg.TradingDay)
}

// TestProtectedLegsAreWellFormed 不让一条半成品声明混进来。
//
// ⚠️ 缺 TradingDay 的那一条是**永久保护**，它会在种子用掉之后
// 继续拦正当的收尾平仓；缺 Why 的那一条，拦下时的错误说不清该怎么办 ——
// 而看到那个错误的人多半正急着把仓平掉。
func TestProtectedLegsAreWellFormed(t *testing.T) {
	for i, l := range protectedLegs {
		switch {
		case l.Symbol == "":
			t.Errorf("第 %d 条没有 Symbol", i)
		case l.Side != safety.Long && l.Side != safety.Short:
			t.Errorf("第 %d 条的 Direction 是 %q，只能是 BUY 或 SELL —— "+
				"⚠️ 零值 \"\" 会让它谁也拦不住，而声明看起来是写了的", i, l.Side)
		case l.TradingDay == "":
			t.Errorf("第 %d 条（%s）没有 TradingDay —— **没有到期日的保护是永久保护**，"+
				"它迟早会拦住正当的收尾平仓", i, l.Symbol)
		case len(l.Why) < 20:
			t.Errorf("第 %d 条（%s）的 Why 太短：%q —— 它会原样出现在拦下时的错误里，"+
				"要让人当场判断得了「该不该划掉这条」", i, l.Symbol, l.Why)
		}
	}
}
