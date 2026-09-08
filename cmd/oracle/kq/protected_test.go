package kq

import "testing"

// seedLeg 是这一组用例共用的受保护腿：SHFE.rb2701 的**空**头。
var seedLeg = ProtectedLeg{
	Symbol: "SHFE.rb2701", Direction: Sell, TradingDay: "20260909",
	Why: "过夜种子",
}

func req(dir Direction, off Offset, inst string) OrderReq {
	return OrderReq{Exchange: "SHFE", Instrument: inst, Direction: dir,
		Offset: off, Volume: 1, LimitPrice: 3000}
}

// TestProtectedLegBlocksTheClosingOrder 是这条守卫的正面用例。
//
// ⚠️ 平掉一个**空**头持仓，发出去的委托方向是 **BUY**。
// 这一条与下一条必须成对读：只有正面用例时，一个「方向不取反」的实现
// 会在这里绿（因为它去拦 SELL/CLOSE），而那正是危险的实现。
func TestProtectedLegBlocksTheClosingOrder(t *testing.T) {
	g := Guard{AllowOrder: true, MaxVolume: 1,
		Protected: []ProtectedLeg{seedLeg}, TradingDay: "20260909"}
	for _, off := range []Offset{Close, CloseToday} {
		err := g.Check(req(Buy, off, "rb2701"))
		if err == nil {
			t.Fatalf("⚠️ BUY/%s 会平掉受保护的空头腿，安全阀却放行了 —— "+
				"过夜种子平掉就要再等一个交易日", off)
		}
		if !contains(err.Error(), "受保护的持仓腿") || !contains(err.Error(), "过夜种子") {
			t.Errorf("⚠️ 拦是拦下了，但错误里没说清拦的是什么、为什么：%v", err)
		}
	}
}

// TestProtectedLegDoesNotBlockTheOppositeSide 钉住方向取反那一步。
//
// SELL/CLOSE* 平的是**多**头。账上那两手多头种子归 seedPlan 管，
// 不在这条保护里 —— 拦住它就是「拦错了人」，而拦错人与不拦
// 在日志里长得都很正常。
func TestProtectedLegDoesNotBlockTheOppositeSide(t *testing.T) {
	g := Guard{AllowOrder: true, MaxVolume: 1,
		Protected: []ProtectedLeg{seedLeg}, TradingDay: "20260909"}
	if err := g.Check(req(Sell, CloseToday, "rb2701")); err != nil {
		t.Fatalf("⚠️ SELL/CLOSETODAY 平的是多头，与受保护的空头腿无关，"+
			"却被拦下了 —— 方向大概写反了：%v", err)
	}
}

// TestProtectedLegIgnoresOpeningAndOtherSymbols 划出这条守卫**不该**碰的范围。
func TestProtectedLegIgnoresOpeningAndOtherSymbols(t *testing.T) {
	g := Guard{AllowOrder: true, MaxVolume: 1,
		Protected: []ProtectedLeg{seedLeg}, TradingDay: "20260909"}
	cases := []struct {
		name string
		r    OrderReq
	}{
		{"开仓不该被拦", req(Buy, Open, "rb2701")},
		{"另一个合约不该被拦", req(Buy, CloseToday, "rb2705")},
	}
	for _, c := range cases {
		if err := g.Check(c.r); err != nil {
			t.Errorf("⚠️ %s：%v", c.name, err)
		}
	}
}

// TestProtectedLegExpiresWithTradingDay 钉住到期。
//
// ⚠️ 保护过了那个交易日必须失效。一条永久保护会在种子早已用掉之后
// 继续拦着收尾平仓 —— 与 MaxVolume 当初拦住收尾平仓是同一个故障：
// **一个用来防止扩大风险的守卫，反过来阻止了缩小风险。**
func TestProtectedLegExpiresWithTradingDay(t *testing.T) {
	g := Guard{AllowOrder: true, MaxVolume: 1,
		Protected: []ProtectedLeg{seedLeg}, TradingDay: "20260910"}
	if err := g.Check(req(Buy, CloseToday, "rb2701")); err != nil {
		t.Fatalf("⚠️ 交易日已经是 20260910，这条只在 20260909 生效的保护"+
			"仍然在拦 —— 它现在拦的是正当的收尾平仓：%v", err)
	}
}

// TestProtectedLegBlocksWhenTradingDayUnknown 钉住**失败方向**。
//
// ⚠️ 交易截面还没回来时 TradingDay 是空串。那一刻的正确行为是**照拦**。
//
// 反过来写（不知道就放行）读起来同样自然，而它在真账户上是这样发生的：
// 连上柜台、截面还没到、收尾平仓先跑了 —— 种子当场没。
// 这个包里已经有过一次一模一样的教训：MaxVolume 那个洞的
// **失败方向朝着「不拦」**，而人在修「阀门太紧」时会本能地往松了调。
func TestProtectedLegBlocksWhenTradingDayUnknown(t *testing.T) {
	g := Guard{AllowOrder: true, MaxVolume: 1,
		Protected: []ProtectedLeg{seedLeg}, TradingDay: ""}
	if err := g.Check(req(Buy, CloseToday, "rb2701")); err == nil {
		t.Fatal("⚠️ 交易日未知时安全阀放行了 —— 这条判断的失败方向必须朝着" +
			"「多拦一次」：多拦会立刻被看见，少拦一次种子就没了")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
