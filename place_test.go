package futsim

import (
	"errors"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestPlaceFreezesAndCancelRestores 钉住挂单冻结、撤单释放，往返之后账户逐字段回到挂单前。
//
// （state.md：CTP 上「报单 → 挂上 → 撤单 → 账户回到起点，差 0」。）
func TestPlaceFreezesAndCancelRestores(t *testing.T) {
	s := submitSim(t, ctpChoices(), "1000000")
	before := s.Account()
	fr, err := s.Place(simDay, wall(t, "2026-09-15 10:00"), "o1", req(t, "DCE.m2701", types.Buy, types.Open, "3360", 2))
	if err != nil {
		t.Fatal(err)
	}
	a := s.Account()
	// CTP 预设：冻结保证金按挂单价 3360 × 10 × 2 × 0.1 = 6720；手续费 1.5 × 2
	if !a.FrozenMargin.Equal(dec("6720")) || !a.FrozenCommission.Equal(dec("3")) || !fr.Margin.Equal(dec("6720")) {
		t.Errorf("挂单冻结 保证金 %s / 手续费 %s，期望 6720 / 3", a.FrozenMargin, a.FrozenCommission)
	}
	if !before.Available.Sub(a.Available).Equal(dec("6723")) {
		t.Errorf("⚠️ 挂单后可用少了 %s，期望 6723 —— 冻结没从可用里扣", before.Available.Sub(a.Available))
	}
	if !a.Commission.IsZero() || !a.CurrMargin.IsZero() {
		t.Errorf("⚠️ 挂单不成交：手续费 %s、占用 %s 应当都是 0", a.Commission, a.CurrMargin)
	}
	if live := s.Live(); len(live) != 1 || live[0] != "o1" {
		t.Errorf("簿上应有 o1，得到 %v", live)
	}
	if _, err := s.Place(simDay, wall(t, "2026-09-15 10:00"), "o1", req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err == nil {
		t.Error("⚠️ 同一个编号挂了两次")
	}
	if err := s.Cancel(simDay, "o1"); err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(s.Account(), before) || len(s.Live()) != 0 {
		t.Errorf("⚠️ 挂撤往返之后账户没回到起点：\n前 %+v\n后 %+v", before, s.Account())
	}
	if err := s.Cancel(simDay, "o1"); err == nil {
		t.Error("⚠️ 撤了两次都成功 —— 第二次会凭空多出一份可用")
	}
}

// TestPlacedCloseHoldsClosable 钉住挂着的平仓单占着可平量：第二笔平仓（挂单或立即成交）拒在可平量，撤掉第一笔后才能挂。
func TestPlacedCloseHoldsClosable(t *testing.T) {
	s := submitSim(t, ctpChoices(), "1000000")
	at := wall(t, "2026-09-15 10:00")
	if _, err := s.Submit(simDay, at, req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	closeToday := req(t, "DCE.m2701", types.Sell, types.CloseToday, "3370", 1)
	fr, err := s.Place(simDay, at, "c1", closeToday)
	if err != nil {
		t.Fatal(err)
	}
	if fr.VolumeToday != 1 || !fr.Margin.IsZero() {
		t.Errorf("平今挂单冻今 1 手、不冻保证金，得到 %+v", fr)
	}
	var rej *match.RejectedError
	if _, err := s.Place(simDay, at, "c2", closeToday); !errors.As(err, &rej) || rej.Rejection.Check != order.CheckClosable {
		t.Errorf("⚠️ 今仓 1 手已被挂单冻住，第二笔平今挂单要拒在可平量：%v", err)
	}
	if _, err := s.Submit(simDay, at, closeToday); !errors.As(err, &rej) || rej.Rejection.Check != order.CheckClosable {
		t.Errorf("⚠️ 今仓 1 手已被挂单冻住，立即成交的平今也要拒在可平量：%v", err)
	}
	if err := s.Cancel(simDay, "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Place(simDay, at, "c2", closeToday); err != nil {
		t.Errorf("撤掉 c1 之后应能挂 c2：%v", err)
	}
}

// TestFillBooksLikeSubmit 钉住挂单成交的账 = 同价立即成交的账，成交后冻结清零。
func TestFillBooksLikeSubmit(t *testing.T) {
	at := wall(t, "2026-09-15 10:00")
	a := submitSim(t, ctpChoices(), "1000000")
	if _, err := a.Place(simDay, at, "o1", req(t, "DCE.m2701", types.Buy, types.Open, "3360", 2)); err != nil {
		t.Fatal(err)
	}
	tr, err := a.Fill(simDay, "o1")
	if err != nil {
		t.Fatal(err)
	}
	if !tr.Price.Equal(dec("3360")) || tr.Volume != 2 {
		t.Errorf("挂单成交价 = 挂单价、量 = 全部，得到 %s × %d", tr.Price, tr.Volume)
	}
	b := submitSim(t, ctpChoices(), "1000000")
	if _, err := b.Submit(simDay, at, req(t, "DCE.m2701", types.Buy, types.Open, "3360", 2)); err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(a.Account(), b.Account()) {
		t.Errorf("⚠️ 挂单成交与立即成交的账不同：\n%+v\n%+v", a.Account(), b.Account())
	}
	if !a.Account().FrozenMargin.IsZero() || !a.Account().FrozenCommission.IsZero() || len(a.Live()) != 0 {
		t.Error("⚠️ 成交之后冻结没有清零 / 挂单还在簿上")
	}
}

// TestFillRestoresOnFailure 钉住成交记账失败时挂单与冻结原样放回。
//
// ⚠️ 正常流程里挂上的单成交时 ApplyTrade 很难失败，这里白盒地拿掉计价价让重算截面失败 —— 验的是「放回」，不是「什么会失败」。
func TestFillRestoresOnFailure(t *testing.T) {
	s := submitSim(t, ctpChoices(), "1000000")
	if _, err := s.Place(simDay, wall(t, "2026-09-15 10:00"), "o1", req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	before := s.Account()
	m := simInst(t, "DCE.m2701")
	ps := s.prices[m]
	ps.hasLast = false
	s.prices[m] = ps
	if _, err := s.Fill(simDay, "o1"); err == nil {
		t.Fatal("前提：拿掉计价价之后成交记账应当失败")
	}
	if !sameSnapshot(s.Account(), before) || len(s.Live()) != 1 {
		t.Errorf("⚠️ 成交失败之后挂单或冻结没放回：簿 %v\n前 %+v\n后 %+v", s.Live(), before, s.Account())
	}
	if s.broken != nil {
		t.Errorf("放回成功时不该失效：%v", s.broken)
	}
}

// TestSettleAndApplyTradeRespectLiveOrders 钉住：簿上有挂单不许结算；灌成交不许平掉挂单冻住的手数。
func TestSettleAndApplyTradeRespectLiveOrders(t *testing.T) {
	s := submitSim(t, ctpChoices(), "1000000")
	at := wall(t, "2026-09-15 10:00")
	if _, err := s.Submit(simDay, at, req(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Place(simDay, at, "c1", req(t, "DCE.m2701", types.Sell, types.CloseToday, "3370", 1)); err != nil {
		t.Fatal(err)
	}
	before := s.Account()
	err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Sell, types.CloseToday, "3370", 1))
	if err == nil || !strings.Contains(err.Error(), "走 Fill") {
		t.Errorf("⚠️ 灌成交平掉了挂单冻住的今仓：%v", err)
	}
	if !sameSnapshot(s.Account(), before) {
		t.Error("⚠️ 被拒的灌成交改了账户")
	}
	err = s.Settle(simDay, settlePx(t, "DCE.m2701", "3370", "SHFE.ag2702", "15785"), simNext)
	if err == nil || !strings.Contains(err.Error(), "先撤单") {
		t.Errorf("⚠️ 簿上有挂单时结算要报「先撤单」：%v", err)
	}
}

// withHistorySubmit 在 submitSim 上开多 1 @3399、按 3384 结算、次日开今 1 @3360 ⇒ 停在 simNext，m2701 今 1 昨 1。
func withHistorySubmit(t *testing.T) *Simulator {
	t.Helper()
	return withHistorySubmitOn(t, "DCE.m2701", "3399", "3384", "3360")
}

// withHistorySubmitOn 同上，合约与价格可换：开多 1 @open、按 settle 结算、次日开今 1 @next。
func withHistorySubmitOn(t *testing.T, sym, open, settle, next string) *Simulator {
	t.Helper()
	s := submitSim(t, ctpChoices(), "1000000")
	if _, err := s.Submit(simDay, wall(t, "2026-09-15 10:00"), req(t, sym, types.Buy, types.Open, open, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(simDay, settlePx(t, sym, settle), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, sym, next, settle)
	if err := s.ApplyTrade(simNext, trade(t, sym, types.Buy, types.Open, next, 1)); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestTwoPlacedClosesSplitAcrossFreeVolumes 钉住多笔挂单之间的拆分：每笔裸 CLOSE 拆的是**扣掉簿上已冻之后**的今 / 昨。
//
// ⚠️ 评审 20260915 打回的缺陷：原来拿持有的昨仓拆，今1昨1 挂两笔裸平各 1 手时两笔都冻「昨 1」，
// 簿上冻昨 2 而账上昨仓只有 1 ⇒ 谁都成交不了（Fill 报「会平掉挂单冻住的手数」），只能撤。单笔对照测不到它。
func TestTwoPlacedClosesSplitAcrossFreeVolumes(t *testing.T) {
	at := wall(t, "2026-09-16 10:00")
	bare := req(t, "DCE.m2701", types.Sell, types.Close, "3360", 1)
	for _, c := range []struct {
		name  string
		first order.Request
	}{
		{"两笔裸平", bare},
		{"先平昨再裸平", req(t, "DCE.m2701", types.Sell, types.CloseYesterday, "3360", 1)},
	} {
		s := withHistorySubmit(t)
		f1, err := s.Place(simNext, at, "c1", c.first)
		if err != nil {
			t.Fatal(err)
		}
		f2, err := s.Place(simNext, at, "c2", bare)
		if err != nil {
			t.Fatalf("%s：第二笔裸平挂不上：%v", c.name, err)
		}
		if f1.VolumeHistory != 1 || f1.VolumeToday != 0 || f2.VolumeHistory != 0 || f2.VolumeToday != 1 {
			t.Errorf("⚠️ %s：第一笔冻 今 %d / 昨 %d、第二笔冻 今 %d / 昨 %d，期望 昨 1 与 今 1 —— 两笔抢了同一手昨仓",
				c.name, f1.VolumeToday, f1.VolumeHistory, f2.VolumeToday, f2.VolumeHistory)
		}
		for _, id := range []string{"c1", "c2"} {
			if _, err := s.Fill(simNext, id); err != nil {
				t.Errorf("⚠️ %s：%s 成交不了：%v", c.name, id, err)
			}
		}
		if len(s.Live()) != 0 {
			t.Errorf("%s：两笔都成交后簿上还有 %v", c.name, s.Live())
		}
		// 与两笔直接成交的账相同
		b := withHistorySubmit(t)
		for _, r := range []order.Request{c.first, bare} {
			if _, err := b.Submit(simNext, at, r); err != nil {
				t.Fatalf("%s 对照：%v", c.name, err)
			}
		}
		if !sameSnapshot(s.Account(), b.Account()) {
			t.Errorf("⚠️ %s：挂单成交与直接成交的账不同：\n%+v\n%+v", c.name, s.Account(), b.Account())
		}
	}
}

// TestBareCloseFillOrderAndSubmitAmongLiveOrders 钉住成交时的消耗与冻结时的拆分是同一个规则（在可平量里先平昨）：
// 哪一笔先成交都行；已有挂单冻住昨仓时，立即成交的裸平去平今仓而不是去抢那手昨仓。
//
// ⚠️ 修 F4 打回时自己走多笔组合撞出来的：原来成交时按先平昨在整份持仓上消耗，「先成交冻今的那笔」会去平
// 另一笔冻住的昨仓、被守卫拒掉；Submit 裸平在有挂单时同理。
// 用 y2701（平今档 = 平昨档）：m2701 上后一笔会撞 §13 #21 的分歧段，那一格单独钉在最后。
func TestBareCloseFillOrderAndSubmitAmongLiveOrders(t *testing.T) {
	at := wall(t, "2026-09-16 10:00")
	bare := req(t, "DCE.y2701", types.Sell, types.Close, "8000", 1)
	y := simInst(t, "DCE.y2701")
	pos := func(s *Simulator) (int, int) {
		p, _ := s.Position(y, types.Speculation)
		return p.VolumeToday(types.Buy), p.VolumeHistory(types.Buy)
	}

	s := withHistorySubmitOn(t, "DCE.y2701", "8000", "8000", "8000") // 今 1 昨 1
	for _, id := range []string{"a", "b"} {
		if _, err := s.Place(simNext, at, id, bare); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Fill(simNext, "b"); err != nil {
		t.Fatalf("⚠️ 先成交冻今的那笔失败：%v", err)
	}
	if td, hs := pos(s); td != 0 || hs != 1 {
		t.Errorf("⚠️ b 冻的是今仓，成交后应剩昨 1，得到 今 %d / 昨 %d", td, hs)
	}
	if _, err := s.Fill(simNext, "a"); err != nil {
		t.Errorf("⚠️ 再成交 a 失败：%v", err)
	}

	u := withHistorySubmitOn(t, "DCE.y2701", "8000", "8000", "8000")
	if _, err := u.Place(simNext, at, "a", bare); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Submit(simNext, at, bare); err != nil {
		t.Fatalf("⚠️ 有挂单冻住昨仓时立即成交的裸平失败：%v", err)
	}
	if td, hs := pos(u); td != 0 || hs != 1 {
		t.Errorf("⚠️ 立即成交的裸平应平今、留下挂单冻住的昨 1，得到 今 %d / 昨 %d", td, hs)
	}
	if _, err := u.Fill(simNext, "a"); err != nil {
		t.Errorf("⚠️ 挂单 a 随后成交失败：%v", err)
	}

	// ⚠️ 已知陷阱（§13 #21 未收敛的直接后果，钉住而不是修）：m2701 平今档 ≠ 平昨档。
	// 挂 a、b 各裸平 1 手、先成交 b（平今）之后，a 面对的是「只有昨仓的裸平」—— 分歧段 ⇒ 成交报错、a 留在簿上、冻结原样。
	// 挂的时候按平仓前今仓 1 手收平今档，挂得上；成交时今仓已经没了。
	w := withHistorySubmit(t)
	barem := req(t, "DCE.m2701", types.Sell, types.Close, "3360", 1)
	for _, id := range []string{"a", "b"} {
		if _, err := w.Place(simNext, at, id, barem); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Fill(simNext, "b"); err != nil {
		t.Fatal(err)
	}
	before := w.Account()
	if _, err := w.Fill(simNext, "a"); err == nil || !strings.Contains(err.Error(), "#21") {
		t.Errorf("m2701 上 a 面对只有昨仓的裸平，要报 §13 #21：%v", err)
	}
	if !sameSnapshot(w.Account(), before) || len(w.Live()) != 1 {
		t.Errorf("⚠️ #21 报错之后 a 或冻结没留住：簿 %v", w.Live())
	}
}
