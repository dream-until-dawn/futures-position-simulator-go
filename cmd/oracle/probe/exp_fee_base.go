package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expFeeBase 问：按成交额收的那档手续费，基准价是**成交价**还是**昨结算价**。
//
// ⚠️ 这个区别不是细枝末节。真实 CTP 的按额手续费用**成交价**；
// 若快期模拟用昨结算价，那它算出的手续费在盘中会与真实柜台系统性地差一截，
// 而这个差在「同一天内」永远不变——正因为不变，它极容易被当成对得上。
//
// 判据：同一合约、**两个不同成交价**各开一手，比较两笔的手续费。
//
//	两笔费额相同   → 基准与成交价无关（昨结算价，或每手固定额）
//	两笔费额不同   → 基准是成交价
//
// ⚠️ 两个不同成交价怎么拿到，是这条实验成败的关键。
// 第一版靠「等最新价动」，跑出来两手都成交在 738.50——最新价动了，
// **卖一没动**，我打的是对手价，所以成交价一样。样本无判别力，
// 守卫拦下了，但那一趟白跑。
//
// 现在改成 **一手买、一手卖**：买单成交在卖一，卖单成交在买一，
// 只要有价差，两个成交价就必然不同，不用等行情。
// 两笔都是开仓，走同一档费率，可比。
func (r *Runner) expFeeBase(ctx context.Context) error {
	if len(r.Symbols) != 1 {
		return fmt.Errorf("本实验需要**恰好一个**合约（同合约两次不同成交价），得到 %d 个", len(r.Symbols))
	}
	sym := r.Symbols[0]
	cli := r.cli
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}
	p := cli.PositionOf(sym)
	if v := kq.MustNum(p, "volume_long") + kq.MustNum(p, "volume_short"); v > 0 {
		return fmt.Errorf("%s 上已有 %.0f 手持仓，本实验要求该合约空仓", sym, v)
	}

	r.Logf("")
	r.Logf("== 手续费基准价：成交价 还是 昨结算价 ==")

	// 第一手。
	c0 := kq.MustNum(cli.Account(), "commission")
	if _, err := r.openOneLot(sym, kq.Buy); err != nil {
		return err
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		return kq.MustNum(cli.PositionOf(sym), "volume_long_today") >= 1
	}) {
		return fmt.Errorf("第一手成交后未见持仓")
	}
	cli.WaitTrade(2 * time.Second)
	c1 := kq.MustNum(cli.Account(), "commission")
	p = cli.PositionOf(sym)
	px1 := kq.MustNum(p, "open_price_long")
	cost1 := kq.MustNum(p, "open_cost_long")
	fee1 := c1 - c0
	r.Logf("  第一手：成交价=%.2f  手续费=%.4f", px1, fee1)

	// 第二手：反方向开仓。买单吃卖一、卖单吃买一，有价差就必然是两个成交价。
	q, _ := cli.QuoteOf(sym)
	r.Logf("  盘口 买一=%.2f 卖一=%.2f，第二手反方向开仓以取到不同成交价", q.BidPrice1, q.AskPrice1)
	if _, err := r.openOneLot(sym, kq.Sell); err != nil {
		_ = r.flatten(sym, kq.Buy)
		return err
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		return kq.MustNum(cli.PositionOf(sym), "volume_short_today") >= 1
	}) {
		_ = r.flatten(sym, kq.Buy)
		return fmt.Errorf("第二手成交后未见空头持仓")
	}
	cli.WaitTrade(2 * time.Second)
	c2 := kq.MustNum(cli.Account(), "commission")
	fee2 := c2 - c1

	p = cli.PositionOf(sym)
	px2 := kq.MustNum(p, "open_price_short")
	mult := 0.0
	if px1 > 0 {
		mult = cost1 / px1
	}
	r.Logf("  第二手：成交价=%.2f（空头开仓均价）  手续费=%.4f", px2, fee2)

	if err := r.dump("exp-fee-base", "手续费基准价：同合约两个不同成交价"); err != nil {
		return err
	}
	e1 := r.flatten(sym, kq.Buy)
	e2 := r.flatten(sym, kq.Sell)
	if e1 != nil || e2 != nil {
		r.Logf("  ⚠️ 收尾平仓失败：%v / %v", e1, e2)
	}

	r.Logf("")
	if px1 <= 0 || px2 <= 0 {
		return fmt.Errorf("⚠️ 成交价反解失败（%.2f / %.2f），本次结论作废", px1, px2)
	}
	if px1 == px2 {
		// ⚠️ 买一 == 卖一（无价差）时两手仍会成交在同一个价上——样本没有判别力。
		return fmt.Errorf("⚠️ 两手成交在同一个价 %.2f 上（盘口无价差），"+
			"「费额相同」在此样本上什么都不说明。**判据不成立**", px1)
	}
	r.Logf("  两手成交价 %.2f vs %.2f，差 %.2f", px1, px2, px2-px1)
	r.Logf("  两手手续费 %.4f vs %.4f，差 %.4f", fee1, fee2, fee2-fee1)
	r.Logf("")

	q0, _ := cli.QuoteOf(sym)
	pre := q0.PreSettlement
	switch {
	case nearlyEqual(fee1, fee2):
		r.Logf("  结论：成交价不同而手续费相同 → **基准与成交价无关**")
		if pre > 0 && mult > 0 {
			r.Logf("        以昨结算价 %.2f 反解：费率 = %.4f / (%.2f × %.0f) = %.10f",
				pre, fee1, pre, mult, fee1/(pre*mult))
		}
		r.Logf("        ⚠️ 这与真实 CTP 不同：CTP 的按额手续费用**成交价**。")
		r.Logf("           差额在同一天内是常数，因此很容易被误认为「对上了」。")
		r.Logf("        ⚠️ 「按昨结算价比例」与「每手固定额」在本实验里仍分不开——")
		r.Logf("           两者都与成交价无关。分开它们要看跨合约的费率是否一致。")
	default:
		ra, rb := fee1/(px1*mult), fee2/(px2*mult)
		r.Logf("  结论：手续费随成交价变 → **基准是成交价**，与真实 CTP 一致")
		r.Logf("        反解费率：%.10f vs %.10f", ra, rb)
		if !nearlyEqual(ra, rb) {
			r.Logf("        ⚠️ 但两笔反解的费率不相等 —— 说明还有别的成分（最低收费？取整？）")
			r.Logf("           **本实验不收敛**，不得写进规则文档的实测栏。")
		}
	}
	return nil
}
