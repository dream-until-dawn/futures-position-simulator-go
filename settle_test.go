package futsim

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

const simNext = types.TradingDay(20260916)

func settlePx(t *testing.T, kv ...string) map[types.InstrumentID]decimal.Decimal {
	t.Helper()
	out := map[types.InstrumentID]decimal.Decimal{}
	for i := 0; i+1 < len(kv); i += 2 {
		out[simInst(t, kv[i])] = dec(kv[i+1])
	}
	return out
}

// TestSettleRollsRebasesAndCarries 手算一次结算：兑现、滚仓、推基线、次日占用按昨结算价。
func TestSettleRollsRebasesAndCarries(t *testing.T) {
	s := newSim(t)
	mark(t, s, "DCE.m2701", "3361", "3384")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3360", 2)); err != nil {
		t.Fatal(err)
	}
	// 今结算 3370：持仓盈亏 (3370−3360)×10×2 = 200 兑现；结存 100000 − 3 + 200 = 100197
	if err := s.Settle(simDay, settlePx(t, "DCE.m2701", "3370"), simNext); err != nil {
		t.Fatal(err)
	}
	a := s.Account()
	if a.TradingDay != simNext || !a.PreBalance.Equal(dec("100197")) {
		t.Errorf("结算后交易日 %d、上日结存 %s，期望 %d / 100197", a.TradingDay, a.PreBalance, simNext)
	}
	// 次日：持仓盈亏 0；占用按昨结算价 3370×10×2×0.1 = 6740（CTP 预设：昨仓用昨结算价，§13 #1）
	wantAccount(t, s, "结算后", "100197", "93457", "6740", "0", "0", "0")
	p, _ := s.Position(simInst(t, "DCE.m2701"), types.Speculation)
	side, _ := p.Side(types.Buy)
	if p.VolumeHistory(types.Buy) != 2 || p.VolumeToday(types.Buy) != 0 {
		t.Errorf("⚠️ 结算后应是昨 2 今 0，得到 昨 %d 今 %d", p.VolumeHistory(types.Buy), p.VolumeToday(types.Buy))
	}
	for _, l := range side.Lots() {
		if !l.Basis.Equal(dec("3370")) || !l.OpenPrice.Equal(dec("3360")) {
			t.Errorf("⚠️ 结算后基线 %s（期望 3370）、开仓价 %s（期望 3360 不变）", l.Basis, l.OpenPrice)
		}
	}

	// 次日行情：昨结算价要等于刚用的结算价
	markOn(t, s, simNext, "DCE.m2701", "3380", "3370")
	wantAccount(t, s, "次日最新价 3380", "100397", "93457", "6740", "0", "0", "200")
	err := s.Mark(simNext, Quote{Instrument: simInst(t, "DCE.m2701"), PreSettlement: dec("3384"), HasPreSettlement: true})
	if err == nil {
		t.Error("⚠️ 次日行情给的昨结算价 3384 ≠ 结算用的 3370，没有报错 —— 喂错结算价会静默错下去")
	}
}

