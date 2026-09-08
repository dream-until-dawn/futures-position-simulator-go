package position

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func newPos(t *testing.T, day types.TradingDay) *Position {
	t.Helper()
	inst, err := types.ParseNative(types.SHFE, "rb2701", day)
	if err != nil {
		t.Fatalf("解析合约失败: %v", err)
	}
	p, err := New(inst, types.Speculation, day, refdata.UseHistory)
	if err != nil {
		t.Fatalf("建仓失败: %v", err)
	}
	return p
}

// TestAvgPriceIsLossySoDetailMustBeKept 把「均价是有损压缩」钉成测试。
//
// ⚠️ 它证明的不是 bug，是决策 10 存在的**理由**：
// 两组均价相同的持仓，逐笔对冲口径的平仓盈亏不同，而两个结果都不会报错。
// 只存均价，这个区别就永远算不回来了。
func TestAvgPriceIsLossySoDetailMustBeKept(t *testing.T) {
	const day = types.TradingDay(20260907)

	a := newPos(t, day)
	if err := a.Open(types.Buy, day, d("100"), 2); err != nil {
		t.Fatal(err)
	}
	if err := a.Open(types.Buy, day, d("130"), 1); err != nil {
		t.Fatal(err)
	}

	b := newPos(t, day)
	if err := b.Open(types.Buy, day, d("110"), 3); err != nil {
		t.Fatal(err)
	}

	sa, _ := a.Side(types.Buy)
	sb, _ := b.Side(types.Buy)
	avgA, okA := sa.AvgOpenPrice()
	avgB, okB := sb.AvgOpenPrice()
	if !okA || !okB {
		t.Fatal("均价应当存在")
	}
	if !avgA.Equal(avgB) {
		t.Fatalf("前提不成立：两组均价应相同，实为 %s 与 %s", avgA, avgB)
	}

	// 各平 1 手（先开先平），比较逐笔对冲口径的基线。
	ra, err := a.Close(types.Buy, types.Close, day, 1, FIFO)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := b.Close(types.Buy, types.Close, day, 1, FIFO)
	if err != nil {
		t.Fatal(err)
	}
	if len(ra.Consumed) != 1 || len(rb.Consumed) != 1 {
		t.Fatalf("各应消耗一片，实为 %d / %d", len(ra.Consumed), len(rb.Consumed))
	}
	if ra.Consumed[0].OpenPrice.Equal(rb.Consumed[0].OpenPrice) {
		t.Fatalf("⚠️ 两组的逐笔对冲基线相同（都是 %s）—— "+
			"那样这条测试就没有判别力了，明细没起作用", ra.Consumed[0].OpenPrice)
	}
	if !ra.Consumed[0].OpenPrice.Equal(d("100")) {
		t.Errorf("A 组先开先平应消耗 @100 那笔，实为 %s", ra.Consumed[0].OpenPrice)
	}
	if !rb.Consumed[0].OpenPrice.Equal(d("110")) {
		t.Errorf("B 组应消耗 @110，实为 %s", rb.Consumed[0].OpenPrice)
	}
}

// TestSettleRollsTodayIntoHistory 覆盖唯一把「今」变成「昨」的地方。
func TestSettleRollsTodayIntoHistory(t *testing.T) {
	const d1, d2 = types.TradingDay(20260907), types.TradingDay(20260908)
	p := newPos(t, d1)
	if err := p.Open(types.Buy, d1, d("3150"), 2); err != nil {
		t.Fatal(err)
	}
	if p.VolumeToday(types.Buy) != 2 || p.VolumeHistory(types.Buy) != 0 {
		t.Fatalf("结算前应是今仓 2 / 昨仓 0，实为 %d / %d",
			p.VolumeToday(types.Buy), p.VolumeHistory(types.Buy))
	}
	if err := p.Settle(d1, d("3160"), d2); err != nil {
		t.Fatal(err)
	}
	if p.VolumeToday(types.Buy) != 0 || p.VolumeHistory(types.Buy) != 2 {
		t.Fatalf("结算后应是今仓 0 / 昨仓 2，实为 %d / %d",
			p.VolumeToday(types.Buy), p.VolumeHistory(types.Buy))
	}

	s, _ := p.Side(types.Buy)
	basis, _ := s.AvgBasis()
	open, _ := s.AvgOpenPrice()
	if !basis.Equal(d("3160")) {
		t.Errorf("⚠️ 昨仓的逐日盯市基线应推进到结算价 3160，实为 %s", basis)
	}
	if !open.Equal(d("3150")) {
		t.Errorf("⚠️ 原始成交价必须**永不改变**（逐笔对冲要它），期望 3150，实为 %s", open)
	}
}

