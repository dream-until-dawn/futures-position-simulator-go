package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expShapeBothSides 把一个**已有昨仓**的合约摆成「多头今昨并存 + 空头今仓」。
//
// 它自己不测任何东西 —— 它造的是**别的实验缺的那个前提**。
// 一次开两手（1 手多、1 手空）之后，同一个合约上同时具备：
//
//	多头 今1/昨N  → 今昨**并存**。position-frozen 至今的两份样本都只有一侧有量，
//	                所以「柜台按今昨拆冻结」与「柜台把冻结镜像到有量的那一侧」
//	                **分不开**（kq_facts 31 声明的盲区）。并存才分得开。
//	多头 今1/昨N  → 裸 CLOSE 冻的是哪一边，直接就是**消耗顺序**的答案
//	空头 今1/昨0  → volume_short_frozen 与 _short_frozen_today 至今零观测；
//	                空头三个字段够不着的唯一原因就是种子是纯多头
//
// ⚠️ `volume_short_frozen_his` 仍然够不着 —— 它要一手**过夜的空仓**，
// 而昨仓只能等结算，造不出来。少一个就是少一个，不含糊过去。
//
// # ⚠️ 这个实验会真的建仓
//
// 用对手价成交，不是挂不上的限价 —— 它要的就是成交。
// 两手都是**今仓**，明天结算后会变成昨仓；跑完记得决定留不留。
func (r *Runner) expShapeBothSides(ctx context.Context) error {
	if len(r.Symbols) != 1 {
		return fmt.Errorf("本实验需要 -symbols 指定**恰好一个**合约，得到 %d 个",
			len(r.Symbols))
	}
	sym := r.Symbols[0]
	cli := r.cli
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}
	if _, ok := cli.WaitQuoteReady(sym, 30*time.Second); !ok {
		return fmt.Errorf("%s 行情未就绪，定不出对手价", sym)
	}

	before := cli.PositionOf(sym)
	if before == nil {
		return fmt.Errorf("读不到 %s 的持仓截面", sym)
	}
	if err := shapePrecondition(before); err != nil {
		return err
	}
	lh := kq.MustNum(before, "volume_long_his")

	r.Logf("")
	r.Logf("== 摆形状：多头今昨并存 + 空头今仓 ==")
	r.Logf("  ⚠️ 本实验**真的建仓**（对手价成交），不是挂不上的限价。")
	r.Logf("  %s 当前：多今%.0f/多昨%.0f 空今%.0f/空昨%.0f",
		sym, kq.MustNum(before, "volume_long_today"), lh,
		kq.MustNum(before, "volume_short_today"), kq.MustNum(before, "volume_short_his"))

	for _, leg := range []struct {
		dir kq.Direction
		why string
	}{
		{kq.Buy, fmt.Sprintf("多头今仓 1 手 —— 与已有的 %.0f 手昨仓并存，"+
			"这是「冻结按不按今昨拆」和「裸平消耗顺序」共同缺的那个前提", lh)},
		{kq.Sell, "空头今仓 1 手 —— volume_short_frozen 与 _today 至今零观测，" +
			"够不着的唯一原因就是种子是纯多头"},
	} {
		r.Logf("")
		r.Logf("  开 %s 1 手：%s", leg.dir, leg.why)
		st, err := r.openOneLot(sym, leg.dir)
		if err != nil {
			// ⚠️ 半途失败时**已成交的那一腿仍在账上**。说清楚，别让人以为回滚了。
			return fmt.Errorf("开 %s 失败：%w —— ⚠️ **前面成交的腿仍在账上**，"+
				"下一步之前先看一眼持仓", leg.dir, err)
		}
		r.Logf("    成交（%s）", st.OrderID)
	}

	cli.WaitTrade(5 * time.Second)
	after := cli.PositionOf(sym)
	r.Logf("")
	r.Logf("  %s 现在：多今%.0f/多昨%.0f 空今%.0f/空昨%.0f",
		sym, kq.MustNum(after, "volume_long_today"), kq.MustNum(after, "volume_long_his"),
		kq.MustNum(after, "volume_short_today"), kq.MustNum(after, "volume_short_his"))

	// ⚠️ 形状没摆成就说出来。下一个实验会在一个不成立的前提上跑，
	// 而它跑出来的结论看起来与真结论一模一样。
	if err := shapeReached(after); err != nil {
		r.Logf("  ⚠️ **形状没摆成**：%v", err)
		r.Logf("     position-frozen 此时跑出来的东西不回答那两个盲区。")
	} else {
		r.Logf("  ✓ 形状成了。接着跑：oracle probe -exp position-frozen -symbols %s", sym)
	}
	r.Logf("  ⚠️ volume_short_frozen_his 仍够不着 —— 它要一手**过夜的空仓**，" +
		"昨仓只能等结算，今晚造不出来。")

	return r.dump("shape-both-sides", fmt.Sprintf(
		"摆形状：%s 上开 1 手多 + 1 手空（对手价成交），"+
			"目的是造出「多头今昨并存」与「空头今仓」两个前提。"+
			"⚠️ 本份是**建仓后**的稳态，两手都是今仓", sym))
}

// shapePrecondition 查前提：必须已有多头昨仓，且两边都还没有今仓。
//
// ⚠️ 已有今仓时再开一手，「今昨并存」照样成立，但**并存是本来就有的**，
// 这个实验就没做任何事 —— 而它的日志读起来完全一样。
func shapePrecondition(p map[string]any) error {
	lh, ok := kq.Num(p, "volume_long_his")
	if !ok {
		return fmt.Errorf("读不到 volume_long_his —— ⚠️ 读不到不当成零")
	}
	if lh <= 0 {
		return fmt.Errorf("⚠️ 多头**没有昨仓**（volume_long_his=%.0f）—— "+
			"本实验要造的是「今昨并存」，没有昨仓就只是开了一手今仓，"+
			"那个盲区一点没动。昨仓只能等结算，换一个有昨仓的合约", lh)
	}
	lt, okT := kq.Num(p, "volume_long_today")
	st, okS := kq.Num(p, "volume_short_today")
	if !okT || !okS {
		return fmt.Errorf("读不到今仓字段 —— ⚠️ 读不到不当成零")
	}
	if lt > 0 || st > 0 {
		return fmt.Errorf("⚠️ 已有今仓（多今%.0f 空今%.0f）—— "+
			"此时再开一手，「并存」是本来就有的，本实验什么都没造出来，"+
			"而它的日志读起来完全一样", lt, st)
	}
	return nil
}

// shapeReached 查形状真的摆成了。
func shapeReached(p map[string]any) error {
	need := []struct {
		key  string
		want string
	}{
		{"volume_long_today", "多头今仓"},
		{"volume_long_his", "多头昨仓"},
		{"volume_short_today", "空头今仓"},
	}
	for _, n := range need {
		v, ok := kq.Num(p, n.key)
		if !ok {
			return fmt.Errorf("%s（%s）读不到", n.key, n.want)
		}
		if v <= 0 {
			return fmt.Errorf("%s（%s）是 %.0f，应当大于 0", n.key, n.want, v)
		}
	}
	return nil
}
