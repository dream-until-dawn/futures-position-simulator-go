package kq

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
)

// TestGuardMapsDirectionToPositionSide 钉住 DIFF 这一侧的**映射**。
//
// # 为什么这条留在 kq，而判断逻辑搬去了 safety
//
// 「平掉空头持仓要发 BUY」是 **DIFF 的语义**，不是通用真理 ——
// CTP 那一侧会有它自己的一份映射，而**两份长得不一样正是要被看见的事**。
// 所以映射写在协议包里、测在协议包里。
//
// ⚠️ 这一步写反的话安全阀会**掉个个**：放过真正危险的那一笔，
// 转去拦一笔无关的，而两种表现都不像 bug。
func TestGuardMapsDirectionToPositionSide(t *testing.T) {
	cases := []struct {
		name string
		req  OrderReq
		want safety.Side
	}{
		{"BUY/CLOSE 平的是**空**头", OrderReq{Direction: Buy, Offset: Close}, safety.Short},
		{"BUY/CLOSETODAY 平的是**空**头", OrderReq{Direction: Buy, Offset: CloseToday}, safety.Short},
		{"SELL/CLOSE 平的是**多**头", OrderReq{Direction: Sell, Offset: Close}, safety.Long},
		{"SELL/CLOSETODAY 平的是**多**头", OrderReq{Direction: Sell, Offset: CloseToday}, safety.Long},
		// ⚠️ 开仓不平任何东西：ClosesSide 必须是零值，否则受保护腿会误命中。
		{"BUY/OPEN 不平任何东西", OrderReq{Direction: Buy, Offset: Open}, safety.SideUnknown},
		{"SELL/OPEN 不平任何东西", OrderReq{Direction: Sell, Offset: Open}, safety.SideUnknown},
	}
	for _, c := range cases {
		got := intentOf(c.req)
		if got.ClosesSide != c.want {
			t.Errorf("⚠️ %s：映射成了 %v，应当是 %v", c.name, got.ClosesSide, c.want)
		}
		if wantClosing := c.want != safety.SideUnknown; got.Closing != wantClosing {
			t.Errorf("⚠️ %s：Closing = %v，应当是 %v —— "+
				"⚠️ 判据必须是「**是不是已识别的平仓**」而不是「是不是开仓」："+
				"Offset 的零值是空串，按后者写会让任何拼错的开平标志绕过手数上限",
				c.name, got.Closing, wantClosing)
		}
	}
}

// TestGuardEndToEndBlocksTheSeed 是**接头**的端到端断言。
//
// ⚠️ 上面那条只验映射、safety 那几条只验判断 —— 两边都全绿仍然说明不了
// 「这个接头接上了」。这一条从 Guard.Check 出发，一路走到拒绝。
func TestGuardEndToEndBlocksTheSeed(t *testing.T) {
	g := Guard{AllowOrder: true, MaxVolume: 1, TradingDay: "20260909",
		Protected: []safety.ProtectedLeg{{
			Symbol: "SHFE.rb2701", Side: safety.Short,
			TradingDay: "20260909", Why: "过夜种子"}}}
	err := g.Check(OrderReq{Exchange: "SHFE", Instrument: "rb2701",
		Direction: Buy, Offset: CloseToday, Volume: 1, LimitPrice: 3000})
	if err == nil {
		t.Fatal("⚠️ BUY/CLOSETODAY 会平掉受保护的空头腿，Guard 却放行了")
	}
	if !strings.Contains(err.Error(), "受保护的持仓腿") {
		t.Fatalf("⚠️ 拦是拦下了，但不是被那条守卫拦的：%v", err)
	}
	// 反向：SELL 平多头，与那条腿无关，必须放行。
	if err := g.Check(OrderReq{Exchange: "SHFE", Instrument: "rb2701",
		Direction: Sell, Offset: CloseToday, Volume: 1, LimitPrice: 3000}); err != nil {
		t.Fatalf("⚠️ SELL 平的是多头，与受保护的空头腿无关，却被拦下了 —— "+
			"方向大概映射反了：%v", err)
	}
}