// TestForgotToSettleIsLoud 是静默风险第 1 条的守卫。
//
// ⚠️ 「今昨仓不滚动」的危害在于账永远是平的、全程没有动静。
// 这条测试要求它**变成一个 error**。
func TestForgotToSettleIsLoud(t *testing.T) {
	const d1, d2 = types.TradingDay(20260907), types.TradingDay(20260908)
	p := newPos(t, d1)
	if err := p.Open(types.Buy, d1, d("3150"), 1); err != nil {
		t.Fatal(err)
	}

	// 未结算就在下一交易日开仓 —— 必须报错。
	err := p.Open(types.Buy, d2, d("3160"), 1)
	if err == nil {
		t.Fatal("⚠️ 跨交易日未结算就开仓，本该报错")
	}
	if !strings.Contains(err.Error(), "尚未结算") {
		t.Errorf("报错应指出未结算，实为 %v", err)
	}

	// 平仓同样。
	if _, err := p.Close(types.Buy, types.Close, d2, 1, FIFO); err == nil {
		t.Error("⚠️ 跨交易日未结算就平仓，本该报错")
	}
	// 结算一次之后放行。
	if err := p.Settle(d1, d("3160"), d2); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(types.Buy, d2, d("3160"), 1); err != nil {
		t.Errorf("结算之后应放行，实为 %v", err)
	}
	// 重复结算同一天必须报错。
	if err := p.Settle(d1, d("3160"), d2); err == nil {
		t.Error("⚠️ 重复结算同一交易日本该报错 —— 重复执行会让基线多推进一次")
	}
}

// TestCloseVolumeCheckedPerPositionDate 断言可平量按今昨**分别**校验。
func TestCloseVolumeCheckedPerPositionDate(t *testing.T) {
	const d1, d2 = types.TradingDay(20260907), types.TradingDay(20260908)
	p := newPos(t, d1)
	if err := p.Open(types.Buy, d1, d("3150"), 3); err != nil { // 将成为昨仓 3 手
		t.Fatal(err)
	}
	if err := p.Settle(d1, d("3160"), d2); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(types.Buy, d2, d("3170"), 5); err != nil { // 今仓 5 手
		t.Fatal(err)
	}

	// ⚠️ 总持仓 8 手，但「平昨 4 手」必须被拒 —— 合并校验会放过它。
	if _, err := p.Close(types.Buy, types.CloseYesterday, d2, 4, FIFO); err == nil {
		t.Error("⚠️ 平昨 4 手超过昨仓 3 手，本该被拒（总持仓 8 手会让合并校验放过它）")
	}
	if _, err := p.Close(types.Buy, types.CloseToday, d2, 6, FIFO); err == nil {
		t.Error("平今 6 手超过今仓 5 手，本该被拒")
	}
	// 各自在量内则放行，且只动对应的一边。
	r, err := p.Close(types.Buy, types.CloseYesterday, d2, 3, FIFO)
	if err != nil {
		t.Fatalf("平昨 3 手应放行: %v", err)
	}
	if r.VolumeHistory != 3 || r.VolumeToday != 0 {
		t.Errorf("平昨应只消耗昨仓，实为 今 %d / 昨 %d", r.VolumeToday, r.VolumeHistory)
	}
	if p.VolumeToday(types.Buy) != 5 || p.VolumeHistory(types.Buy) != 0 {
		t.Errorf("平昨后应剩今仓 5 / 昨仓 0，实为 %d / %d",
			p.VolumeToday(types.Buy), p.VolumeHistory(types.Buy))
	}
}

