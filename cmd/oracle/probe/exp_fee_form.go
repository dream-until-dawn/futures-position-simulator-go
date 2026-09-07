package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// feeResolution 是手续费字段能分辨的最小差。柜台把 commission 报到小数点后 4 位，
// 这里留一个数量级的余量。
const feeResolution = 0.001

// expFeeForm 分开手续费的两种收法：**每手固定额** 与 **按昨结算价比例**。
//
// ⚠️ 这两种收法在**单个合约**上给出同一个数，怎么测都分不开——
// 前一条实验（fee-base）已经把「按成交价」排除掉了，剩下这两个候选都与成交价无关。
// 分开它们需要**同品种、两个昨结算价不同的合约**：
//
//	两个费额相同           → 每手固定额
//	费额与昨结算价成比例    → 按昨结算价比例
//
// ⚠️ 必须同品种。跨品种时费率本来就不同，两个候选都解释得通，等于没测。
//
// ⚠️ 还必须两个昨结算价差得够开：差得太小时，「按比例」算出来的差
// 落在手续费字段的分辨率以下，于是它看起来和「固定额」一模一样。
// 这一条在跑完之后按实测的费额复核，不成立就报判据不成立。
func (r *Runner) expFeeForm(ctx context.Context) error {
	if err := checkSpreadSample(r.Symbols); err != nil {
		return fmt.Errorf("手续费形式实验与实验 3 用同一条样本守卫（同品种、不同月份）：%w", err)
	}
	cli := r.cli
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(r.Symbols...); err != nil {
		return err
	}

	r.Logf("")
	r.Logf("== 手续费收法：每手固定额 还是 按昨结算价比例 ==")

	var rows []feeRow
	for _, sym := range r.Symbols {
		p := cli.PositionOf(sym)
		if v := kq.MustNum(p, "volume_long") + kq.MustNum(p, "volume_short"); v > 0 {
			return fmt.Errorf("%s 上已有 %.0f 手持仓，平仓时分不清平的是谁的手，本实验作废", sym, v)
		}
		c0 := kq.MustNum(cli.Account(), "commission")
		if _, err := r.openOneLot(sym, kq.Buy); err != nil {
			return err
		}
		if !cli.WaitUntil(20*time.Second, func() bool {
			return kq.MustNum(cli.PositionOf(sym), "volume_long_today") > 0
		}) {
			return fmt.Errorf("%s 成交后未见持仓", sym)
		}
		cli.WaitTrade(2 * time.Second)
		c1 := kq.MustNum(cli.Account(), "commission")

		p = cli.PositionOf(sym)
		openPx := kq.MustNum(p, "open_price_long")
		mult := 0.0
		if v := kq.MustNum(p, "volume_long"); openPx > 0 && v > 0 {
			mult = kq.MustNum(p, "open_cost_long") / (openPx * v)
		}
		marginOfLot := kq.MustNum(p, "margin_long")

		if err := r.flatten(sym, kq.Buy); err != nil {
			return fmt.Errorf("平仓失败，**账户可能仍有持仓**：%w", err)
		}
		if !cli.WaitUntil(20*time.Second, func() bool {
			return kq.MustNum(cli.PositionOf(sym), "volume_long_today") == 0
		}) {
			return fmt.Errorf("%s 报了平成但持仓未归零", sym)
		}
		cli.WaitTrade(2 * time.Second)
		c2 := kq.MustNum(cli.Account(), "commission")

		q, _ := cli.QuoteOf(sym)
		rows = append(rows, feeRow{sym, openPx, q.PreSettlement, mult, openPx * mult, c1 - c0, c2 - c1, marginOfLot})
		r.Logf("  %-14s 昨结=%9.2f 乘数=%4.0f  开仓费=%11.6f 平今费=%11.6f 保证金=%10.2f",
			sym, q.PreSettlement, mult, c1-c0, c2-c1, marginOfLot)
	}

	if err := r.dump("exp-fee-form", "手续费收法：同品种两月份，每手固定额 vs 按昨结算价比例"); err != nil {
		return err
	}

	a, b := rows[0], rows[1]
	if a.PreSettle <= 0 || b.PreSettle <= 0 || a.Mult <= 0 || b.Mult <= 0 {
		return fmt.Errorf("⚠️ 昨结算价或乘数缺失（%.2f/%.0f 与 %.2f/%.0f），本次结论作废",
			a.PreSettle, a.Mult, b.PreSettle, b.Mult)
	}

	r.Logf("")
	r.reportFeeForm("开仓档", a, b, a.OpenFee, b.OpenFee)
	r.reportFeeForm("平今档", a, b, a.CloseFee, b.CloseFee)
	return nil
}

func (r *Runner) reportFeeForm(tier string, a, b feeRow, fa, fb float64) {
	r.Logf("")
	r.Logf("  【%s】%s=%.6f  %s=%.6f", tier, a.Sym, fa, b.Sym, fb)
	if fa == 0 || fb == 0 {
		r.Logf("     ⚠️ 有一边为 0 —— 该档不收费或没实现，两个候选在此恒等，**判据不成立**")
		return
	}

	// 「按昨结算价比例」预测的 b 档费额。
	predicted := fa * (b.PreSettle * b.Mult) / (a.PreSettle * a.Mult)
	gap := abs(predicted - fa)
	r.Logf("     两个候选各自预测 %s 的费额：固定额 → %.4f，按比例 → %.4f（相差 %.4f）",
		b.Sym, fa, predicted, gap)

	// ⚠️ 样本守卫放在**跑完之后**，因为它要用实测的费额才能算。
	if gap < feeResolution {
		r.Logf("     ⚠️ 两个候选的预测只差 %.6f，低于手续费字段的分辨率 %.4f —— ", gap, feeResolution)
		r.Logf("        它们在这组合约上给出同一个数，**判据不成立**。")
		r.Logf("        换两个昨结算价差得更开的月份重跑。")
		return
	}

	switch {
	case nearlyEqual(fa, fb):
		r.Logf("     → **每手固定额** %.4f 元/手：昨结算价差 %.2f 而费额相同。", fa, b.PreSettle-a.PreSettle)
	case abs(fb-predicted) < feeResolution:
		rate := fa / (a.PreSettle * a.Mult)
		r.Logf("     → **按昨结算价比例**：实测 %.4f 与按比例预测 %.4f 相符，费率 %.10f（万分之 %.4f）",
			fb, predicted, rate, rate*10000)
	default:
		r.Logf("     ⚠️ 实测 %.4f 既不等于 %.4f，也不等于按比例预测的 %.4f ——", fb, fa, predicted)
		r.Logf("        两个候选都对不上，**本档不收敛**，不得写进规则文档的实测栏。")
	}
}
