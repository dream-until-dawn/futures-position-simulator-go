package probe

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// solveBaseline 从两个盈亏字段反解出持仓盈亏用的价格基线，**不需要知道合约乘数**。
//
//	float_profit    = (last − openPx) × vol × mult
//	position_profit = (last − X)      × vol × mult
//	⇒ X = last − (last − openPx) × position_profit / float_profit
//
// 乘数与手数在相除时约掉了。这一点重要：免费行情网关不下发 volume_multiple，
// 而**为了拿一个约得掉的量去引入一整条规则数据链路，是把实验押在另一个未验证的东西上**。
//
// ok 为 false 表示分母太接近零，解不出来——⚠️ 那是「测不出」，不是「基线等于某个值」。
func solveBaseline(last, openPx, floatProfit, posProfit float64) (x float64, ok bool) {
	if math.Abs(floatProfit) < 1e-9 {
		return 0, false
	}
	return last - (last-openPx)*posProfit/floatProfit, true
}

// expBaselineFull 是实验 1b 与实验 2 的完整版：**必须有昨仓**。
//
// 今仓版（expMarginPrice）只能排除「连续重估」这一个候选，因为逐日盯市下
// 今仓的基线本就是开仓价，用最新价还是昨结算价都会随行情动。
// 有了昨仓，两个字段各自的基线就能分别解出来：
//
//	昨仓的 position_profit 基线 ≈ 昨结算价  → 逐日盯市（本模型的预测）
//	昨仓的 position_profit 基线 ≈ 开仓价    → 与 float_profit 同源，模型要改
//
//	margin_long_his 与 margin_long_today 是否不等 → 今昨保证金是否用不同的价
func (r *Runner) expBaselineFull(ctx context.Context) error {
	if len(r.Symbols) == 0 {
		return fmt.Errorf("实验 1b/2 需要 -symbols 指定一个**持有昨仓**的合约")
	}
	sym := r.Symbols[0]
	cli := r.cli

	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}
	his, err := requireYesterday(cli, sym, kq.Buy)
	if err != nil {
		return err
	}
	q, ok := cli.WaitQuoteReady(sym, 30*time.Second)
	if !ok {
		return fmt.Errorf("%s 行情未就绪（原因未确定：可能非交易时段，也可能订阅未生效）", sym)
	}

	p := cli.PositionOf(sym)
	today := kq.MustNum(p, "volume_long_today")
	openPx := kq.MustNum(p, "open_price_long")
	fp := kq.MustNum(p, "float_profit_long")
	pp := kq.MustNum(p, "position_profit_long")

	r.Logf("")
	r.Logf("== 实验 1b / 2：昨仓在场时，margin 与 position_profit 各自的价格基线 ==")
	r.Logf("  %s  昨仓=%.0f 今仓=%.0f", sym, his, today)
	r.Logf("  开仓均价=%.4f  昨结算=%.4f  最新=%.4f", openPx, q.PreSettlement, q.LastPrice)
	r.Logf("  float_profit=%.4f  position_profit=%.4f", fp, pp)

	// —— 判别力自查：两个候选价必须分得开 ——
	spread := math.Abs(openPx - q.PreSettlement)
	if openPx <= 0 || q.PreSettlement <= 0 {
		return fmt.Errorf("开仓价或昨结算价缺失，**判据不成立**")
	}
	if spread < q.PriceTick && q.PriceTick > 0 || spread == 0 {
		r.Logf("")
		r.Logf("  ⚠️ **判据不成立**：开仓价与昨结算价相差 %.4f，两个候选给出同一个数。", spread)
		r.Logf("     这不是「结论是某某」，是「这个样本测不出来」。换开仓价不同的样本重跑。")
		return r.dump("exp1b2-baseline-INCONCLUSIVE", "实验 1b/2：样本无判别力，未得结论")
	}

	r.Logf("")
	if x, ok := solveBaseline(q.LastPrice, openPx, fp, pp); ok {
		dOpen := math.Abs(x - openPx)
		dPre := math.Abs(x - q.PreSettlement)
		r.Logf("  【实验 2】反解出的 position_profit 基线 X = %.4f", x)
		r.Logf("     距开仓价 %.4f ／ 距昨结算价 %.4f", dOpen, dPre)
		switch {
		case dPre < dOpen:
			r.Logf("     → 基线是**昨结算价**，逐日盯市成立（本模型的预测）")
		case dOpen < dPre:
			r.Logf("     → 基线是**开仓价** ⚠️ 与 float_profit 同源，本项目对 ByDate 的理解要改")
		default:
			r.Logf("     ⚠️ 两者等距，**判据不成立**")
		}
		r.Logf("     （此解不依赖合约乘数——乘数与手数在相除时约掉了）")
	} else {
		// ⚠️ 解不出来是「测不出」，不是「基线等于开仓价」。
		r.Logf("  ⚠️ float_profit 太接近零，反解**无解**。这是「测不出」，不是任何结论。")
		r.Logf("     等行情离开开仓价更远时重跑。")
	}

	// —— 实验 1b：今昨保证金是否用不同的价 ——
	mHis := kq.MustNum(p, "margin_long_his")
	mToday := kq.MustNum(p, "margin_long_today")
	r.Logf("")
	r.Logf("  【实验 1b】margin_long_his=%.4f  margin_long_today=%.4f", mHis, mToday)
	switch {
	case his > 0 && today > 0 && mHis > 0 && mToday > 0:
		perHis, perToday := mHis/his, mToday/today
		r.Logf("     每手：昨仓 %.4f  今仓 %.4f  差 %.4f", perHis, perToday, perHis-perToday)
		if math.Abs(perHis-perToday) < 1e-6 {
			r.Logf("     → 今昨每手保证金相同 → 指向**候选 2**（今昨都用同一个价，很可能是昨结算价）")
		} else {
			r.Logf("     → 今昨每手保证金不同 → 指向**候选 1**（昨仓用昨结算价、今仓用开仓价）")
			r.Logf("       比值 昨/今 = %.6f，昨结算/开仓价 = %.6f（两者接近即坐实）",
				perHis/perToday, q.PreSettlement/openPx)
		}
	case today == 0:
		r.Logf("     ⚠️ 今仓为零，**这一项判据不成立**——只有昨仓时两个候选同值。")
		r.Logf("        先在今日开一手今仓再跑。")
	default:
		r.Logf("     ⚠️ 保证金字段缺失或为零，判据不成立")
	}

	return r.dump("exp1b2-baseline", "实验 1b/2：昨仓在场时的 margin 与 position_profit 基线")
}

