package position

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func dt(t *testing.T, kind refdata.PositionDateType) *Position {
	t.Helper()
	inst, err := types.ParseSymbol("SHFE.rb2701", types.NewTradingDay(2026, 9, 8))
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(inst, types.Speculation, types.NewTradingDay(2026, 9, 8), kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Open(types.Buy, types.NewTradingDay(2026, 9, 8),
		decimal.RequireFromString("3150"), 3); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSettleRespectsPositionDateType 断言结算**按合约的类型分岔**，
// 而且两条路**给出不同的结果**。
//
// ⚠️ 后半句才是判据。只断言 UseHistory 那条，NoUseHistory 的分支
// 写成什么样都能通过；而两条路若碰巧给出同一个结果，
// 这个改动就等于没做，测试却是绿的。
//
// 实测依据 kq_facts 24（20260909 结算）：同一次结算之后
// `SHFE.rb2701` 多今0/多昨3，而 `DCE.m2701` 多今3/多昨0，
// 且账户层结算**已完成** —— 所以「大商所还没结算」被否掉了。
func TestSettleRespectsPositionDateType(t *testing.T) {
	d8 := types.NewTradingDay(2026, 9, 8)
	d9 := types.NewTradingDay(2026, 9, 9)
	settle := decimal.RequireFromString("3163")

	useHist := dt(t, refdata.UseHistory)
	noHist := dt(t, refdata.NoUseHistory)
	for _, p := range []*Position{useHist, noHist} {
		if err := p.Settle(d8, settle, d9); err != nil {
			t.Fatalf("%v 结算失败：%v", p.DateType(), err)
		}
	}

	// —— 今昨划分：两条路必须**不同** ——
	if got := useHist.VolumeHistory(types.Buy); got != 3 {
		t.Errorf("UseHistory 结算后昨仓 %d 手，应为 3 —— 今仓没变成昨仓", got)
	}
	if got := useHist.VolumeToday(types.Buy); got != 0 {
		t.Errorf("UseHistory 结算后今仓 %d 手，应为 0", got)
	}
	if got := noHist.VolumeHistory(types.Buy); got != 0 {
		t.Errorf("⚠️ NoUseHistory 结算后昨仓 %d 手，应为 0 —— "+
			"那类合约的持仓**留在今仓**（kq_facts 24 实测）", got)
	}
	if got := noHist.VolumeToday(types.Buy); got != 3 {
		t.Errorf("⚠️ NoUseHistory 结算后今仓 %d 手，应为 3", got)
	}
	// ⚠️ 判别力：两条路给出的今昨划分必须真的不一样。
	if useHist.VolumeHistory(types.Buy) == noHist.VolumeHistory(types.Buy) {
		t.Fatal("⚠️ 两种 PositionDateType 结算后昨仓手数**相同** —— " +
			"分岔等于没分，而上面每一条断言都能被同一段代码满足")
	}

	// —— 基线：两条路必须**相同**，都推进到结算价 ——
	//
	// ⚠️ 逐日盯市是资金层面的事，与今昨划分是两回事。
	// 混为一谈会让 NoUseHistory 合约的持仓盈亏永远以开仓价为基线，
	// 而那与结算单对不上。
	for _, c := range []struct {
		name string
		p    *Position
	}{{"UseHistory", useHist}, {"NoUseHistory", noHist}} {
		s, err := c.p.Side(types.Buy)
		if err != nil {
			t.Fatal(err)
		}
		for i, l := range s.Lots() {
			if !l.Basis.Equal(settle) {
				t.Errorf("⚠️ %s 第 %d 笔的基线是 %s，应推进到结算价 %s —— "+
					"逐日盯市对**所有**合约成立，与今昨划分是两回事",
					c.name, i, l.Basis, settle)
			}
			// 逐笔对冲基线永不改变。
			if !l.OpenPrice.Equal(decimal.RequireFromString("3150")) {
				t.Errorf("⚠️ %s 第 %d 笔的 OpenPrice 被改成了 %s —— "+
					"它是逐笔对冲的基线，**永不改变**", c.name, i, l.OpenPrice)
			}
		}
	}
}

// TestSettleRefusesUnknownDateType 断言零值**结算时**报错。
//
// ⚠️ New 刻意**不**拦零值（理由见 New 的注释：那会逼只重放、
// 永不结算的调用方编一个值出来）。所以关口只有这一个，它必须真的在。
func TestSettleRefusesUnknownDateType(t *testing.T) {
	p := dt(t, refdata.PositionDateUnknown)
	err := p.Settle(types.NewTradingDay(2026, 9, 8),
		decimal.RequireFromString("3163"), types.NewTradingDay(2026, 9, 9))
	if err == nil {
		t.Fatal("⚠️ PositionDateType 是零值却结算成功了 —— " +
			"那意味着它悄悄按某一种处理了，而两种的结果不同")
	}
	// ⚠️ 报错要说得出**该去做什么**。只说「未指定」的话，
	// 读的人会去找哪里没传参，而真正的原因多半是「这个合约没人量过」。
	for _, want := range []string{"逐合约", "不许按交易所推", "kq_facts 24"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("⚠️ 错误信息里没有 %q —— 它要指向「去量」，不是「去传参」：\n%v",
				want, err)
		}
	}
	// 结算失败之后，持仓不许被改动过一半。
	if p.Day != types.NewTradingDay(2026, 9, 8) {
		t.Errorf("⚠️ 结算报错了，但交易日已经推进到 %s —— "+
			"半途而废的结算比不结算更坏", p.Day)
	}
	if p.VolumeHistory(types.Buy) != 0 {
		t.Error("⚠️ 结算报错了，但今仓已经被标成昨仓")
	}
}
