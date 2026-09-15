package futsim

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// kqChoices 是快期预设补上两格没实测的（取整、大边范围）之后能开户的口径。
func kqChoices() Choices {
	c := KQChoices()
	c.FeeRounding = fee.NoRounding
	c.SideScope = margin.ByInstrument
	return c
}

// rbTodayAndHistory 开一个模拟器：rb2701 在 simDay 开多 3 @3000、按 3000 结算，次日开今 1 @3010 ⇒ 停在 simNext，多 今 1 / 昨 3。
//
// 形状照 kq_facts 32（多今 1 / 多昨 3 的 rb2701：CLOSE 冻 volume_long_frozen_his）。
func rbTodayAndHistory(t *testing.T, ch Choices) *Simulator {
	t.Helper()
	s, err := New(Config{Day: simDay, PreBalance: dec("1000000"), Rules: simRules(t), Choices: ch})
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, "SHFE.rb2701", "3000", "3000")
	if err := s.ApplyTrade(simDay, trade(t, "SHFE.rb2701", types.Buy, types.Open, "3000", 3)); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(simDay, settlePx(t, "SHFE.rb2701", "3000"), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, "SHFE.rb2701", "3010", "3000")
	if err := s.ApplyTrade(simNext, trade(t, "SHFE.rb2701", types.Buy, types.Open, "3010", 1)); err != nil {
		t.Fatal(err)
	}
	return s
}

func rbVolumes(t *testing.T, s *Simulator) (today, history int) {
	t.Helper()
	p, ok := s.Position(simInst(t, "SHFE.rb2701"), types.Speculation)
	if !ok {
		t.Fatal("rb2701 没有持仓")
	}
	return p.VolumeToday(types.Buy), p.VolumeHistory(types.Buy)
}

// TestUndatedCloseAsYesterdayOnUseHistory 钉住第八项口径：UseHistory 上的裸 CLOSE 记作平昨 ——
// 冻昨仓、消耗昨仓、收平昨档，且成交、挂单成交、冻结三条路给同一本账。
//
// ⚠️ 判别力来自两处，缺一处都有别的实现能过：
//   - 手续费档：rb2701 平昨 2 / 平今 5。按 NoUseHistory 那条「在可平量里先平昨、档位按平仓前今仓」记，冻结与成交都收 5（§13 #21 的 a、b）
//   - 超过昨仓：今 1 昨 3 裸平 4 手。按可平量拆会拆成 昨 3 + 今 1 成交；快期是拒单（「平昨手数超过昨仓持仓量」），本库报错
func TestUndatedCloseAsYesterdayOnUseHistory(t *testing.T) {
	bare := req(t, "SHFE.rb2701", types.Sell, types.Close, "3010", 1)

	s := rbTodayAndHistory(t, kqChoices())
	fr, err := s.FreezeOf(simNext, bare)
	if err != nil {
		t.Fatalf("⚠️ 快期口径下 UseHistory 上的裸 CLOSE 算不出冻结：%v", err)
	}
	if fr.VolumeHistory != 1 || fr.VolumeToday != 0 || !fr.Commission.Equal(dec("2")) || !fr.Margin.IsZero() {
		t.Errorf("⚠️ 裸 CLOSE 1 手的冻结 %+v，期望冻昨 1、手续费 2（平昨档）、保证金 0", fr)
	}

	// 直接成交：与显式平昨逐字段相同
	a, b := rbTodayAndHistory(t, kqChoices()), rbTodayAndHistory(t, kqChoices())
	if err := a.ApplyTrade(simNext, trade(t, "SHFE.rb2701", types.Sell, types.Close, "3010", 1)); err != nil {
		t.Fatalf("⚠️ 快期口径下 UseHistory 上的裸 CLOSE 成交不了：%v", err)
	}
	if err := b.ApplyTrade(simNext, trade(t, "SHFE.rb2701", types.Sell, types.CloseYesterday, "3010", 1)); err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(a.Account(), b.Account()) {
		t.Errorf("⚠️ 裸 CLOSE 与平昨的账不同：\n%+v\n%+v", a.Account(), b.Account())
	}
	if td, h := rbVolumes(t, a); td != 1 || h != 2 {
		t.Errorf("⚠️ 裸 CLOSE 1 手之后 今 %d / 昨 %d，期望 今 1 / 昨 2（消耗昨仓）", td, h)
	}

	// 挂单成交：挂上冻昨、成交后与直接成交同账
	c := rbTodayAndHistory(t, kqChoices())
	got, err := c.PlaceAccepted(simNext, "k1", bare)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", fr) {
		t.Errorf("PlaceAccepted 冻的 %+v 与 FreezeOf 算的 %+v 不同", got, fr)
	}
	// 簿上冻着昨 1：再挂平昨 3 手（昨仓共 3）就超了
	if _, err := c.PlaceAccepted(simNext, "k2", req(t, "SHFE.rb2701", types.Sell, types.CloseYesterday, "3010", 3)); err == nil {
		t.Error("⚠️ 裸 CLOSE 冻住昨 1 之后，平昨 3 手也挂上了 —— 裸 CLOSE 没冻在昨仓上")
	}
	if _, err := c.Fill(simNext, "k1"); err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(c.Account(), a.Account()) {
		t.Errorf("⚠️ 挂单成交与直接成交的账不同：\n%+v\n%+v", c.Account(), a.Account())
	}

	// 超过昨仓：拒，状态不动
	d := rbTodayAndHistory(t, kqChoices())
	before := d.Account()
	four := req(t, "SHFE.rb2701", types.Sell, types.Close, "3010", 4)
	if err := d.ApplyTrade(simNext, trade(t, "SHFE.rb2701", types.Sell, types.Close, "3010", 4)); err == nil {
		t.Error("⚠️ 今 1 昨 3 时裸 CLOSE 4 手成交了 —— 快期上它是平昨，昨仓只有 3 手")
	}
	if _, err := d.PlaceAccepted(simNext, "k4", four); err == nil {
		t.Error("⚠️ 今 1 昨 3 时裸 CLOSE 4 手挂上了")
	}
	if td, h := rbVolumes(t, d); td != 1 || h != 3 || !sameSnapshot(before, d.Account()) || len(d.Live()) != 0 {
		t.Errorf("被拒之后状态变了：今 %d / 昨 %d，簿 %v", td, h, d.Live())
	}
}

