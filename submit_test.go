package futsim

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/ctperr"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// submitCalendar 是一份小日历：交易日 20260915 / 20260916；大商所豆粕的时段照 sessions-20260908.json（日盘三段 + 夜盘 21:00–23:00）。
// ⚠️ 上期所白银**刻意不给时段表** —— 用来走「没查成」那一支。
func submitCalendar(t *testing.T) *refdata.Calendar {
	t.Helper()
	day := []refdata.Session{
		{Start: refdata.MustClockTime(9, 0, 0), End: refdata.MustClockTime(10, 15, 0)},
		{Start: refdata.MustClockTime(10, 30, 0), End: refdata.MustClockTime(11, 30, 0)},
		{Start: refdata.MustClockTime(13, 30, 0), End: refdata.MustClockTime(15, 0, 0)},
	}
	m := refdata.SessionTable{Exchange: types.DCE, Product: "m", Day: day,
		Night: []refdata.Session{{Start: refdata.MustClockTime(21, 0, 0), End: refdata.MustClockTime(23, 0, 0)}}}
	y := m
	y.Product = "y"
	// rb2701（上期所）：给第八项口径的报单路径用，时段形状照抄上面（合成，不是上期所时段表的实测）
	rb := m
	rb.Exchange, rb.Product = types.SHFE, "rb"
	c, err := refdata.NewCalendar([]types.TradingDay{simDay, simNext}, []refdata.SessionTable{m, y, rb}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func wall(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04", s, refdata.CNZone())
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func submitSim(t *testing.T, ch Choices, pre string) *Simulator {
	t.Helper()
	s, err := New(Config{Day: simDay, PreBalance: dec(pre), Rules: simRules(t), Choices: ch,
		Calendar: submitCalendar(t), TickRounding: MeasuredTickRounding(),
		PositionLimits: map[types.InstrumentID]int{simInst(t, "DCE.m2701"): 100, simInst(t, "SHFE.ag2702"): 100, simInst(t, "DCE.y2701"): 100,
			simInst(t, "SHFE.rb2701"): 100}})
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, "DCE.m2701", "3361", "3384")
	mark(t, s, "SHFE.ag2702", "15460", "15785")
	mark(t, s, "DCE.y2701", "8000", "8000")
	return s
}

func req(t *testing.T, sym string, dir types.Direction, off types.Offset, px string, vol int) order.Request {
	return order.Request{Instrument: simInst(t, sym), Direction: dir, Offset: off, Hedge: types.Speculation, Price: dec(px), Volume: vol}
}

// TestSubmitBooksTheSameAsApplyTrade 钉住：报单成交之后的账，与直接灌同一笔成交的账逐字段相同。
func TestSubmitBooksTheSameAsApplyTrade(t *testing.T) {
	a := submitSim(t, ctpChoices(), "100000")
	tr, err := a.Submit(simDay, wall(t, "2026-09-15 10:00"), req(t, "DCE.m2701", types.Buy, types.Open, "3360", 2))
	if err != nil {
		t.Fatalf("时段内、价内、钱够：应当成交：%v", err)
	}
	if !tr.Price.Equal(dec("3360")) || tr.Volume != 2 {
		t.Errorf("裁决：按报价全量成交，得到 %s × %d", tr.Price, tr.Volume)
	}
	b := submitSim(t, ctpChoices(), "100000")
	if err := b.ApplyTrade(simDay, tr); err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(a.Account(), b.Account()) {
		t.Errorf("⚠️ 报单成交与直接灌成交的账不同：\n%+v\n%+v", a.Account(), b.Account())
	}
	if a.Account().Commission.IsZero() {
		t.Error("前提：成交要收手续费，否则上面那条比的是两份空账")
	}
}

// TestSubmitRejectsWithCorpusCodes 在门面组装的事实上逐条造出语料里的拒因，查 RejectedError.Code。
//
// ⚠️ 它核的是「门面喂给 order 的事实让它拒在同一项、给出同一个 Kind」—— 码值本身与 ctperr 的表同源，不算独立。
// 涨跌停：m2701 昨结 3384 × (1 ± 6%) 按大商所四舍五入 = 3587 / 3181；ag2702 昨结 15785 × (1 ± 9%) 按上期所向下 = 17205 / 14364。
func TestSubmitRejectsWithCorpusCodes(t *testing.T) {
	cases := []struct {
		name   string
		r      order.Request
		check  order.Check
		reason ctperr.Reason
	}{
		{"大商所 非整数倍", req(t, "DCE.m2701", types.Buy, types.Open, "3360.5", 1), order.CheckPriceTick, ctperr.ReasonPriceTick},
		{"大商所 高于涨停", req(t, "DCE.m2701", types.Buy, types.Open, "3588", 1), order.CheckPriceLimit, ctperr.ReasonAboveUpperLimit},
		{"大商所 低于跌停", req(t, "DCE.m2701", types.Sell, types.Open, "3180", 1), order.CheckPriceLimit, ctperr.ReasonBelowLowerLimit},
		{"大商所 无仓平昨", req(t, "DCE.m2701", types.Sell, types.CloseYesterday, "3360", 1), order.CheckClosable, ctperr.ReasonCloseYesterdayExceeds},
		{"上期所 非整数倍", req(t, "SHFE.ag2702", types.Buy, types.Open, "15460.5", 1), order.CheckPriceTick, ctperr.ReasonPriceTick},
		{"上期所 高于涨停", req(t, "SHFE.ag2702", types.Buy, types.Open, "17206", 1), order.CheckPriceLimit, ctperr.ReasonAboveUpperLimit},
		{"上期所 低于跌停", req(t, "SHFE.ag2702", types.Sell, types.Open, "14363", 1), order.CheckPriceLimit, ctperr.ReasonBelowLowerLimit},
		{"上期所 无仓平昨", req(t, "SHFE.ag2702", types.Sell, types.CloseYesterday, "15460", 1), order.CheckClosable, ctperr.ReasonCloseYesterdayExceeds},
	}
	// ⚠️ 上期所白银没有时段表 ⇒ 时段那一项没查成；而 match.Fill 先看拒绝、再看没查成，所以白银那几格仍然拒在对应项
	for _, c := range cases {
		s := submitSim(t, ctpChoices(), "1000000")
		before := s.Account()
		_, err := s.Submit(simDay, wall(t, "2026-09-15 10:00"), c.r)
		var rej *match.RejectedError
		if !errors.As(err, &rej) {
			t.Errorf("⚠️ %s：要被拒，得到 %v", c.name, err)
			continue
		}
		if rej.Rejection.Check != c.check {
			t.Errorf("%s：拒在 %v，期望 %v", c.name, rej.Rejection.Check, c.check)
		}
		want, ok := ctperr.Lookup(c.r.Instrument.Exchange, c.reason)
		got, gotOK := rej.Code()
		if !ok || !gotOK || got != want {
			t.Errorf("⚠️ %s：码 %v（%v），语料 %v（%v）", c.name, got, gotOK, want, ok)
		}
		if !sameSnapshot(s.Account(), before) {
			t.Errorf("⚠️ %s：被拒之后账户变了", c.name)
		}
	}
}

// TestSubmitSession 钉住时段三种答案：在、不在（拒）、答不了（没查成），以及时刻与交易日矛盾（报错）。
func TestSubmitSession(t *testing.T) {
	s := submitSim(t, ctpChoices(), "1000000")
	open1 := req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)

	_, err := s.Submit(simDay, wall(t, "2026-09-15 16:00"), open1)
	var rej *match.RejectedError
	if !errors.As(err, &rej) || rej.Rejection.Check != order.CheckSession {
		t.Errorf("⚠️ 16:00 不在任何时段内，要拒在时段：%v", err)
	}
	_, err = s.Submit(simDay, wall(t, "2026-09-15 21:30"), open1)
	if err == nil || !strings.Contains(err.Error(), "矛盾") {
		t.Errorf("⚠️ 21:30 的夜盘属于交易日 20260916，而模拟器在 20260915，要报矛盾：%v", err)
	}
	_, err = s.Submit(simDay, wall(t, "2026-09-15 10:00"), req(t, "SHFE.ag2702", types.Buy, types.Open, "15460", 1))
	var unc *match.UncheckedError
	if !errors.As(err, &unc) || !uncheckedHas(unc, order.CheckSession) {
		t.Errorf("⚠️ 白银没有时段表，要报时段没查成：%v", err)
	}
	if !s.Account().Commission.IsZero() {
		t.Error("⚠️ 三笔都不该成交，账上却有手续费")
	}
}

func uncheckedHas(e *match.UncheckedError, c order.Check) bool {
	for _, u := range e.Unchecked {
		if u.Check == c {
			return true
		}
	}
	return false
}

// TestSubmitRefusesWithoutFacts 钉住缺日历 / 缺取整方向 / 缺限仓时不成交、点名缺哪一项。
func TestSubmitRefusesWithoutFacts(t *testing.T) {
	for _, c := range []struct {
		name  string
		edit  func(*Config)
		check order.Check
	}{
		{"没有日历", func(c *Config) { c.Calendar = nil }, order.CheckSession},
		{"没有大商所的取整方向", func(c *Config) {
			c.TickRounding = map[types.Exchange]refdata.TickRounding{types.SHFE: refdata.TickFloor}
		}, order.CheckPriceLimit},
		{"没有限仓", func(c *Config) { c.PositionLimits = nil }, order.CheckPositionLimit},
	} {
		cfg := Config{Day: simDay, PreBalance: dec("1000000"), Rules: simRules(t), Choices: ctpChoices(),
			Calendar: submitCalendar(t), TickRounding: MeasuredTickRounding(),
			PositionLimits: map[types.InstrumentID]int{simInst(t, "DCE.m2701"): 100}}
		c.edit(&cfg)
		s, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		mark(t, s, "DCE.m2701", "3361", "3384")
		_, err = s.Submit(simDay, wall(t, "2026-09-15 10:00"), req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1))
		var unc *match.UncheckedError
		if !errors.As(err, &unc) || !uncheckedHas(unc, c.check) {
			t.Errorf("⚠️ %s：要报 %v 没查成，得到 %v", c.name, c.check, err)
		}
		if !s.Account().Commission.IsZero() {
			t.Errorf("⚠️ %s：没查成却成交了", c.name)
		}
	}
}

