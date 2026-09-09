package ctp

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
	def "gitee.com/haifengat/goctp/ctpdefine"
)

// TestIntentMapsDirectionAndOffset 钉住 CTP 这一侧的映射。
//
// ⚠️ 它与 kq 那侧那条**不是重复**，而这正是不共用测试的理由：
//
//	Buy  = '0'   Open       = '0'   ← **同一个字符**，不同字段
//	Sell = '1'   Close      = '1'   ← 又是同一个字符
//
// 一处「读错了字段」的 bug 会在**部分组合上恰好正确** ——
// 比如把 Offset 当 Direction 读，BUY/OPEN 与 SELL/CLOSE 都还「对」。
// 所以下面必须把四个组合都列出来，而不是抽样。
func TestIntentMapsDirectionAndOffset(t *testing.T) {
	cases := []struct {
		name    string
		dir     def.TThostFtdcDirectionType
		off     def.TThostFtdcOffsetFlagType
		closing bool
		side    safety.Side
	}{
		{"买开 → 不平任何东西", def.THOST_FTDC_D_Buy, def.THOST_FTDC_OF_Open, false, safety.SideUnknown},
		{"卖开 → 不平任何东西", def.THOST_FTDC_D_Sell, def.THOST_FTDC_OF_Open, false, safety.SideUnknown},
		{"买平 → 平**空**头", def.THOST_FTDC_D_Buy, def.THOST_FTDC_OF_Close, true, safety.Short},
		{"卖平 → 平**多**头", def.THOST_FTDC_D_Sell, def.THOST_FTDC_OF_Close, true, safety.Long},
		{"买平今 → 平**空**头", def.THOST_FTDC_D_Buy, def.THOST_FTDC_OF_CloseToday, true, safety.Short},
		{"卖平今 → 平**多**头", def.THOST_FTDC_D_Sell, def.THOST_FTDC_OF_CloseToday, true, safety.Long},
		// ⚠️ CloseYesterday 是 CTP **比 DIFF 多出来的**一档。
		// kq 那侧的注释早写过「将来加 CloseYesterday 而漏改这里」——
		// 那一天到了，而且是在另一个协议上到的。
		{"买平昨 → 平**空**头", def.THOST_FTDC_D_Buy, def.THOST_FTDC_OF_CloseYesterday, true, safety.Short},
		{"卖平昨 → 平**多**头", def.THOST_FTDC_D_Sell, def.THOST_FTDC_OF_CloseYesterday, true, safety.Long},
	}
	for _, c := range cases {
		in := intentOf(OrderReq{Exchange: "SHFE", Instrument: "rb2701",
			Direction: c.dir, Offset: c.off, Volume: 1, LimitPrice: 3000})
		if in.Closing != c.closing {
			t.Errorf("⚠️ %s：Closing = %v，应当是 %v —— "+
				"⚠️ 判据必须是「**是不是已识别的平仓**」而不是「是不是开仓」："+
				"后者会让任何未设置或拼错的开平标志绕过手数上限", c.name, in.Closing, c.closing)
		}
		if in.ClosesSide != c.side {
			t.Errorf("⚠️ %s：ClosesSide = %v，应当是 %v", c.name, in.ClosesSide, c.side)
		}
	}
}

// TestUnknownOffsetIsTreatedAsOpening 钉住**失败方向**。
//
// ⚠️ 一个没见过的开平标志（零值、拼错、CTP 将来新加的）必须按**开仓**对待，
// 于是它受手数上限约束 —— 后果是「多拦一次」，会立刻被看见。
// 反过来（当成平仓放过上限）在真账户上是**扩大敞口**。
func TestUnknownOffsetIsTreatedAsOpening(t *testing.T) {
	for _, off := range []def.TThostFtdcOffsetFlagType{0, '9', 'x'} {
		in := intentOf(OrderReq{Exchange: "SHFE", Instrument: "rb2701",
			Direction: def.THOST_FTDC_D_Buy, Offset: off, Volume: 5, LimitPrice: 3000})
		if in.Closing {
			t.Errorf("⚠️ 开平标志 %q 没见过，却被当成了平仓 —— "+
				"那会让它绕过手数上限，而绕过的方向是**扩大敞口**", string(off))
		}
	}
}

// TestCheckBlocksProtectedLegAndOversizeOpen 是安全阀在 CTP 侧的**接头**断言。
//
// ⚠️ safety 那几条只验判断、上面两条只验映射 —— 两边都全绿仍然说明不了
// 「这个接头接上了」。这一条从 Client.Check 出发，一路走到拒绝。
func TestCheckBlocksProtectedLegAndOversizeOpen(t *testing.T) {
	c := New(Credentials{BrokerID: "9999", UserID: "x"}, nil)
	c.Valve = safety.Valve{AllowOrder: true, MaxVolume: 1,
		Protected: []safety.ProtectedLeg{{
			Symbol: "SHFE.rb2701", Side: safety.Short,
			TradingDay: "", Why: "测试用的受保护腿"}}}

	// ⚠️ TradingDay 为空 —— 未登录时安全阀必须**照拦**，见 safety.blocks。
	err := c.Check(OrderReq{Exchange: "SHFE", Instrument: "rb2701",
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: 3000})
	if err == nil {
		t.Fatal("⚠️ 买平今会平掉受保护的空头腿，Check 却放行了")
	}
	if !strings.Contains(err.Error(), "受保护的持仓腿") {
		t.Fatalf("⚠️ 拦是拦下了，但不是被那条守卫拦的：%v", err)
	}

	// 反向：卖平多头，与那条腿无关，放行。
	if err := c.Check(OrderReq{Exchange: "SHFE", Instrument: "rb2701",
		Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: 3000}); err != nil {
		t.Fatalf("⚠️ 卖平的是多头，与受保护的空头腿无关，却被拦下了 —— "+
			"方向大概映射反了：%v", err)
	}

	// 超量**开仓**要被上限拦下。
	if err := c.Check(OrderReq{Exchange: "SHFE", Instrument: "rb2705",
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 5, LimitPrice: 3000}); err == nil {
		t.Error("⚠️ 5 手开仓没有被 MaxVolume=1 拦下")
	}
	// 而超量**平仓**要放行（那次「账上留着仓平不掉」的故障）。
	if err := c.Check(OrderReq{Exchange: "SHFE", Instrument: "rb2705",
		Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_Close,
		Volume: 5, LimitPrice: 3000}); err != nil {
		t.Errorf("⚠️ 5 手**平仓**被上限挡下了 —— 平仓只会让敞口变小：%v", err)
	}
}

// TestAllowOrderOffBlocksEverything 钉住总闸。
//
// ⚠️ P3 之前 CTP 侧一笔单都不该发得出去，而**保证这件事的不是「我记得没调」**，
// 是 PROBE_ALLOW_ORDER 这道总闸。
func TestAllowOrderOffBlocksEverything(t *testing.T) {
	c := New(Credentials{}, nil)
	c.Valve = safety.Valve{AllowOrder: false, MaxVolume: 1}
	if err := c.Check(OrderReq{Exchange: "SHFE", Instrument: "rb2701",
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: 3000}); err == nil {
		t.Fatal("⚠️ AllowOrder 为假时仍然放行了")
	}
}