// TestUndatedCloseChoiceZeroValue 钉住第八项的零值：开得了户，而 UseHistory 上的裸 CLOSE 在记账路径上照旧报错；
// 以及口径只管 UseHistory —— 快期口径下 NoUseHistory 的裸 CLOSE 仍按可平量拆。
func TestUndatedCloseChoiceZeroValue(t *testing.T) {
	zero := kqChoices()
	zero.UndatedCloseOnUseHistory = UndatedCloseUnmeasured
	s := rbTodayAndHistory(t, zero)
	bare := req(t, "SHFE.rb2701", types.Sell, types.Close, "3010", 1)
	if err := s.ApplyTrade(simNext, trade(t, "SHFE.rb2701", types.Sell, types.Close, "3010", 1)); err == nil {
		t.Error("⚠️ 零值口径下 UseHistory 上的裸 CLOSE 成交了")
	}
	if _, err := s.FreezeOf(simNext, bare); err == nil {
		t.Error("⚠️ 零值口径下 UseHistory 上的裸 CLOSE 算出了冻结")
	}
	if _, err := s.PlaceAccepted(simNext, "z", bare); err == nil {
		t.Error("⚠️ 零值口径下 UseHistory 上的裸 CLOSE 挂上了")
	}

	bad := kqChoices()
	bad.UndatedCloseOnUseHistory = UndatedCloseAsYesterday + 1
	if _, err := New(Config{Day: simDay, PreBalance: dec("1"), Rules: simRules(t), Choices: bad}); err == nil || !strings.Contains(err.Error(), "UndatedCloseOnUseHistory") {
		t.Errorf("⚠️ 认不得的第八项取值开户成功了：%v", err)
	}

	// NoUseHistory：今 1 昨 1 裸平 2 手，按可平量拆成 今 1 + 昨 1（记作平昨则冻昨 2）
	m, err := New(Config{Day: simDay, PreBalance: dec("1000000"), Rules: simRules(t), Choices: kqChoices()})
	if err != nil {
		t.Fatal(err)
	}
	mark(t, m, "DCE.y2701", "8000", "8000")
	if err := m.ApplyTrade(simDay, trade(t, "DCE.y2701", types.Buy, types.Open, "8000", 1)); err != nil {
		t.Fatal(err)
	}
	if err := m.Settle(simDay, settlePx(t, "DCE.y2701", "8000"), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, m, simNext, "DCE.y2701", "8000", "8000")
	if err := m.ApplyTrade(simNext, trade(t, "DCE.y2701", types.Buy, types.Open, "8000", 1)); err != nil {
		t.Fatal(err)
	}
	fr, err := m.FreezeOf(simNext, req(t, "DCE.y2701", types.Sell, types.Close, "8000", 2))
	if err != nil || fr.VolumeToday != 1 || fr.VolumeHistory != 1 {
		t.Errorf("⚠️ 快期口径下 NoUseHistory 的裸 CLOSE 2 手冻 %+v（%v），期望 今 1 / 昨 1 —— 第八项漏到了 NoUseHistory 上", fr, err)
	}
}

// TestUndatedCloseAsYesterdayStaysOutOfValidation 钉住口径只管记账：快期口径下 Submit 仍拒 UseHistory 上的裸 CLOSE（有昨仓也拒）。
//
// ⚠️ 八项校验是本库的规则；快期柜台接受的这种单走 PlaceAccepted / ApplyTrade。哪天校验也跟了口径，这里红 ——
// 那得先改 design.md §10 与 order.checkClosable 的拒因原话，不是顺手。
func TestUndatedCloseAsYesterdayStaysOutOfValidation(t *testing.T) {
	s := submitSim(t, kqChoices(), "1000000")
	if err := s.ApplyTrade(simDay, trade(t, "SHFE.ag2702", types.Buy, types.Open, "15460", 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(simDay, settlePx(t, "SHFE.ag2702", "15500"), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, "SHFE.ag2702", "15500", "15500")
	_, err := s.Submit(simNext, wall(t, "2026-09-16 10:00"), req(t, "SHFE.ag2702", types.Sell, types.Close, "15500", 1))
	var rej *match.RejectedError
	if !errors.As(err, &rej) || rej.Rejection.Check != order.CheckClosable {
		t.Errorf("⚠️ 快期口径下有昨仓的 UseHistory 裸 CLOSE，Submit 要拒在可平量：%v", err)
	}
}
