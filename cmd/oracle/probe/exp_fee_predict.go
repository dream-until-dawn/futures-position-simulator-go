package probe

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// expFeePredict 把「按额手续费 = 昨结算价 × 乘数 × 品种费率」变成一次**可证伪的预测**。
//
// ⚠️ 此前这条的证据是「同合约一买一卖、成交价差一个 tick、费额相同」——
// 那只排除了成交价，没有正面确认基准是昨结算价，也没有确认费率按品种。
//
// 成交截面进夹具之后能做得更狠。九个合约的成交里：
//
//	cu2701  八个不同成交价，一个费额     → 费额与成交价无关，八点无一例外
//	rb2610 / rb2701 / rb2705            → 同品种三个月份，三个**不同**费额
//
// 最后一条给出了判据：若费率真按**品种**、基准真是**各合约自己的昨结算价**，
// 那么用 rb2701 定出的费率去算 rb2610 与 rb2705，必须命中它们各自的昨结算价。
// **这是一次外推**——用一个合约标定，去预测另外两个，
// 而它们的昨结算价是**独立观测到的**，不参与标定。
//
//	对上   费率按品种 + 基准是昨结算价，两条一起成立
//	对不上 至少有一条不成立，而且能看出是哪一条
//
// ⚠️ 这个实验**只读**：不下单，不撤单，只订阅行情并读成交。
func (r *Runner) expFeePredict(ctx context.Context) error {
	cli := r.cli
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}

	// 从成交截面收集每个合约的费额。
	type obs struct {
		sym     string
		product string
		fee     float64
		prices  map[float64]bool
	}
	byInst := map[string]*obs{}
	for _, raw := range cli.Trades() {
		t, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		inst, _ := t["instrument_id"].(string)
		ex, _ := t["exchange_id"].(string)
		fee, okf := numOf(t, "commission")
		price, okp := numOf(t, "price")
		if inst == "" || ex == "" || !okf || !okp {
			continue
		}
		o := byInst[inst]
		if o == nil {
			prod, _ := productOf(inst)
			o = &obs{sym: ex + "." + inst, product: prod, fee: fee,
				prices: map[float64]bool{}}
			byInst[inst] = o
		}
		o.prices[price] = true
		// ⚠️ 同合约出现两个不同费额时**报错**，不取其一：
		// 那说明费额不只由合约决定，而整条判据正建立在「只由合约决定」上。
		if math.Abs(o.fee-fee) > 1e-9 {
			return fmt.Errorf("合约 %s 出现两个不同费额 %.6f 与 %.6f —— "+
				"⚠️ 费额不只由合约决定，本实验的判据不成立，先查清楚", inst, o.fee, fee)
		}
	}
	if len(byInst) < 3 {
		return fmt.Errorf("成交截面里只有 %d 个合约 —— 太少，外推没有意义", len(byInst))
	}

	syms := make([]string, 0, len(byInst))
	for _, o := range byInst {
		syms = append(syms, o.sym)
	}
	sort.Strings(syms)
	if err := cli.SubscribeQuotes(syms...); err != nil {
		return err
	}
	for _, s := range syms {
		if _, ok := cli.WaitQuoteReady(s, 30*time.Second); !ok {
			return fmt.Errorf("%s 行情未就绪，读不到昨结算价", s)
		}
	}

	r.Logf("")
	r.Logf("== 按额手续费的基准与费率：一次外推 ==")
	r.Logf("")
	// ⚠️ 判据用「费额 ÷ 昨结算价」，**不用乘数**。
	//
	// 同品种各月份的乘数相同，所以做同品种内的比较时它自己消掉了 ——
	// 少一个输入就少一处可能出错的地方，而这个行情网关恰恰**不推乘数**
	// （i2701 的 volume_multiple 读到 0）。
	// 一个「顺手也读一下乘数」的写法会在这里失败，而失败的理由与本实验无关。
	r.Logf("  %-12s %10s %10s %16s %10s", "合约", "费额", "昨结算价", "费额÷昨结", "成交价种数")

	var rows []row
	for _, s := range syms {
		q, _ := cli.QuoteOf(s)
		o := byInst[instOf(s)]
		if q.PreSettlement <= 0 {
			return fmt.Errorf("%s 的昨结算价读不到（%.4f）—— 判据的分母缺了",
				s, q.PreSettlement)
		}
		rt := o.fee / q.PreSettlement
		rows = append(rows, row{s, o.product, o.fee, q.PreSettlement,
			q.VolumeMultiple, rt, len(o.prices)})
		r.Logf("  %-12s %10.5f %10.2f %16.10f %10d",
			s, o.fee, q.PreSettlement, rt, len(o.prices))
	}

	// —— 判据一：同品种多月份，把收法**分类**出来 ——
	//
	// ⚠️ 第一版把这条写成了「同品种反解费率必须一致，不一致就告警」，那是错的：
	// `m` 三个月份费额都是 1.5、昨结却各不相同，
	// 「不一致」在它身上是**正确答案** —— 它按手收，本就不该有比例关系。
	// 一条把正确答案报成告警的判据，会训练人忽略告警。
	//
	// 正确的形状是三分类，而且两类各自都是**正面**证据：
	//
	//	按手  费额跨月份恒定，而昨结各不相同   → 费额与昨结无关，排除按额
	//	按额  费额÷昨结跨月份恒定，而费额各不相同 → 一次外推命中
	//	异常  两条都不成立
	//
	// ⚠️ 只有一个月份的品种**判不了**：单点上按手与按额给出同一个数。
	// 那必须明说成「判不了」，不能默认归进任何一类。
	byProduct := map[string][]row{}
	for _, rw := range rows {
		byProduct[rw.product] = append(byProduct[rw.product], rw)
	}
	multi, byLot, byMoney := 0, 0, 0
	var anomalies []string
	r.Logf("")
	r.Logf("  —— 判据一：同品种多月份 → 分类出收法 ——")
	for _, p := range sortedProducts(byProduct) {
		rs := byProduct[p]
		if len(rs) < 2 {
			r.Logf("    %-6s 只有 %d 个月份 —— ⚠️ **判不了**：单点上按手与按额给出同一个数",
				p, len(rs))
			continue
		}
		multi++
		var fees, pres []float64
		for _, rw := range rs {
			fees = append(fees, rw.fee)
			pres = append(pres, rw.pre)
		}
		mode, detail := classifyFeeMode(rs)
		switch mode {
		case feeByLot:
			byLot++
			r.Logf("    %-6s %d 个月份 → **按手固定 %.4f 元/手**", p, len(rs), rs[0].fee)
			r.Logf("      昨结 %v 各不相同而费额恒定 —— 费额与昨结无关，排除按额", pres)
		case feeByMoney:
			byMoney++
			r.Logf("    %-6s %d 个月份 → **按额，费额÷昨结 = %.10f**", p, len(rs), rs[0].rate)
			r.Logf("      费额 %v 各不相同、昨结 %v 各不相同，商却相同", fees, pres)
			r.Logf("      ⚠️ 这是一次**外推命中**：用任一个月份标定，都能算出另外两个的费额。")
			r.Logf("      再除以该品种的乘数就是按额费率，而乘数在同品种内是常数 ——")
			r.Logf("      所以这一条**不需要知道乘数**就能成立。")
		default:
			anomalies = append(anomalies, p)
			r.Logf("    %-6s %d 个月份 → ⚠️ **%s**", p, len(rs), detail)
			r.Logf("      费额 %v，昨结 %v —— 这是真正要查的那一类", fees, pres)
		}
	}
	if multi == 0 {
		// ⚠️ 一个品种都没有多个月份时，上面整段一次都没跑，而本实验照样「成功」。
		return fmt.Errorf("⚠️ 没有任何品种出现两个以上月份 —— " +
			"分类判据一次都没被执行，本实验此时什么都没证明")
	}
	// ⚠️ 判别力守卫：两类都出现过，这条才算真的分开了两种收法。
	// 只见过一类的分类器，与「一律判成这一类」在样本上完全同值。
	if byLot == 0 || byMoney == 0 {
		return fmt.Errorf("⚠️ 可判的品种里按手 %d 个、按额 %d 个 —— "+
			"两类没有同时出现，本次样本证不了「两种收法并存」，"+
			"只能证「至少存在这一类」", byLot, byMoney)
	}
	r.Logf("")
	r.Logf("    分类结果：按手 %d 个品种，按额 %d 个品种", byLot, byMoney)
	if len(anomalies) > 0 {
		return fmt.Errorf("⚠️ 品种 %v 两类都不成立 —— 落盘前先报出来，不当成通过", anomalies)
	}

	// —— 判据二：费额与成交价无关 ——
	r.Logf("")
	r.Logf("  —— 判据二：同合约多个成交价，费额是否恒定 ——")
	best, bestSym := 0, ""
	for _, rw := range rows {
		if rw.nPrices > best {
			best, bestSym = rw.nPrices, rw.sym
		}
	}
	r.Logf("    最强的一个：%s 有 %d 个不同成交价，费额只有 1 个", bestSym, best)
	if best < 3 {
		return fmt.Errorf("⚠️ 成交价最多的合约也只有 %d 个不同价 —— "+
			"「费额与成交价无关」这条在本次样本上判别力不足（两个价只差一个 tick 时，"+
			"用成交价算也可能因取整而相同）", best)
	}

	r.Logf("")
	r.Logf("  ⚠️ 本实验**测不到**的：平昨费率。本交易日全部平仓都是 CLOSETODAY，")
	r.Logf("     一笔 CLOSE 都没有 —— 平昨要等今晚有了昨仓才做得出来。")

	// 乘数若碰巧读到了，顺带把按额费率打出来 —— 但判据不依赖它。
	for _, rw := range rows {
		if rw.mult > 0 {
			r.Logf("    ⓘ %s 乘数 %.0f，按额费率 %.8f", rw.sym, rw.mult, rw.rate/rw.mult)
		}
	}

	return r.dump("fee-predict", fmt.Sprintf(
		"按额手续费外推：%d 个合约，%d 个品种出现多月份。"+
			"分类出按手 %d 个品种、按额 %d 个品种（判据不需要乘数）；"+
			"费额与成交价无关（最强 %s 有 %d 个不同成交价）。⚠️ 只读实验，未下单",
		len(rows), multi, byLot, byMoney, bestSym, best))
}

