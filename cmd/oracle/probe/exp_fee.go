package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// feeRow 是一个合约上的一轮开平观测。
type feeRow struct {
	Sym       string
	OpenPx    float64
	PreSettle float64 // 昨结算价：手续费与保证金的候选基准之一
	Mult      float64
	Notional  float64 // 成交价 × 乘数，即一手的名义额
	OpenFee   float64
	CloseFee  float64 // 平今档
	Margin    float64 // 该手持仓保证金
}

// expFeeRates 问的是：**快期模拟的手续费是全局一个口径，还是分品种的真实费率。**
//
// ⚠️ 这个问题的提法是被保证金那条实测逼出来的：rb2701 / rb2610 / m2701
// 三个合约横跨两个交易所，保证金率反解**全部精确等于 7%**——真实市场里
// 螺纹和豆粕的保证金率并不相等，所以快期模拟用的是统一简化值。
// 手续费若也如此，那它就不能当费率的对拍口子。
//
// 做法：拿若干名义额差得很开的合约各开平一手，看
//
//	fee 恒定             → 全局**按手**固定额
//	fee/名义额 恒定       → 全局**按成交额**比例
//	两者都不恒定          → 分品种费率（这才说明柜台加载了真实费率数据）
//
// ⚠️ 「分品种」这一档**不是收敛结论**，它只是排除了两种全局简化；
// 真实费率长什么样，要一个品种一个品种地量，本实验不回答。
//
// 本实验刻意**不要求账户空仓**——过夜种子要留着。它只要求所测合约上没有持仓。
// 平昨档今天测不了：那需要昨仓。
func (r *Runner) expFeeRates(ctx context.Context) error {
	if len(r.Symbols) < 2 {
		return fmt.Errorf("手续费实验需要**至少两个**名义额差得开的合约，得到 %d 个", len(r.Symbols))
	}
	cli := r.cli
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(r.Symbols...); err != nil {
		return err
	}

	r.Logf("")
	r.Logf("== 手续费：全局统一口径，还是分品种费率 ==")

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
		marginOfLot := kq.MustNum(p, "margin_long")
		openPx := kq.MustNum(p, "open_price_long")
		cost := kq.MustNum(p, "open_cost_long")
		vol := kq.MustNum(p, "volume_long")
		// 乘数由 open_cost 反解，不猜也不查表。
		mult := 0.0
		if openPx > 0 && vol > 0 {
			mult = cost / (openPx * vol)
		}

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
		row := feeRow{sym, openPx, q.PreSettlement, mult, openPx * mult, c1 - c0, c2 - c1, marginOfLot}
		if row.Notional <= 0 {
			return fmt.Errorf("⚠️ %s 名义额反解失败（价=%.2f 乘数=%.2f），本次结论作废", sym, openPx, mult)
		}
		rows = append(rows, row)
		r.Logf("  %-14s 成交=%10.2f 昨结=%10.2f 乘数=%5.0f 名义额=%12.2f", sym, row.OpenPx, row.PreSettle, row.Mult, row.Notional)
		r.Logf("  %-14s   开仓费=%9.4f 平今费=%9.4f 保证金=%11.2f", "", row.OpenFee, row.CloseFee, row.Margin)
	}

	if err := r.dump("exp-fee-rates", "手续费：全局统一口径 vs 分品种费率"); err != nil {
		return err
	}

	r.Logf("")
	r.Logf("  名义额跨度 %.2f ~ %.2f（%.1f 倍）", minNotional(rows), maxNotional(rows),
		maxNotional(rows)/minNotional(rows))
	if !feeSampleDiscriminating(rows) {
		return fmt.Errorf("⚠️ 名义额跨度只有 %.2f 倍，「按手固定额」与「按成交额比例」"+
			"在这组样本上给出几乎同一组数，**判据不成立**。换名义额差得开的合约重跑",
			maxNotional(rows)/minNotional(rows))
	}

	r.reportFeeTier("开仓档", rows, func(x feeRow) float64 { return x.OpenFee })
	r.reportFeeTier("平今档", rows, func(x feeRow) float64 { return x.CloseFee })

	r.reportMarginRate(rows)

	r.Logf("")
	r.Logf("  ⚠️ 平昨档今天测不了：它需要昨仓。等过夜种子结算之后。")
	return nil
}

