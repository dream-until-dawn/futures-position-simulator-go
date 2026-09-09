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
// safety 那边已经有用例钉住 Valve 的行为，kq 那边钉住映射 ——
// 它们全绿也说明不了这个包**用上了**它们。而这正是这个包栽过的跟头：
// decideFallback 抽成纯函数、配了 7 条用例 2 条不变式 3 次破坏验证，
// **然后忘了接线**；评审把内联守卫换成 `if false`，全库仍然全绿。
//
// ⚠️ **接线那一步在被省略时是不可见的**：不报错、不告警，`unused` 也不报，
// 因为测试用了它。
//
// # ⚠️ 它现在**注入**受保护腿，而不是依赖声明的那份
//
// 上一版直接用包级 protectedLegs，于是清单一空就 t.Skip。
// 20260909 结算后清单真的空了 —— 而那一刻两条钉它的破坏（184/188）
// **同时失去意义**。那说明「清单空了就跳过」不是一个可以将就的盲区，
// 是一个该修的设计：**一条只在有数据时才生效的守卫，
// 恰好在数据最少的时候最需要它。**
func TestGuardCarriesProtectedLegs(t *testing.T) {
	r := &Runner{Env: Env{AllowOrder: true, MaxVolume: 1}}
	r.Protected = []safety.ProtectedLeg{{
		Symbol: "SHFE.rb2701", Side: safety.Short,
		TradingDay: "", Why: "接线测试注入的腿"}}

	g := r.guard()
	ex, inst := splitSymbol("SHFE.rb2701")
	// 平**空**头要发 BUY。
	err := g.Check(kq.OrderReq{Exchange: ex, Instrument: inst,
		Direction: kq.Buy, Offset: kq.CloseToday, Volume: 1, LimitPrice: 3000})
	if err == nil {
		t.Fatal("⚠️ r.guard() 造出来的安全阀**没有拦下**那一笔 —— " +
			"清单声明了它，但那份声明没有接到下单口上。" +
			"⚠️ 这与「守卫写错了」不是一回事：这里守卫是对的，只是没人调用它")
	}
	if !strings.Contains(err.Error(), "受保护的持仓腿") {
		t.Fatalf("⚠️ 拦是拦下了，但不是被那条守卫拦的：%v", err)
	}
}

// TestGuardFallsBackToDeclaredLegs 钉住**没有注入时走哪条路**。
//
// ⚠️ 它与上一条查的不是一回事：上一条注入了腿，因此**根本没走**默认那条路。
// 而生产路径走的正是默认那条 —— 一条只在测试里被走过的分支，
// 与一条没人走的分支在覆盖率上长得一样。
func TestGuardFallsBackToDeclaredLegs(t *testing.T) {
	r := &Runner{Env: Env{AllowOrder: true, MaxVolume: 1}}
	if r.Protected != nil {
		t.Fatal("Runner 的零值里 Protected 应当是 nil")
	}
	got := r.protectedLegs()
	if len(got) != len(protectedLegs) {
		t.Errorf("⚠️ 没有注入时取到 %d 条，而声明的那份有 %d 条 —— "+
			"生产路径拿到的不是声明的那份", len(got), len(protectedLegs))
	}
	// ⚠️ nil 与空切片含义不同：nil = 没注入；空切片 = 注入了一份空的。
	// 混同会让「注入一个空清单」悄悄退回成「用声明的」。
	r.Protected = []safety.ProtectedLeg{}
	if n := len(r.protectedLegs()); n != 0 {
		t.Errorf("⚠️ 注入了一份**空**清单，却取到 %d 条 —— "+
			"nil 与空切片被混同了，于是「明确要求不保护任何东西」"+
			"悄悄退回成了「用声明的那份」", n)
	}
}

// TestProtectedLegsAreWellFormed 不让一条半成品声明混进来。
func TestProtectedLegsAreWellFormed(t *testing.T) {
	for i, l := range protectedLegs {
		switch {
		case l.Symbol == "":
			t.Errorf("第 %d 条没有 Symbol", i)
		case l.Side != safety.Long && l.Side != safety.Short:
			t.Errorf("⚠️ 第 %d 条的 Side 是 %v，只能是多头或空头 —— "+
				"⚠️ 零值会让它谁也拦不住，而声明看起来是写了的", i, l.Side)
		case l.TradingDay == "":
			t.Errorf("⚠️ 第 %d 条（%s）没有 TradingDay —— **没有到期日的保护是永久保护**，"+
				"它迟早会拦住正当的收尾平仓", i, l.Symbol)
		case len(l.Why) < 20:
			t.Errorf("⚠️ 第 %d 条（%s）的 Why 太短：%q —— 它会原样出现在拦下时的错误里",
				i, l.Symbol, l.Why)
		}
	}
	t.Logf("声明的受保护腿 %d 条（20260909 结算后清空，见 exp_overnight.go 的注释）",
		len(protectedLegs))
}