// row 是一个合约的一行观测。
type row struct {
	sym, product string
	fee, pre     float64
	// mult 是乘数，**可能为 0**：这个行情网关不推它。
	// 留着是为了在推的时候能顺带打出按额费率，判据本身不依赖它。
	mult    float64
	rate    float64 // 费额 ÷ 昨结算价
	nPrices int
}

// numOf 读一个 JSON 数值字段。⚠️ 读不到就说读不到，不回落到 0。
func numOf(m map[string]any, key string) (float64, bool) {
	v, ok := m[key].(float64)
	return v, ok
}

// spreadOf 返回一组行上某个量的极差。
func spreadOf(rs []row, pick func(row) float64) float64 {
	lo, hi := pick(rs[0]), pick(rs[0])
	for _, rw := range rs {
		lo = math.Min(lo, pick(rw))
		hi = math.Max(hi, pick(rw))
	}
	return hi - lo
}

func instOf(sym string) string {
	_, inst := splitSymbol(sym)
	return inst
}

func sortedProducts(m map[string][]row) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// feeMode 是一个品种的手续费收法。
type feeMode uint8

const (
	// feeUndecidable 是零值：判不了。
	//
	// ⚠️ 它必须是零值。一个默认落进「按额」或「按手」的零值，
	// 会让判不了的品种被静默归类，而归类结果看起来和真判出来的一模一样。
	feeByLot       feeMode = iota
	feeUndecidable         // 占位
	feeByMoney             // 按成交额（基准是昨结算价）
	feeAnomalous           // 两类都不成立
)

