package futsim

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestTodayTierLots 逐格钉住 §13 #23 的 (f)：多手按额度拆、额度分方向（F13，design.md 门面形状 §17），「显式平今扣不扣额度」分岔报错。
func TestTodayTierLots(t *testing.T) {
	id := types.InstrumentID{Exchange: types.DCE, Product: "m", Year: 2027, Month: 1}
	q := func(o, c, e int) quotaCount { return quotaCount{Opened: o, Charged: c, Explicit: e} }
	cases := []struct {
		name string
		vol  int
		own  quotaCount
		want int
		err  string
	}{
		{"当日没开过 ⇒ 平昨档（郑商所 X1、大商所 E1）", 1, q(0, 0, 0), 0, ""},
		{"当日开 1 未用 ⇒ 平今档（X2、大商所 0915）", 1, q(1, 0, 0), 1, ""},
		{"当日开 1 已用 ⇒ 平昨档（X0；(a) 预言平今）", 1, q(1, 1, 0), 0, ""},
		{"当日开 2 已用 2 ⇒ 平昨档（ctp-quota 第 3 笔）", 1, q(2, 2, 0), 0, ""},
		{"两手、额度 2 ⇒ 全今", 2, q(2, 0, 0), 2, ""},
		{"两手、额度 0 ⇒ 全昨", 2, q(1, 1, 0), 0, ""},
		{"两手、额度 1 ⇒ 按额度拆 1 手平今（外推 A，m2703 / MA703）", 2, q(1, 0, 0), 1, ""},
		{"三手、额度 2 ⇒ 2 手平今", 3, q(2, 0, 0), 2, ""},
		{"显式平今用掉额度 ⇒ 扣 / 不扣分岔，报错", 1, q(1, 1, 1), 0, "显式平今扣不扣额度"},
		{"两手、显式平今之后 ⇒ 扣给 1、不扣给 2，报错", 2, q(2, 1, 1), 0, "显式平今扣不扣额度"},
		{"显式平今之后额度仍够 ⇒ 同值照收", 1, q(2, 1, 1), 1, ""},
		{"两手、显式平今之后额度仍够两手 ⇒ 同值照收", 2, q(3, 1, 1), 2, ""},
	}
	for _, c := range cases {
		got, err := todayTierLots(id, c.vol, c.own)
		switch {
		case c.err != "":
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s：要报错（含「%s」），得到 %d / %v", c.name, c.err, got, err)
			}
		case err != nil:
			t.Errorf("%s：不该报错：%v", c.name, err)
		case got != c.want:
			t.Errorf("%s：平今档 %d 手，期望 %d", c.name, got, c.want)
		}
	}
}

// TestUndatedCloseQuotaFullNightCZCE 从门面走一遍 20260917 夜盘郑商所的整晚（交易日 20260918）：
// X1（今0昨2 裸平，开仓之前）→ 开今 1 → X2（今1昨1 裸平）→ X0（今1昨0 裸平）⇒ 收 2 / 6 / 2。
// ⚠️ X1 平在开仓之前、额度 0 那一格专门防「按当日开仓量而不扣已平」的错读（不设下限的 (g)）。
func TestUndatedCloseQuotaFullNightCZCE(t *testing.T) {
	s := newSim(t)
	mark(t, s, "CZCE.MA2701", "3000", "3000")
	if err := s.ApplyTrade(simDay, trade(t, "CZCE.MA2701", types.Buy, types.Open, "3000", 2)); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(simDay, settlePx(t, "CZCE.MA2701", "3000"), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, "CZCE.MA2701", "3000", "3000")
	steps := []struct {
		off  types.Offset
		dir  types.Direction
		want string // 这一笔的手续费
	}{
		{types.Close, types.Sell, "2"}, // X1
		{types.Open, types.Buy, "2"},   // 开今 1（开仓费 2）
		{types.Close, types.Sell, "6"}, // X2
		{types.Close, types.Sell, "2"}, // X0
	}
	for i, st := range steps {
		b := s.Account().Commission
		if err := s.ApplyTrade(simNext, trade(t, "CZCE.MA2701", st.dir, st.off, "3000", 1)); err != nil {
			t.Fatalf("第 %d 步：%v", i+1, err)
		}
		if got := s.Account().Commission.Sub(b); !got.Equal(dec(st.want)) {
			t.Errorf("⚠️ 第 %d 步收了 %s，柜台 %s（ctp-slices-20260918{,-2..-6}）", i+1, got, st.want)
		}
	}
}

