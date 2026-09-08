package probe

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
)

// agQuote 是 20260909 SHFE.ag2702 的真实数字（tick=1）。
func agQuote() (kq.Quote, float64) {
	return kq.Quote{Symbol: "SHFE.ag2702", UpperLimit: 19514, LowerLimit: 13009,
		LastPrice: 16216}, 1
}

func agBase() kq.OrderReq {
	return kq.OrderReq{Exchange: "SHFE", Instrument: "ag2702",
		Direction: kq.Buy, Offset: kq.Open, Volume: 1, LimitPrice: 13009}
}

// TestApplyAllBuildsWhatItClaims 是那条实验的**零层**：违规真的被构造出来了吗。
//
// # 它是一次真实事故的守卫
//
// 20260909 第一版把「越涨停」写成 `r.LimitPrice = q.UpperLimit * 1.05`。
// 它**覆盖**掉了「不是整数倍」刚加上的零头，于是：
//
//	单违规 limit   @20489.7
//	组合 tick+limit @20489.7   ← 同一笔单
//
// 归因变成拿一笔单和它自己比，**必然「相等」**，而实验照样打印出
// 一行「与本库相反」的结论。⚠️ 更隐蔽的是第二处：19514×1.05 = 20489.7
// 本身就不是 tick 的整数倍 —— 那条「只越涨停」的**标尺**从一开始就是脏的。
//
// 两处都不会以失败的形式出现：单子发得出去、柜台照样拒、日志照样有结论。
func TestApplyAllBuildsWhatItClaims(t *testing.T) {
	q, tick := agQuote()
	cases := []struct {
		keys           []string
		wantOff, wantX bool // 期望：不是整数倍 / 越界
	}{
		{[]string{"tick"}, true, false},
		{[]string{"limit"}, false, true},
		{[]string{"tick", "limit"}, true, true},
		{[]string{"close"}, false, false},
		{[]string{"tick", "close"}, true, false},
		{[]string{"limit", "closetoday"}, false, true},
	}
	for _, c := range cases {
		name := strings.Join(c.keys, "+")
		t.Run(name, func(t *testing.T) {
			keys := map[string]bool{}
			for _, k := range c.keys {
				keys[k] = true
			}
			req, err := applyAll(q, tick, agBase(), keys)
			if err != nil {
				t.Fatalf("⚠️ %s 构造失败：%v", name, err)
			}
			if got := offTick(req.LimitPrice, tick); got != c.wantOff {
				t.Errorf("⚠️ %s：价格 %v 是否偏离整数倍 = %v，应为 %v",
					name, req.LimitPrice, got, c.wantOff)
			}
			x := req.LimitPrice > q.UpperLimit || req.LimitPrice < q.LowerLimit
			if x != c.wantX {
				t.Errorf("⚠️ %s：价格 %v 是否越界 = %v，应为 %v",
					name, req.LimitPrice, x, c.wantX)
			}
		})
	}
}

// TestApplyAllRefusesWhenOverwritten 演示覆盖一旦发生就会被当场拒绝。
//
// ⚠️ 它不是把 bug 重写一遍，而是直接喂一个**已经被覆盖过**的价格：
// 声称「同时违反 tick 与 limit」，而实际给的价格是整数倍的越界价。
func TestApplyAllRefusesWhenOverwritten(t *testing.T) {
	q, tick := agQuote()
	base := agBase()
	base.LimitPrice = q.UpperLimit + 10*tick // 越界，但**是**整数倍
	// 只叠加 limit（它是 Abs，会把价格再改写一遍成同一个数），
	// 于是「tick 也成立」这个声称是假的。
	_, err := applyAll(q, tick, base, map[string]bool{"limit": true, "tick": false})
	if err != nil {
		t.Fatalf("这一笔只声称 limit，应当通过：%v", err)
	}
	// 声称 tick 也成立，但没有任何一步去制造零头。
	req := base
	if v, ok := violationOf("limit"); ok {
		v.Apply(q, tick, &req)
	}
	if offTick(req.LimitPrice, tick) {
		t.Fatal("⚠️ 前提不成立：这个价格本来就偏离整数倍，测不出覆盖")
	}
	holds := map[string]bool{"limit": true, "tick": true}
	var bad bool
	for _, v := range violations {
		if v.Holds(q, tick, req) != holds[v.Key] {
			bad = true
		}
	}
	if !bad {
		t.Error("⚠️ 声称 tick+limit 而价格是整数倍的越界价，Holds 竟然全都对上了 —— " +
			"零层失效：覆盖发生了也不会被发现")
	}
}

// TestOffTickToleranceScalesWithTick 钉住容差随 tick 缩放。
//
// ⚠️ 绝对容差会在两端各错一次，而错的方向是「以为构造出了违规」。
func TestOffTickToleranceScalesWithTick(t *testing.T) {
	cases := []struct {
		price, tick float64
		want        bool
	}{
		{13009, 1, false},
		{13009.3333, 1, true},
		{668.5, 0.5, false},     // DCE.i 的 tick
		{668.6667, 0.5, true},   //
		{108330, 10, false},     // SHFE.cu 的 tick
		{108333.3333, 10, true}, //
		{100, 0, false},         // tick 缺失时**不声称违规**
	}
	for _, c := range cases {
		if got := offTick(c.price, c.tick); got != c.want {
			t.Errorf("offTick(%v, %v) = %v，应为 %v", c.price, c.tick, got, c.want)
		}
	}
}

// TestRankComesFromLibrary 断言实验用的优先级是**库里的**，不是手抄的。
//
// ⚠️ 手抄一份，库改了实验不会红 —— 而这条实验的全部意义
// 就是拿库的说法去和柜台对。拿一份抄件去对，对的是抄件。
func TestRankComesFromLibrary(t *testing.T) {
	want := map[string]order.Check{
		"limit":      order.CheckPriceLimit,
		"tick":       order.CheckPriceTick,
		"close":      order.CheckClosable,
		"closetoday": order.CheckClosable,
	}
	if len(violations) != len(want) {
		t.Fatalf("violations 有 %d 项，期望表有 %d 项 —— 增删了就要同步",
			len(violations), len(want))
	}
	for _, v := range violations {
		w, ok := want[v.Key]
		if !ok {
			t.Errorf("多出一项违规 %s", v.Key)
			continue
		}
		if v.Rank != w {
			t.Errorf("⚠️ %s 的 Rank 是 %d，而 order.%s 是 %d", v.Key, v.Rank, v.Check, w)
		}
	}
	// ⚠️ 顺便钉住「Abs 的排在前面」：加零头的必须落在改写价格之后。
	seenNonAbs := false
	for _, v := range violations {
		if !v.Abs {
			seenNonAbs = true
			continue
		}
		if seenNonAbs {
			t.Errorf("⚠️ %s 会改写价格，却排在某个加零头的后面 —— 它会把零头覆盖掉", v.Key)
		}
	}
}

// violationOf 按 Key 取一项违规。
func violationOf(key string) (violation, bool) {
	for _, v := range violations {
		if v.Key == key {
			return v, true
		}
	}
	return violation{}, false
}