// TestSubmitFundsCheckFollowsFreezeBasis 钉住资金校验用的「要占用」随冻结保证金基准变。
//
// 开多 1 手 m2701 @3360：CTP 按挂单价冻 3360 + 手续费 1.5 = 3361.5；快期按昨结算价冻 3384 + 1.5 = 3385.5。
// 可用 3370 ⇒ CTP 成交、快期拒在资金。
func TestSubmitFundsCheckFollowsFreezeBasis(t *testing.T) {
	kq := KQChoices()
	kq.FeeRounding, kq.SideScope = fee.NoRounding, margin.ByInstrument
	kq.FeeBasis = fee.TradePrice // 只让冻结保证金基准不同：手续费两边都按报价，1.5 / 手按手收也不受影响
	for _, c := range []struct {
		name string
		ch   Choices
		fill bool
	}{{"CTP 挂单价", ctpChoices(), true}, {"快期 昨结算价", kq, false}} {
		s := submitSim(t, c.ch, "3370")
		fr, err := s.FreezeOf(simDay, req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1))
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.Submit(simDay, wall(t, "2026-09-15 10:00"), req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1))
		var rej *match.RejectedError
		switch {
		case c.fill && err != nil:
			t.Errorf("%s：冻 %s + %s ≤ 可用 3370，应当成交：%v", c.name, fr.Margin, fr.Commission, err)
		case !c.fill && (!errors.As(err, &rej) || rej.Rejection.Check != order.CheckFunds):
			t.Errorf("⚠️ %s：冻 %s + %s > 可用 3370，要拒在资金：%v", c.name, fr.Margin, fr.Commission, err)
		}
	}
}

