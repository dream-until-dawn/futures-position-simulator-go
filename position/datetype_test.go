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

// TestNewRefusesUnknownButAcceptsNotNeeded 断言**构造期**的那道关口。
//
// ⚠️ 这道关口一度不存在，而它不存在的理由是一次**假的取舍**：
// 第一版 New 拦零值，于是只重放、永不结算的调用方被逼着为一个用不上的
// 参数编一个值 —— 编出来的值会被后来的人当成实测值。我因此把关口整个撤了。
//
// 评审指出第三条路：**零值同时表示「忘了传」和「不需要」，才是选不了的全部原因。**
// 加一个非零的 PositionDateNotNeeded，两件事就分开了：
//
//	New   拒绝零值（忘了传 → 建仓即报）
//	New   接受 NotNeeded（显式声明「我不结算」，不编任何数据）
//	Settle 两个都拒
func TestNewRefusesUnknownButAcceptsNotNeeded(t *testing.T) {
	inst, err := types.ParseSymbol("SHFE.rb2701", types.NewTradingDay(2026, 9, 8))
	if err != nil {
		t.Fatal(err)
	}
	day := types.NewTradingDay(2026, 9, 8)

	if _, err := New(inst, types.Speculation, day, refdata.PositionDateUnknown); err == nil {
		t.Error("⚠️ 零值 PositionDateType 建仓成功了 —— " +
			"「忘了传」应当在**构造期**就报，而不是滑到结算才报")
	} else {
		// ⚠️ 报错必须指出**那条出路**，否则读的人只会去编一个值 ——
		// 那正是这道关口第一版被撤掉的原因。
		if !strings.Contains(err.Error(), "PositionDateNotNeeded") {
			t.Errorf("⚠️ 错误信息里没提 PositionDateNotNeeded —— "+
				"不给出路的话，读的人会去编一个值：%v", err)
		}
		if !strings.Contains(err.Error(), "不要编一个值") {
			t.Errorf("⚠️ 错误信息里没写「不要编一个值」：%v", err)
		}
	}

	// 显式声明「用不到」必须放行 —— 否则又回到逼人编数据。
	if _, err := New(inst, types.Speculation, day, refdata.PositionDateNotNeeded); err != nil {
		t.Errorf("⚠️ 显式声明 PositionDateNotNeeded 却建不了仓：%v —— "+
			"那等于把「不需要」也当成「忘了」，这道关口就又变回假取舍了", err)
	}
	for _, ok := range []refdata.PositionDateType{refdata.UseHistory, refdata.NoUseHistory} {
		if _, err := New(inst, types.Speculation, day, ok); err != nil {
			t.Errorf("⚠️ %v 建仓失败：%v", ok, err)
		}
	}
}

// TestNotNeededNeverEntersRefdata 断言「本路径用不到」**进不了规则数据**。
//
// ⚠️ 它是给 position.New 的调用方用的声明，不是合约的属性。
// 一份规则数据里出现它是自相矛盾的：规则数据的存在理由就是承载这个字段。
// ⚠️ 而它一旦落进快照，posDateToken 会给出空串，
// 于是写出一份**能存下来、加载时才炸**的快照。
func TestNotNeededNeverEntersRefdata(t *testing.T) {
	i := refdata.Instrument{
		VolumeMultiple:   decimal.NewFromInt(10),
		PriceTick:        decimal.NewFromInt(1),
		PositionDateType: refdata.PositionDateNotNeeded,
	}
	err := i.Validate()
	if err == nil {
		t.Fatal("⚠️ PositionDateNotNeeded 通过了合约规格校验 —— " +
			"它会落进快照，而 posDateToken 对它给空串，" +
			"写出一份能存下来、加载时才炸的快照")
	}
	if !strings.Contains(err.Error(), "不是合约的属性") {
		t.Errorf("报错了但没说清是**谁的**声明：%v", err)
	}
}

// TestSettleRefusesNotNeeded 断言「本路径用不到」**结算时**照样报错。
//
// ⚠️ 两道关口分工不同，缺一条都会漏：
//
//	New    拦零值 —— 「忘了传」在构造期就报
//	Settle 拦零值**与** NotNeeded —— 「我不结算」不等于「可以结算」
//
// 少了 Settle 这道，一个显式声明「不结算」的持仓真去结算时会
// 悄悄按某一种处理，而两种的结果不同。
func TestSettleRefusesNotNeeded(t *testing.T) {
	// ⚠️ 用 NotNeeded 构造：New 现在放行它（那是显式声明），
	// 而 Settle 必须照样拒 —— 「我不结算」与「可以结算」是两回事。
	p := dt(t, refdata.PositionDateNotNeeded)
	err := p.Settle(types.NewTradingDay(2026, 9, 8),
		decimal.RequireFromString("3163"), types.NewTradingDay(2026, 9, 9))
	if err == nil {
		t.Fatal("⚠️ PositionDateType 是 NotNeeded 却结算成功了 —— " +
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