// TestOverCloseIsRejectedNeverReversed 断言超量平仓**报错**，绝不反手。
func TestOverCloseIsRejectedNeverReversed(t *testing.T) {
	const day = types.TradingDay(20260907)
	p := newPos(t, day)
	if err := p.Open(types.Buy, day, d("3150"), 4); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Close(types.Buy, types.Close, day, 10, FIFO); err == nil {
		t.Fatal("⚠️ 拿 10 手去平 4 手，本该报错")
	}
	// 持仓必须原封不动，方向绝不能反。
	if p.VolumeToday(types.Buy) != 4 {
		t.Errorf("⚠️ 被拒的平仓不得改动持仓，期望多头 4 手，实为 %d", p.VolumeToday(types.Buy))
	}
	if p.VolumeToday(types.Sell) != 0 {
		t.Errorf("⚠️ 超量平仓绝不能开出反向仓位，实得空头 %d 手 —— "+
			"「想平仓，仓位反而变大、方向还没变」", p.VolumeToday(types.Sell))
	}
}

// TestCloseOrderUnmeasuredRefuses 断言未实测的消耗顺序**拒绝运行**。
//
// ⚠️ 三个候选在「平满」的样本上给出同一个结果，所以一个错的默认值可以长期
// 不被发现。零值必须报错，不能回退到「合理的默认」。
func TestCloseOrderUnmeasuredRefuses(t *testing.T) {
	const d1, d2 = types.TradingDay(20260907), types.TradingDay(20260908)
	p := newPos(t, d1)
	if err := p.Open(types.Buy, d1, d("3150"), 2); err != nil {
		t.Fatal(err)
	}
	if err := p.Settle(d1, d("3160"), d2); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(types.Buy, d2, d("3170"), 2); err != nil {
		t.Fatal(err)
	}

	_, err := p.Close(types.Buy, types.Close, d2, 1, CloseOrderUnmeasured)
	if err == nil {
		t.Fatal("⚠️ 消耗顺序未实测，本该拒绝运行而不是挑一个默认")
	}
	if !strings.Contains(err.Error(), "实验 4") {
		t.Errorf("报错应指向实验 4，实为 %v", err)
	}

	// 显式指定则放行，且三种顺序给出**不同**的结果 —— 这正是它需要被实测的原因。
	for _, tc := range []struct {
		order            CloseOrder
		wantToday, wantH int
	}{
		{YesterdayFirst, 0, 1},
		{TodayFirst, 1, 0},
		{FIFO, 0, 1}, // ⚠️ 与 YesterdayFirst 同值：昨仓本就比今仓早开
	} {
		q := newPos(t, d1)
		_ = q.Open(types.Buy, d1, d("3150"), 2)
		_ = q.Settle(d1, d("3160"), d2)
		_ = q.Open(types.Buy, d2, d("3170"), 2)
		r, err := q.Close(types.Buy, types.Close, d2, 1, tc.order)
		if err != nil {
			t.Errorf("%v 平仓失败: %v", tc.order, err)
			continue
		}
		if r.VolumeToday != tc.wantToday || r.VolumeHistory != tc.wantH {
			t.Errorf("%v：消耗 今 %d / 昨 %d，期望 今 %d / 昨 %d",
				tc.order, r.VolumeToday, r.VolumeHistory, tc.wantToday, tc.wantH)
		}
	}
}