// expYdVsHis 是实验 7：`volume_long_yd` 与 `volume_long_his` 的差别。
//
// 这条是**字段集断言自己挖出来的**——它不在原始的六条里，
// 是 2026-09-07 持仓记录首次出现时冒出来的 20 个未文档化字段之一。
func (r *Runner) expYdVsHis(ctx context.Context) error {
	if len(r.Symbols) == 0 {
		return fmt.Errorf("实验 7 需要 -symbols 指定一个**持有昨仓**的合约")
	}
	sym := r.Symbols[0]
	cli := r.cli
	if _, err := requireYesterday(cli, sym, kq.Buy); err != nil {
		return err
	}
	p := cli.PositionOf(sym)

	r.Logf("")
	r.Logf("== 实验 7：volume_long_yd 与 volume_long_his 的差别 ==")
	for _, k := range []string{"volume_long", "volume_long_today", "volume_long_his", "volume_long_yd",
		"volume_long_frozen_today", "volume_long_frozen_his",
		"pos_long_today", "pos_long_his"} {
		v, present := kq.Num(p, k)
		if !present {
			r.Logf("    %-26s （无此字段）", k) // ⚠️ 与「值是零」分开报
			continue
		}
		r.Logf("    %-26s %.0f", k, v)
	}

	his := kq.MustNum(p, "volume_long_his")
	yd := kq.MustNum(p, "volume_long_yd")
	r.Logf("")
	if his == yd {
		r.Logf("  ⚠️ 当前 his == yd == %.0f，**这个样本分不开它们**。", his)
		r.Logf("     候选：(a) 同义冗余；(b) his 随平昨递减、yd 是日初快照恒定不变。")
		r.Logf("     判别方法：**平掉一手昨仓再看**——候选 b 下 his 减一而 yd 不变。")
		r.Logf("     这不是结论，是「本次样本无判别力」。")
	} else {
		r.Logf("  his=%.0f ≠ yd=%.0f → 二者**不同义**。", his, yd)
		r.Logf("     结合上下文判断哪个是日初快照、哪个随平仓递减。")
	}
	return r.dump("exp7-yd-vs-his", "实验 7：volume_long_yd 与 volume_long_his")
}