// TestQuotaRefusalsLeaveNoTrace：三条外推报错之后，账户、持仓、额度都不动。
func TestQuotaRefusalsLeaveNoTrace(t *testing.T) {
	run := func(name string, setup func(s *Simulator), vol int, want string) {
		t.Helper()
		s := withHistoryOn(t, "DCE.m2701", "3000")
		setup(s)
		before, q := s.Account(), s.quotaOf(simInst(t, "DCE.m2701"), types.Speculation, types.Buy)
		err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Sell, types.Close, "3000", vol))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s：要报错（含「%s」），得到 %v", name, want, err)
		}
		if !sameSnapshot(s.Account(), before) || s.quotaOf(simInst(t, "DCE.m2701"), types.Speculation, types.Buy) != q {
			t.Errorf("⚠️ %s：报错之后状态变了", name)
		}
	}
	open := func(dir types.Direction, vol int) func(*Simulator) {
		return func(s *Simulator) {
			if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", dir, types.Open, "3000", vol)); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("显式平今之后", func(s *Simulator) {
		open(types.Buy, 1)(s)
		open(types.Buy, 1)(s) // 今 2：显式平今 1 之后仍有今仓，额度 2 − 1
		if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Sell, types.CloseToday, "3000", 1)); err != nil {
			t.Fatal(err)
		}
		if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Sell, types.Close, "3000", 1)); err != nil {
			t.Fatal(err) // 额度还剩 1：扣 / 不扣都 ≥ 1，照收平今
		}
	}, 1, "显式平今扣不扣额度")
}

// TestQuotaMultiLotAndDirectionF13：F11 外推 A、C 放开之后在门面上的形状（design.md 门面形状 §17）。
// 合成费率 m2701 平昨 1.2 / 平今 0.75（simRules）；昨 1 由真结算造出。
func TestQuotaMultiLotAndDirectionF13(t *testing.T) {
	open := func(s *Simulator, dir types.Direction) {
		t.Helper()
		if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", dir, types.Open, "3000", 1)); err != nil {
			t.Fatal(err)
		}
	}
	closeFee := func(s *Simulator, vol int) string {
		t.Helper()
		before := s.Account().Commission
		if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Sell, types.Close, "3000", vol)); err != nil {
			t.Fatalf("⚠️ 裸平 %d 手不该报错（F13 已放开）：%v", vol, err)
		}
		return s.Account().Commission.Sub(before).String()
	}
	// A：昨 1、开今 1（额度 1）、一笔裸平 2 手 ⇒ 1 手平今 0.75 + 1 手平昨 1.2
	s := withHistoryOn(t, "DCE.m2701", "3000")
	open(s, types.Buy)
	if got := closeFee(s, 2); got != "1.95" {
		t.Errorf("⚠️ 外推 A：裸平 2 手收 %s，期望 0.75 + 1.2 = 1.95（按额度拆）", got)
	}
	if q := s.quotaOf(simInst(t, "DCE.m2701"), types.Speculation, types.Buy); q.Charged != 1 {
		t.Errorf("⚠️ 外推 A：按平今档收了 %d 手，期望 1", q.Charged)
	}
	// C：多头昨 1、卖开 1（空头额度 1，多头额度 0）、裸平多头 1 ⇒ 平昨档 1.2
	s = withHistoryOn(t, "DCE.m2701", "3000")
	open(s, types.Sell)
	if got := closeFee(s, 1); got != "1.2" {
		t.Errorf("⚠️ 外推 C：开过反方向之后裸平多头收 %s，期望平昨档 1.2（额度分方向）", got)
	}
}

