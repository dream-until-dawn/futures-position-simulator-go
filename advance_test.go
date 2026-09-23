package futsim

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// advSim 是带日历与取整方向的门面，m2701 已 Mark 过昨结算价 3384（涨跌停 3587 / 3181，见 simRules 的注释）。
func advSim(t *testing.T) *Simulator {
	t.Helper()
	s, err := New(submitCfg(t, simDay))
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, "DCE.m2701", "3361", "3384")
	return s
}

func bar(t *testing.T, sym string, from, to string, high, low, close string) Bar {
	t.Helper()
	return Bar{Instrument: simInst(t, sym), TradingDay: simDay,
		Start: wall(t, from), End: wall(t, to), High: dec(high), Low: dec(low), Close: dec(close)}
}

// TestAdvanceMarksCloseAndSkipsMatching 钉住 F9b 的正路：守卫过了之后按 Close 计价，**不撮合**（Filled 恒为空）。
func TestAdvanceMarksCloseAndSkipsMatching(t *testing.T) {
	s := advSim(t)
	if _, err := s.Submit(simDay, wall(t, "2026-09-15 10:00"), req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	// 簿上放一笔**能被触到**的买挂单：F9c 会成交它，F9b 不该动它
	if _, err := s.Place(simDay, wall(t, "2026-09-15 10:00"), "o1", req(t, "DCE.m2701", types.Buy, types.Open, "3370", 1)); err != nil {
		t.Fatal(err)
	}
	before := s.Account().PositionProfit
	got, err := s.Advance(bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:01", "3372", "3358", "3370"))
	if err != nil {
		t.Fatalf("这根 K 线应当推得过去：%v", err)
	}
	if len(got.Filled) != 0 {
		t.Errorf("⚠️ F9b 不撮合，Filled 应为空，得到 %d 笔", len(got.Filled))
	}
	if len(got.Unchecked) != 0 {
		t.Errorf("⚠️ 这根 K 线的涨跌停查得出来，Unchecked 应为空，得到 %+v", got.Unchecked)
	}
	if len(s.Live()) != 1 {
		t.Errorf("⚠️ 挂单被动了：还剩 %d 笔", len(s.Live()))
	}
	// 计价：3360 开的一手多头，按 Close 3370 算持仓盈亏 = (3370 − 3360) × 10 = 100
	if pp := s.Account().PositionProfit; !pp.Equal(dec("100")) {
		t.Errorf("⚠️ 按 Close 计价后持仓盈亏 %s，期望 100（推进前 %s）", pp, before)
	}
}

// TestAdvanceGuardsRefuseAndLeaveNoTrace 逐格钉住 ① 的守卫：报错且状态不动。
func TestAdvanceGuardsRefuseAndLeaveNoTrace(t *testing.T) {
	cases := []struct {
		name string
		bar  func(t *testing.T) Bar
		want string
	}{
		{"交易日不是门面当前的", func(t *testing.T) Bar {
			b := bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:01", "3372", "3358", "3370")
			b.TradingDay = simNext
			return b
		}, "交易日"},
		{"Start = End（不是正时长）", func(t *testing.T) Bar {
			return bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:00", "3372", "3358", "3370")
		}, "不是一段正时长"},
		{"最低价为零", func(t *testing.T) Bar {
			return bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:01", "3372", "0", "3370")
		}, "不为正"},
		{"收盘价越出高低", func(t *testing.T) Bar {
			return bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:01", "3372", "3358", "3380")
		}, "低 ≤ 收 ≤ 高"},
		{"跨两个时段（10:14–10:31 越过 10:15 的休息）", func(t *testing.T) Bar {
			return bar(t, "DCE.m2701", "2026-09-15 10:14", "2026-09-15 10:31", "3372", "3358", "3370")
		}, "跨了两个时段"},
		{"起点在时段外（10:20 休息中）", func(t *testing.T) Bar {
			return bar(t, "DCE.m2701", "2026-09-15 10:20", "2026-09-15 10:31", "3372", "3358", "3370")
		}, "不落在"},
		{"高价越涨停（3587）", func(t *testing.T) Bar {
			return bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:01", "3588", "3358", "3370")
		}, "越过当日涨跌停"},
		{"低价越跌停（3181）", func(t *testing.T) Bar {
			return bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:01", "3372", "3180", "3200")
		}, "越过当日涨跌停"},
	}
	for _, c := range cases {
		s := advSim(t)
		before, live := s.Account(), len(s.Live())
		last := s.prices[simInst(t, "DCE.m2701")]
		if _, err := s.Advance(c.bar(t)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("⚠️ %s：要报错（含「%s」），得到 %v", c.name, c.want, err)
			continue
		}
		if !sameSnapshot(s.Account(), before) || len(s.Live()) != live || s.prices[simInst(t, "DCE.m2701")] != last {
			t.Errorf("⚠️ %s：报错之后状态变了", c.name)
		}
	}
}

// TestAdvanceAcceptsLastBarOfSession 钉住评审 20260917 条件 1：End 正好等于时段收盘时刻的那一根**不该**被拒
// —— 那是每个时段的最后一根（10:15 / 11:30 / 15:00 / 23:00）。
func TestAdvanceAcceptsLastBarOfSession(t *testing.T) {
	for _, c := range []struct{ name, from, to string }{
		{"上午第一段最后一根", "2026-09-15 10:14", "2026-09-15 10:15"},
		{"上午第二段最后一根", "2026-09-15 11:29", "2026-09-15 11:30"},
		{"下午最后一根", "2026-09-15 14:59", "2026-09-15 15:00"},
	} {
		s := advSim(t)
		if _, err := s.Advance(bar(t, "DCE.m2701", c.from, c.to, "3372", "3358", "3370")); err != nil {
			t.Errorf("⚠️ %s（%s–%s）被拒了：%v —— End 是右开的，判的该是它的前一瞬", c.name, c.from, c.to, err)
		}
	}
}

// TestAdvanceNightBarBelongsToNextTradingDay：夜盘那一根属于**下一个**交易日；跨零点的后半段仍是同一场。
func TestAdvanceNightBarBelongsToNextTradingDay(t *testing.T) {
	s := advSim(t)
	// 20260915 夜盘（21:00–23:00）属于交易日 20260916：门面还停在 20260915 ⇒ 交易日对不上，报错
	if _, err := s.Advance(bar(t, "DCE.m2701", "2026-09-15 21:00", "2026-09-15 21:01", "3372", "3358", "3370")); err == nil ||
		!strings.Contains(err.Error(), "按日历属于交易日 20260916") {
		t.Errorf("⚠️ 夜盘那一根应当被判给交易日 20260916：%v", err)
	}
	// 结算过去之后同一根就能推进
	if err := s.Settle(simDay, settlePx(t, "DCE.m2701", "3370"), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, "DCE.m2701", "3370", "3370")
	b := bar(t, "DCE.m2701", "2026-09-15 21:00", "2026-09-15 21:01", "3372", "3358", "3370")
	b.TradingDay = simNext
	if _, err := s.Advance(b); err != nil {
		t.Errorf("⚠️ 结算到 20260916 之后，20260915 夜盘那一根应当推得过去：%v", err)
	}
}

// TestAdvanceSkipsPriceLimitWhenUnknowable：算不出涨跌停 ⇒ 跳过那一项、记进 Unchecked、**不报错**
// （报错会让没配 TickRounding 的调用方完全用不了 Advance）。
func TestAdvanceSkipsPriceLimitWhenUnknowable(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T) *Simulator
		want  string
	}{
		{"没给取整方向", func(t *testing.T) *Simulator {
			cfg := submitCfg(t, simDay)
			cfg.TickRounding = nil
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			mark(t, s, "DCE.m2701", "3361", "3384")
			return s
		}, "取整方向"},
		{"没 Mark 过昨结算价", func(t *testing.T) *Simulator {
			s, err := New(submitCfg(t, simDay))
			if err != nil {
				t.Fatal(err)
			}
			return s
		}, "昨结算价"},
	} {
		s := c.setup(t)
		// ⚠️ 用一根**越了涨停**的 K 线：查得出来时它必报错 ⇒ 这一格确实走的是「跳过」那一支
		got, err := s.Advance(bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:01", "9999", "3358", "3370"))
		if err != nil {
			t.Errorf("⚠️ %s：算不出涨跌停应当跳过而不是报错：%v", c.name, err)
			continue
		}
		found := false
		for _, u := range got.Unchecked {
			if u.Check == order.CheckPriceLimit && strings.Contains(u.Missing, c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("⚠️ %s：涨跌停没落进 Unchecked（缺项要点名「%s」），得到 %+v", c.name, c.want, got.Unchecked)
		}
	}
}

// TestAdvanceWithoutCalendarSkipsSession：没给日历时不判时段（与 Place 同一口径），其余守卫照旧。
func TestAdvanceWithoutCalendarSkipsSession(t *testing.T) {
	cfg := submitCfg(t, simDay)
	cfg.Calendar = nil
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, "DCE.m2701", "3361", "3384")
	// 10:20 在休息中：有日历时会被拒，没日历时不判
	if _, err := s.Advance(bar(t, "DCE.m2701", "2026-09-15 10:20", "2026-09-15 10:31", "3372", "3358", "3370")); err != nil {
		t.Errorf("⚠️ 没给日历时不该判时段：%v", err)
	}
	if _, err := s.Advance(bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:01", "9999", "3358", "3370")); err == nil {
		t.Error("⚠️ 没给日历不影响涨跌停那一项：越涨停的 K 线仍要报错")
	}
}

// TestAdvanceEqualsMarkOnTheBooks 钉住「Advance 不引入新的记账路径」：推一根 K 线的账 ≡ 同一个 Close 上 Mark 一次的账。
func TestAdvanceEqualsMarkOnTheBooks(t *testing.T) {
	build := func() *Simulator {
		s := advSim(t)
		if _, err := s.Submit(simDay, wall(t, "2026-09-15 10:00"), req(t, "DCE.m2701", types.Buy, types.Open, "3360", 2)); err != nil {
			t.Fatal(err)
		}
		return s
	}
	viaAdvance := build()
	if _, err := viaAdvance.Advance(bar(t, "DCE.m2701", "2026-09-15 10:00", "2026-09-15 10:01", "3372", "3358", "3370")); err != nil {
		t.Fatal(err)
	}
	viaMark := build()
	if err := viaMark.Mark(simDay, Quote{Instrument: simInst(t, "DCE.m2701"), Last: dec("3370"), HasLast: true}); err != nil {
		t.Fatal(err)
	}
	if a, b := viaAdvance.Account(), viaMark.Account(); !sameSnapshot(a, b) {
		t.Errorf("⚠️ 推一根 K 线的账与 Mark 一次不同：\nAdvance %+v\nMark    %+v", a, b)
	}
}