// TestSettleRefusesMissingPriceAndLeavesNoTrace 钉住结算价缺失 / 非正时报错、状态不动。
func TestSettleRefusesMissingPriceAndLeavesNoTrace(t *testing.T) {
	s := newSim(t)
	mark(t, s, "DCE.m2701", "3361", "3384")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	before := s.Account()
	// ⚠️ 逐条**点名是哪一层拦的**，不只断言「报错了」。
	//
	// 门面这两道（`!ok` 与 `!px.IsPositive()`）与 position.Settle 自己的零价 / 非正价检查是**三层冗余**：
	// 只断言 err != nil 的话，去掉门面任何一道，内层都会顺手接住，测试照样绿 ——
	// 破坏 527 与 634 实测都是「仍然绿」，而那不是盲区声明，是这条测试分不出层。
	// ⇒ 断言到门面自己那句话上，去掉哪一道都会表现为**换了一句错**。
	for name, c := range map[string]struct {
		px   map[types.InstrumentID]decimal.Decimal
		want string
	}{
		"缺结算价":    {map[types.InstrumentID]decimal.Decimal{}, "有持仓而没有今结算价"},
		"结算价为零":   {settlePx(t, "DCE.m2701", "0"), "今结算价 0 不为正"},
		"只给了别的合约": {settlePx(t, "SHFE.ag2702", "15785"), "有持仓而没有今结算价"},
	} {
		err := s.Settle(simDay, c.px, simNext)
		if err == nil {
			t.Errorf("⚠️ %s：结算成功了", name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("⚠️ %s 报错了，但不是门面那一道拦的（要含 %q）：%v —— "+
				"落到 position 那层说明门面自己的检查被绕过了，而两层都在时这条差别是唯一看得见的", name, c.want, err)
		}
		if !sameSnapshot(s.Account(), before) {
			t.Errorf("⚠️ %s 失败之后账户变了", name)
		}
		if p, _ := s.Position(simInst(t, "DCE.m2701"), types.Speculation); p.VolumeToday(types.Buy) != 1 || p.Day != simDay {
			t.Errorf("⚠️ %s 失败之后持仓变了", name)
		}
	}
	if err := s.Settle(simDay, settlePx(t, "DCE.m2701", "3370"), simDay); err == nil {
		t.Error("⚠️ 下一交易日等于当前交易日也结算了")
	}
	// ⚠️ 「下一交易日不晚于当前」这一句门面与 position 逐字相同，分不出层。
	// 而**空仓**时 position.Settle 根本不会被调用（循环体一次都不进）——
	// 于是这一次只有门面自己那道能拦，层次由**场景**而不是措辞分开。
	flat := newSim(t)
	if err := flat.Settle(simDay, map[types.InstrumentID]decimal.Decimal{}, simDay); err == nil {
		t.Error("⚠️ 空仓时下一交易日等于当前交易日也结算了 —— " +
			"这一档 position.Settle 不会被调用，拦不住就是门面自己那道没了")
	}
	// ⚠️ 这一条既是反向对照（免得上面那条靠「空仓一律报错」通过），
	// 也是 LeavesNoTrace 的那一半 —— 而它比上面那条更有判别力：
	// 门面那道拦在一切之前，**失败不碰状态**；把它去掉，同一档会一路走到更深处才失败，
	// 并在途中把模拟器弄**失效**，于是这条正常结算再也跑不了。
	if err := flat.Settle(simDay, map[types.InstrumentID]decimal.Decimal{}, simNext); err != nil {
		t.Errorf("⚠️ 空仓那一档失败之后模拟器不该受影响，而随后一次正常的结算失败了：%v —— "+
			"门面那道「下一交易日不晚于当前」拦在一切之前，失败得干净；它没了就会走到更深处才失败", err)
	}
}

// notNeededRules 把一份规则数据里所有合约的 PositionDateType 换成 NotNeeded —— 模拟两个手写 Provider。
type notNeededRules struct{ refdata.Provider }

func (r notNeededRules) Instrument(id types.InstrumentID) (refdata.Instrument, error) {
	inst, err := r.Provider.Instrument(id)
	inst.PositionDateType = refdata.PositionDateNotNeeded
	return inst, err
}

// TestSettleRefusesNotNeededPositions 把两个手写 Provider（Rebuild 的 specRules、ctpfixture 的 oneInstrumentRules）
// 注释里的前提「这条路永不结算」变成断言：给 NotNeeded 的持仓上结算报错、状态不动（评审 F1 风险 3）。
func TestSettleRefusesNotNeededPositions(t *testing.T) {
	s, err := New(Config{Day: simDay, PreBalance: dec("100000"), Rules: notNeededRules{simRules(t)}, Choices: ctpChoices()})
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, "DCE.m2701", "3361", "3384")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	before := s.Account()
	err = s.Settle(simDay, settlePx(t, "DCE.m2701", "3370"), simNext)
	if err == nil || !strings.Contains(err.Error(), "无法结算") {
		t.Errorf("⚠️ NotNeeded 的持仓上结算要报「无法结算」：%v", err)
	}
	if !sameSnapshot(s.Account(), before) {
		t.Error("⚠️ 报错之后账户变了")
	}
}

// TestSettleDropsFlatPositionsAndTheirPrices 钉住空仓在结算时丢掉，次日没仓的合约要重新 Mark。
func TestSettleDropsFlatPositionsAndTheirPrices(t *testing.T) {
	s := newSim(t)
	mark(t, s, "DCE.m2701", "3361", "3384")
	for _, tr := range []struct {
		off types.Offset
		dir types.Direction
	}{{types.Open, types.Buy}, {types.CloseToday, types.Sell}} {
		if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", tr.dir, tr.off, "3360", 1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Settle(simDay, nil, simNext); err != nil {
		t.Fatalf("全部平掉之后不给结算价也应能结算：%v", err)
	}
	if _, ok := s.Position(simInst(t, "DCE.m2701"), types.Speculation); ok {
		t.Error("⚠️ 空仓的持仓对象被带进了次日")
	}
	// 次日没 Mark 就开仓：昨结算价是上一交易日的，不许沿用
	err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Buy, types.Open, "3360", 1))
	if err == nil || !strings.Contains(err.Error(), "先 Mark") {
		t.Errorf("⚠️ 次日没 Mark 就开仓要报「先 Mark」：%v", err)
	}
}