// TestFIFOIndistinguishableFromYesterdayFirstHere 把实验 4 的**样本局限**钉成测试。
//
// ⚠️ 在「昨仓都比今仓早开」的样本上，FIFO 与 YesterdayFirst 给出同一个结果。
// 这条测试的作用是：将来若有人拿这种样本去「验证」实验 4 的结论，
// 这里写着它没有判别力。
func TestFIFOIndistinguishableFromYesterdayFirstHere(t *testing.T) {
	const d1, d2 = types.TradingDay(20260907), types.TradingDay(20260908)
	build := func() *Position {
		p := newPos(t, d1)
		_ = p.Open(types.Buy, d1, d("3150"), 2)
		_ = p.Settle(d1, d("3160"), d2)
		_ = p.Open(types.Buy, d2, d("3170"), 2)
		return p
	}
	a, _ := build().Close(types.Buy, types.Close, d2, 3, YesterdayFirst)
	b, _ := build().Close(types.Buy, types.Close, d2, 3, FIFO)
	if a.VolumeToday != b.VolumeToday || a.VolumeHistory != b.VolumeHistory {
		t.Fatalf("前提变了：本样本上两者本应同值，实为 (%d,%d) vs (%d,%d) —— "+
			"若确实分开了，说明样本形态变了，实验 4 的判别力说明要同步更新",
			a.VolumeToday, a.VolumeHistory, b.VolumeToday, b.VolumeHistory)
	}
}

// TestSettleRejectsZeroPrice 断言结算价为零**报错**。
func TestSettleRejectsZeroPrice(t *testing.T) {
	const d1, d2 = types.TradingDay(20260907), types.TradingDay(20260908)
	bad := []struct {
		px  string
		why string
	}{
		{"0", "⚠️ 免费日线源上 7.7%～98.6% 的结算价字段是 0，那是缺失的伪装"},
		{"-1", "负结算价"},
	}
	if len(bad) != 2 {
		t.Fatalf("反例数应为 2，实际 %d", len(bad))
	}
	for _, c := range bad {
		p := newPos(t, d1)
		_ = p.Open(types.Buy, d1, d("3150"), 1)
		if err := p.Settle(d1, d(c.px), d2); err == nil {
			t.Errorf("结算价 %s 本该报错（%s）", c.px, c.why)
		}
	}
}

// TestBothStandardsComputableFromConsumed 断言两套盈亏口径都能从 Consumed 算出来。
func TestBothStandardsComputableFromConsumed(t *testing.T) {
	const d1, d2 = types.TradingDay(20260907), types.TradingDay(20260908)
	p := newPos(t, d1)
	_ = p.Open(types.Buy, d1, d("3150"), 1) // 将成昨仓，基线推进到 3160
	_ = p.Settle(d1, d("3160"), d2)
	_ = p.Open(types.Buy, d2, d("3170"), 1) // 今仓，基线 = 开仓价 3170

	r, err := p.Close(types.Buy, types.Close, d2, 2, YesterdayFirst)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Consumed) != 2 {
		t.Fatalf("应消耗两片，实为 %d", len(r.Consumed))
	}
	closePx := d("3180")
	byTrade, byDate := decimal.Zero, decimal.Zero
	for _, l := range r.Consumed {
		v := decimal.NewFromInt(int64(l.Volume))
		byTrade = byTrade.Add(closePx.Sub(l.OpenPrice).Mul(v))
		byDate = byDate.Add(closePx.Sub(l.Basis).Mul(v))
	}
	// 逐笔对冲：(3180-3150) + (3180-3170) = 40
	// 逐日盯市：(3180-3160) + (3180-3170) = 30   ← 昨仓那片用的是昨结算价
	if !byTrade.Equal(d("40")) {
		t.Errorf("逐笔对冲口径应为 40，实为 %s", byTrade)
	}
	if !byDate.Equal(d("30")) {
		t.Errorf("逐日盯市口径应为 30，实为 %s", byDate)
	}
	if byTrade.Equal(byDate) {
		t.Error("⚠️ 两套口径在本样本上同值 —— 那样这条测试没有判别力")
	}
}

// TestHedgeFlagRequired 断言投机套保标志不能默认。
func TestHedgeFlagRequired(t *testing.T) {
	inst, _ := types.ParseNative(types.SHFE, "rb2701", 20260907)
	if _, err := New(inst, types.HedgeUnknown, 20260907, refdata.UseHistory); err == nil {
		t.Error("⚠️ 投机套保标志决定保证金率，未指定时本该报错")
	}
}
