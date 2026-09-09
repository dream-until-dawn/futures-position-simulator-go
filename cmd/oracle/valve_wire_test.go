package main

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
	def "gitee.com/haifengat/goctp/ctpdefine"
)

// TestCTPValveCarriesEnv 是 CTP 侧安全阀的**接线测试**。
//
// # 为什么它必须存在
//
// `ctp.Client.Valve` 的零值是 `AllowOrder=false` —— 失败方向是对的，
// 但那意味着**忘了接线的表现是「一切都发不出去」**，而那看起来像是环境问题，
// 不像是 bug。⚠️ 反过来，如果将来有人把零值改成「放行」，
// 忘了接线就变成**一切都发得出去**，而那不会有任何动静。
//
// ⚠️ 这个模块栽过一模一样的跟头：`probe.decideFallback` 抽成纯函数、
// 7 条用例全绿、**忘了接线**。所以这里从 `ctpValve` 出发，一路走到拒绝。
//
// ⚠️ 它**注入**受保护腿，不依赖 `protectedLegs` 当下是不是空的 ——
// 今天已经三次栽在「一条依赖数据非空的测试，在数据变空那天安静失效」上。
func TestCTPValveCarriesEnv(t *testing.T) {
	env := probe.Env{AllowOrder: true, MaxVolume: 1}
	v := ctpValve(env, []safety.ProtectedLeg{{
		Symbol: "SHFE.rb2701", Side: safety.Short,
		TradingDay: "", Why: "接线测试注入的腿"}})

	c := ctp.New(ctp.Credentials{BrokerID: "9999", UserID: "x"}, nil)
	c.Valve = v

	// ① 受保护腿：买平会平掉空头 → 必须被拦
	err := c.Check(ctp.OrderReq{Exchange: "SHFE", Instrument: "rb2701",
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: 3000})
	if err == nil {
		t.Error("⚠️ 受保护腿没有被拦 —— 清单接到 Valve 上了吗")
	} else if !strings.Contains(err.Error(), "受保护的持仓腿") {
		t.Errorf("⚠️ 拦是拦下了，但不是被那条守卫拦的：%v", err)
	}

	// ② MaxVolume：超量**开仓**要被拦
	if err := c.Check(ctp.OrderReq{Exchange: "SHFE", Instrument: "rb2705",
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 5, LimitPrice: 3000}); err == nil {
		t.Error("⚠️ 5 手开仓没有被 MaxVolume=1 拦下 —— env.MaxVolume 接上了吗")
	}

	// ③ ⚠️ 总闸：AllowOrder=false 时**一切**都要被拦
	c2 := ctp.New(ctp.Credentials{}, nil)
	c2.Valve = ctpValve(probe.Env{AllowOrder: false, MaxVolume: 1}, nil)
	if err := c2.Check(ctp.OrderReq{Exchange: "SHFE", Instrument: "rb2705",
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: 3000}); err == nil {
		t.Error("⚠️ PROBE_ALLOW_ORDER 关着时仍然放行 —— 总闸没接上")
	}

	// ④ 正常单要放行 —— ⚠️ 少了这一条，一个「什么都拦」的实现也能通过上面三条，
	// 而那种实现在真实使用时表现成「柜台不收单」，最难查。
	if err := c.Check(ctp.OrderReq{Exchange: "SHFE", Instrument: "rb2705",
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: 3000}); err != nil {
		t.Errorf("⚠️ 一笔正常的 1 手开仓被拦下了：%v", err)
	}
}