// TestQuotaSurvivesStateAndResetsAtSettle：额度用掉一半时存档往返，裸平收费与不经存档相同；跨一次 Settle 之后额度从 0 起算。
func TestQuotaSurvivesStateAndResetsAtSettle(t *testing.T) {
	build := func() *Simulator {
		s, err := New(submitCfg(t, simDay))
		if err != nil {
			t.Fatal(err)
		}
		mark(t, s, "DCE.m2701", "3000", "3000")
		for i := 0; i < 2; i++ {
			if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3000", 1)); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Sell, types.Close, "3000", 1)); err != nil {
			t.Fatal(err) // 额度 2 → 用掉 1
		}
		return s
	}
	direct := build()
	st, err := build().State()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Quotas) != 1 || st.Quotas[0].OpenedToday != 2 || st.Quotas[0].ChargedToday != 1 {
		t.Fatalf("存档里的额度计数 %+v，期望 开 2 / 收平今 1", st.Quotas)
	}
	restored, err := Restore(submitCfg(t, simDay), roundTrip(t, st))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Simulator{direct, restored} {
		b := s.Account().Commission
		if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Sell, types.Close, "3000", 1)); err != nil {
			t.Fatal(err)
		}
		if got := s.Account().Commission.Sub(b); !got.Equal(dec("0.75")) {
			t.Errorf("⚠️ 额度还剩 1 时裸平收了 %s，期望平今档 0.75 —— 经存档恢复的额度计数丢了？", got)
		}
	}

	// 跨一次结算：昨天的额度不带到今天
	if err := direct.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3000", 1)); err != nil {
		t.Fatal(err)
	}
	if err := direct.Settle(simDay, settlePx(t, "DCE.m2701", "3000"), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, direct, simNext, "DCE.m2701", "3000", "3000")
	b := direct.Account().Commission
	if err := direct.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Sell, types.Close, "3000", 1)); err != nil {
		t.Fatal(err)
	}
	if got := direct.Account().Commission.Sub(b); !got.Equal(dec("1.2")) {
		t.Errorf("⚠️ 结算后当日没开过仓，裸平收了 %s，期望平昨档 1.2 —— 额度没随交易日清零", got)
	}
}

// TestRestoreRefusesTamperedQuota：存档是可以手改的文件（design.md §6.5）。
func TestRestoreRefusesTamperedQuota(t *testing.T) {
	s, err := New(submitCfg(t, simDay))
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, "DCE.m2701", "3000", "3000")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3000", 2)); err != nil {
		t.Fatal(err)
	}
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		edit func(*State)
		want string
	}{
		{"额度计数丢了", func(st *State) { st.Quotas = nil }, "额度计数丢了"},
		{"已收平今 > 当日开仓", func(st *State) { st.Quotas[0].ChargedToday = 3 }, "额度计数不成立"},
		{"显式 > 已收平今", func(st *State) { st.Quotas[0].ExplicitToday = 1 }, "额度计数不成立"},
		{"当日开仓少于今仓", func(st *State) { st.Quotas[0].OpenedToday = 1 }, "额度计数丢了"},
		{"重复", func(st *State) { st.Quotas = append(st.Quotas, st.Quotas[0]) }, "出现两次"},
	} {
		tampered := roundTrip(t, st)
		c.edit(&tampered)
		if _, err := Restore(submitCfg(t, simDay), tampered); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("⚠️ %s：要被拒（含「%s」），得到 %v", c.name, c.want, err)
		}
	}
}

// TestPendingBareClosesShareQuotaAtFreezeAndConsumeAtFill：两笔裸平挂单按同一个额度冻结（挂单不预占额度），
// 成交时先成交的那笔用掉额度、后一笔按剩余收 ⇒ 冻结额与成交额可以不同（design.md 门面形状 §15 验收）。
func TestPendingBareClosesShareQuotaAtFreezeAndConsumeAtFill(t *testing.T) {
	s := withHistoryOn(t, "DCE.m2701", "3000")
	if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Buy, types.Open, "3000", 1)); err != nil {
		t.Fatal(err) // 今 1 昨 1，额度 1
	}
	req := order.Request{Instrument: simInst(t, "DCE.m2701"), Direction: types.Sell, Offset: types.Close,
		Hedge: types.Speculation, Price: dec("3000"), Volume: 1}
	f1, err := s.FreezeOf(simNext, req)
	if err != nil {
		t.Fatal(err)
	}
	if !f1.Commission.Equal(dec("0.75")) {
		t.Fatalf("额度 1 时裸平挂单应按平今档冻 0.75，得到 %s", f1.Commission)
	}
	// 第一笔成交用掉额度之后，同样一笔的冻结 / 成交都按平昨档
	if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Sell, types.Close, "3000", 1)); err != nil {
		t.Fatal(err)
	}
	f2, err := s.FreezeOf(simNext, req)
	if err != nil {
		t.Fatal(err)
	}
	if !f2.Commission.Equal(dec("1.2")) {
		t.Errorf("⚠️ 额度用掉之后裸平挂单应按平昨档冻 1.2，得到 %s —— 冻结没看额度", f2.Commission)
	}
}