// reportMarginRate 顺带反解各合约的保证金率，用**昨结算价**做基准。
//
// ⚠️ 基准取昨结算价，是因为 rb2701/rb2610/m2701 三个合约上它给出精确一致的 7%，
// 而最新价基准给出三个互不相同的数。这里再拿差得远的品种复核一次：
// 若 cu / i 也是 7%，说明快期模拟用的是**全局统一保证金率**，
// 它就不能当保证金率的对拍口子——真实市场里铜和铁矿的保证金率与螺纹并不相同。
func (r *Runner) reportMarginRate(rows []feeRow) {
	r.Logf("")
	r.Logf("  【保证金率】以昨结算价为基准反解")
	same := true
	var r0 float64
	for i, x := range rows {
		if x.PreSettle <= 0 || x.Mult <= 0 {
			r.Logf("     %-14s ⚠️ 昨结(%.2f)或乘数(%.2f)缺失，这一行不参与判定", x.Sym, x.PreSettle, x.Mult)
			continue
		}
		rate := x.Margin / (x.PreSettle * x.Mult)
		rateLast := x.Margin / x.Notional
		r.Logf("     %-14s 昨结基准=%.8f   成交价基准=%.8f", x.Sym, rate, rateLast)
		if i == 0 {
			r0 = rate
		} else if !nearlyEqual(rate, r0) {
			same = false
		}
	}
	if same {
		r.Logf("     → 各品种保证金率**全部相同**(%.6f) → 全局统一简化值。", r0)
		r.Logf("       ⚠️ 快期模拟因此**不能**当保证金率的对拍口子；")
		r.Logf("         能对拍的是「基准价用哪个」与「怎么合计」，不是费率本身。")
	} else {
		r.Logf("     → 各品种保证金率**不同** → 柜台加载了逐品种的保证金率数据。")
	}
}

func minNotional(rows []feeRow) float64 {
	m := rows[0].Notional
	for _, x := range rows {
		if x.Notional < m {
			m = x.Notional
		}
	}
	return m
}

func maxNotional(rows []feeRow) float64 {
	m := rows[0].Notional
	for _, x := range rows {
		if x.Notional > m {
			m = x.Notional
		}
	}
	return m
}

// feeSampleDiscriminating 报告这组样本能否把「按手」与「按额」分开。
//
// ⚠️ 分不开的条件是**名义额都差不多**：那时按额收也几乎是同一个数，
// 跟按手收长得一样。要求最大 / 最小 ≥ 1.5 倍——比手续费的取整粒度大得多。
func feeSampleDiscriminating(rows []feeRow) bool {
	if len(rows) < 2 {
		return false
	}
	return maxNotional(rows) >= 1.5*minNotional(rows)
}

// nearlyEqual 用**相对**误差比较，因为这里的数跨了好几个数量级。
func nearlyEqual(a, b float64) bool {
	scale := abs(a)
	if abs(b) > scale {
		scale = abs(b)
	}
	if scale == 0 {
		return true
	}
	return abs(a-b)/scale < 1e-6
}

func (r *Runner) reportFeeTier(tier string, rows []feeRow, pick func(feeRow) float64) {
	r.Logf("")
	r.Logf("  【%s】", tier)

	allZero := true
	for _, x := range rows {
		if pick(x) != 0 {
			allZero = false
		}
	}
	if allZero {
		r.Logf("     全部为 0 —— 该档**不收费**，或本柜台没实现这一档。")
		r.Logf("     ⚠️ 「不收费」与「没实现」在数据上一模一样，这里分不开。")
		return
	}

	sameAmount, sameRate := true, true
	r0 := pick(rows[0]) / rows[0].Notional
	for _, x := range rows {
		if !nearlyEqual(pick(x), pick(rows[0])) {
			sameAmount = false
		}
		if !nearlyEqual(pick(x)/x.Notional, r0) {
			sameRate = false
		}
		r.Logf("     %-14s 费=%9.4f  费/名义额=%.10f", x.Sym, pick(x), pick(x)/x.Notional)
	}

	switch {
	case sameAmount && sameRate:
		// 名义额跨度已被守卫挡过，这两条不该同时成立。
		r.Logf("     ⚠️ 既等额又等率 —— 与样本守卫矛盾，**判据自相矛盾**，需人工看。")
	case sameAmount:
		r.Logf("     → **全局按手固定额** %.4f 元/手，跨品种不变。", pick(rows[0]))
		r.Logf("       这是简化实现：真实市场里各品种每手费额并不相同。")
	case sameRate:
		r.Logf("     → **全局按成交额比例** %.10f（万分之 %.6f），跨品种不变。", r0, r0*10000)
		r.Logf("       这是简化实现：真实市场里各品种费率并不相同，且很多品种是按手收。")
	default:
		r.Logf("     → 既不等额也不等率 → **分品种费率**，柜台加载了逐品种的费率数据。")
		r.Logf("       ⚠️ 这不是收敛结论，只排除了两种全局简化；")
		r.Logf("          具体费率要一个品种一个品种地量，本实验不回答。")
	}
}