// classifyFeeMode 从同品种多个月份的观测里分类出收法。
//
// ⚠️ 抽成纯函数不只是为了好测：这条判据此前只活在实验的运行时路径上，
// 而实验要连网、要有持仓、要在交易时段跑 —— 也就是它**几乎不会被执行到**，
// 更不会被穷举。一条几乎不被执行的判据，写错了不会有任何动静。
//
// 判据本身：
//
//	按手  费额跨月份恒定，而昨结各不相同   —— 后半句不能省：
//	      昨结也恒定时，按手与按额给出同一个数，什么都没分开
//	按额  费额÷昨结跨月份恒定，而费额各不相同 —— 后半句同理
//	其余  异常或判不了
func classifyFeeMode(rs []row) (feeMode, string) {
	if len(rs) < 2 {
		return feeUndecidable, "只有一个月份：单点上按手与按额给出同一个数"
	}
	feeSpread := spreadOf(rs, func(rw row) float64 { return rw.fee })
	preSpread := spreadOf(rs, func(rw row) float64 { return rw.pre })
	rateSpread := spreadOf(rs, func(rw row) float64 { return rw.rate })

	// ⚠️ 昨结全相同时两类都成立，而「两类都成立」等于没分开。
	// 这一条必须在两个分支**之前**判，否则先命中的那一支会把它吞掉。
	if preSpread <= feeEps {
		return feeUndecidable, "各月份昨结算价相同：两类给出同一个数，分不开"
	}
	// ⚠️ 这里**不写** `byMoney := rateSpread <= feeEps && feeSpread > feeEps`。
	// 那个 `&& feeSpread > feeEps` 是死条件：走到 byMoney 时 byLot 已经判过假，
	// 也就是 feeSpread > feeEps 必然成立。破坏验证当场抓到了它 ——
	// 删掉它没有任何测试变红，而一个删掉也没人发现的条件就是装饰。
	//
	// 两类互斥这件事由上面那道 preSpread 守卫保证，而且能证明：
	// 费额恒定且费额÷昨结也恒定 ⟹ 昨结恒定 ⟹ 已在守卫处返回「判不了」。
	byLot := feeSpread <= feeEps
	byMoney := rateSpread <= feeEps
	switch {
	case byLot:
		return feeByLot, "按手固定额"
	case byMoney:
		return feeByMoney, "按成交额，基准昨结算价"
	default:
		return feeAnomalous, fmt.Sprintf(
			"两类都不成立：费额跨度 %.2e，费额÷昨结跨度 %.2e", feeSpread, rateSpread)
	}
}

// feeEps 是「算作相同」的阈值。
//
// ⚠️ 它取得比价格的最小变动小得多，但**不是零**：
// 费额与昨结都从 JSON 的 float64 来，精确相等在这里是运气不是保证。
const feeEps = 1e-9