// TestSubmitBareCloseOnNoUseHistory 钉住大商所裸 CLOSE 走报单路径能成交（20260915 修：F3 合进 main 时一律报「冻结算不出来」）。
func TestSubmitBareCloseOnNoUseHistory(t *testing.T) {
	s := submitSim(t, ctpChoices(), "1000000")
	if _, err := s.Submit(simDay, wall(t, "2026-09-15 10:00"), req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	before := s.Account().Commission
	if _, err := s.Submit(simDay, wall(t, "2026-09-15 10:01"), req(t, "DCE.m2701", types.Sell, types.Close, "3360", 1)); err != nil {
		t.Fatalf("⚠️ 大商所今 1 手、裸 CLOSE 1 手的报单要成交：%v", err)
	}
	if got := s.Account().Commission.Sub(before); !got.Equal(dec("0.75")) {
		t.Errorf("裸平消耗今仓收平今档 0.75，得到 %s", got)
	}
	// 无仓裸平：要拒在可平量，不许被「算不出资金」盖住
	_, err := s.Submit(simDay, wall(t, "2026-09-15 10:02"), req(t, "DCE.m2701", types.Sell, types.Close, "3360", 1))
	var rej *match.RejectedError
	if !errors.As(err, &rej) || rej.Rejection.Check != order.CheckClosable {
		t.Fatalf("⚠️ 无仓裸平要拒在可平量：%v", err)
	}
	// ⚠️ 语料里没有裸 CLOSE 的拒单（只有「账上无仓时显式平昨」那一条，CTP 30）⇒ 拒因必须是 Unknown、不给码。
	// 期待 30 就是把显式平昨的码外推到裸 CLOSE 上（评审 20260915）
	if rej.Rejection.Kind != ctperr.ReasonUnknown {
		t.Errorf("⚠️ 无仓裸平给了拒因 %v —— 语料没有裸 CLOSE 的拒单", rej.Rejection.Kind)
	}
	if c, ok := rej.Code(); ok {
		t.Errorf("⚠️ 无仓裸平查到了码 %v —— 那是外推", c)
	}
}

// TestFreezeOfBareCloseSplitsYesterdayFirst 钉住门面给裸 CLOSE 的持仓侧冻结按先平昨拆（与成交时消耗的那一边一致；CTP 上未观测，推得）。
func TestFreezeOfBareCloseSplitsYesterdayFirst(t *testing.T) {
	// y2701：平今档 = 平昨档，裸平 2 手（超出今仓）不撞 §13 #21 的分歧段
	s := withHistoryOn(t, "DCE.y2701", "8000") // 昨 1，停在 simNext
	if err := s.ApplyTrade(simNext, trade(t, "DCE.y2701", types.Buy, types.Open, "8000", 1)); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ vol, today, his int }{{1, 0, 1}, {2, 1, 1}} {
		fr, err := s.FreezeOf(simNext, req(t, "DCE.y2701", types.Sell, types.Close, "8000", c.vol))
		if err != nil {
			t.Fatal(err)
		}
		if fr.VolumeToday != c.today || fr.VolumeHistory != c.his || !fr.Margin.IsZero() {
			t.Errorf("⚠️ 今1昨1 裸平 %d 手：冻 今 %d / 昨 %d（保证金 %s），期望 今 %d / 昨 %d、不冻保证金",
				c.vol, fr.VolumeToday, fr.VolumeHistory, fr.Margin, c.today, c.his)
		}
	}
}
