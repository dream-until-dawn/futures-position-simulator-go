package probe

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// guard 从 .env 构造下单安全阀。
func (r *Runner) guard() kq.Guard {
	return kq.Guard{AllowOrder: r.Env.AllowOrder, MaxVolume: r.Env.MaxVolume}
}

// openOneLot 用涨跌停价下一手限价单，等它成交。
//
// 用涨跌停价而不是市价单：市价单在各交易所的支持程度不一，且成交价不可控，
// 而判别实验要求输入可复现。
func (r *Runner) openOneLot(sym string, dir kq.Direction) (kq.OrderState, error) {
	cli := r.cli
	q, ok := cli.WaitQuoteReady(sym, 30*time.Second)
	if !ok {
		return kq.OrderState{}, fmt.Errorf("%s 行情未就绪（涨跌停价缺失），无法定价下单", sym)
	}
	ex, inst := splitSymbol(sym)
	req := kq.OrderReq{
		Exchange: ex, Instrument: inst,
		Direction: dir, Offset: kq.Open, Volume: 1,
		LimitPrice: q.AggressivePrice(dir),
	}
	id, err := cli.InsertOrder(r.guard(), req)
	if err != nil {
		return kq.OrderState{}, err
	}
	st, done := cli.WaitOrderFinished(id, 30*time.Second)
	if !done {
		// ⚠️ 超时不等于失败：委托可能还挂着。把两种情形分开报，不要合并成「下单失败」。
		return st, fmt.Errorf("委托 %s 在 30 秒内未走到终态（status=%q last_msg=%q）——"+
			"这只说明没等到，不说明被拒；非交易时段属于正常", id, st.Status, st.LastMsg)
	}
	if st.VolumeLeft != 0 {
		return st, fmt.Errorf("委托 %s 终态但未全成：volume_left=%d last_msg=%q",
			id, st.VolumeLeft, st.LastMsg)
	}
	return st, nil
}

func splitSymbol(sym string) (exchange, instrument string) {
	for i := 0; i < len(sym); i++ {
		if sym[i] == '.' {
			return sym[:i], sym[i+1:]
		}
	}
	return "", sym
}

// obs 是一次观测：把判别所需的字段一次性抓下来。
type obs struct {
	At        time.Time
	LastPrice float64
	Margin    float64
	PosProfit float64
	Float     float64
	Balance   float64
	CTPBal    float64
	Available float64
	CTPAvail  float64
	VolToday  float64
	VolHis    float64
}

func (r *Runner) observe(sym string, dir kq.Direction) obs {
	cli := r.cli
	acc := cli.Account()
	p := cli.PositionOf(sym)
	q, _ := cli.QuoteOf(sym)

	o := obs{
		At:        time.Now(),
		LastPrice: q.LastPrice,
		Balance:   kq.MustNum(acc, "balance"),
		CTPBal:    kq.MustNum(acc, "ctp_balance"),
		Available: kq.MustNum(acc, "available"),
		CTPAvail:  kq.MustNum(acc, "ctp_available"),
	}
	if dir == kq.Buy {
		o.Margin = kq.MustNum(p, "margin_long")
		o.PosProfit = kq.MustNum(p, "position_profit_long")
		o.Float = kq.MustNum(p, "float_profit_long")
		o.VolToday = kq.MustNum(p, "volume_long_today")
		o.VolHis = kq.MustNum(p, "volume_long_his")
	} else {
		o.Margin = kq.MustNum(p, "margin_short")
		o.PosProfit = kq.MustNum(p, "position_profit_short")
		o.Float = kq.MustNum(p, "float_profit_short")
		o.VolToday = kq.MustNum(p, "volume_short_today")
		o.VolHis = kq.MustNum(p, "volume_short_his")
	}
	return o
}

// expMarginPrice 是实验 1 与实验 2 的共同观测。
//
// 两条实验共用一次观测是刻意的——它们问的是同一个截面在同一段时间里的两个字段，
// 分两次跑会引入「两次之间行情走了」这个本可避免的噪声。
//
// ⚠️ **今仓只能分开一部分候选。** 逐日盯市下今仓的基线是开仓价，
// 所以 position_profit 无论用哪个价都会随行情动；真正要区分
// 「昨结算价基线」还是「最新价基线」，必须有**昨仓**，那要等一次结算之后。
// 今晚能定的是：
//
//	margin 随行情动不动  → 分开候选 3（连续重估）与候选 1/2（静态）
//	position_profit 是否等于 float_profit（今仓下二者基线相同，应当相等）
//	balance 与 ctp_balance 在**有持仓**时差多少 → 口径差的首次测量
func (r *Runner) expMarginPrice(ctx context.Context) error {
	if len(r.Symbols) == 0 {
		return fmt.Errorf("实验 1/2 需要 -symbols 指定一个合约")
	}
	sym := r.Symbols[0]
	cli := r.cli

	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}

	if cli.OpenLots() > 0 {
		return fmt.Errorf("账户已有持仓，实验 1/2 要求从空仓开始（否则起点不可复现）")
	}

	r.Logf("")
	r.Logf("== 实验 1/2：盘中 margin 与 position_profit 各自用哪个价 ==")
	if _, err := r.openOneLot(sym, kq.Buy); err != nil {
		return err
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		return kq.MustNum(cli.PositionOf(sym), "volume_long_today") > 0
	}) {
		return fmt.Errorf("成交后 20 秒内持仓截面未出现多头今仓")
	}

	base := r.observe(sym, kq.Buy)
	r.Logf("  建仓完成：今仓=%.0f 昨仓=%.0f  最新价=%.2f  margin=%.2f  持仓盈亏=%.2f  浮动盈亏=%.2f",
		base.VolToday, base.VolHis, base.LastPrice, base.Margin, base.PosProfit, base.Float)

	// 采样：等行情走动，记录 margin 与 position_profit 是否跟着变。
	samples := []obs{base}
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		cli.WaitQuote(5 * time.Second)
		o := r.observe(sym, kq.Buy)
		if o.LastPrice != samples[len(samples)-1].LastPrice {
			samples = append(samples, o)
			r.Logf("    价=%.2f  margin=%.2f  持仓盈亏=%.2f  浮动盈亏=%.2f  balance=%.2f  ctp_balance=%.2f",
				o.LastPrice, o.Margin, o.PosProfit, o.Float, o.Balance, o.CTPBal)
		}
		if len(samples) >= 8 {
			break
		}
	}

	r.Logf("")
	if len(samples) < 2 {
		// ⚠️ 阴性结果不能当阳性用：没等到行情变动，不等于 margin 是静态的。
		r.Logf("  ⚠️ 只采到 %d 个不同价位的样本，**判据不成立**。", len(samples))
		r.Logf("     「没等到行情动」与「margin 不随行情动」在数据上长得一模一样，")
		r.Logf("     不能据此下结论。需在行情活跃时段重跑。")
	} else {
		r.reportMarginVerdict(samples)
	}

	if err := r.dump("exp12-margin-profit-price", "实验 1/2：今仓下 margin 与 position_profit 的价格基线"); err != nil {
		return err
	}
	return r.flatten(sym, kq.Buy)
}

func (r *Runner) reportMarginVerdict(s []obs) {
	minP, maxP := s[0].LastPrice, s[0].LastPrice
	marginMoved, profitMoved := false, false
	for _, o := range s {
		if o.LastPrice < minP {
			minP = o.LastPrice
		}
		if o.LastPrice > maxP {
			maxP = o.LastPrice
		}
		if o.Margin != s[0].Margin {
			marginMoved = true
		}
		if o.PosProfit != s[0].PosProfit {
			profitMoved = true
		}
	}
	r.Logf("  样本 %d 个，价格区间 %.2f ~ %.2f（跨度 %.2f）", len(s), minP, maxP, maxP-minP)

	r.Logf("")
	r.Logf("  【实验 1】margin 随行情变动：%v", marginMoved)
	if marginMoved {
		r.Logf("     → 指向候选 3（全部用最新成交价，连续重估）")
	} else {
		r.Logf("     → 排除候选 3；落在候选 1/2（静态基线）。")
		r.Logf("       ⚠️ 分开候选 1（今仓用开仓价）与候选 2（今昨都用昨结算价）")
		r.Logf("         还需要一笔「开仓价 ≠ 昨结算价」的今仓与一笔昨仓，见实验 1b。")
	}

	r.Logf("")
	r.Logf("  【实验 2】position_profit 随行情变动：%v", profitMoved)
	r.Logf("     ⚠️ 今仓下这一项**没有判别力**：逐日盯市下今仓的基线本就是开仓价，")
	r.Logf("       用最新价还是昨结算价都会随行情动。要分开必须有昨仓。")
	eq := true
	for _, o := range s {
		if o.PosProfit != o.Float {
			eq = false
		}
	}
	r.Logf("     今仓下 position_profit == float_profit：%v（本模型预测为 true）", eq)
	if !eq {
		r.Logf("     ⚠️ 预测不成立 —— 说明两者基线不同，本项目对 ByDate/ByTrade 的理解要改。")
	}

	last := s[len(s)-1]
	r.Logf("")
	r.Logf("  【口径差】有持仓时 balance=%.4f  ctp_balance=%.4f  差=%.6f",
		last.Balance, last.CTPBal, last.Balance-last.CTPBal)
	r.Logf("            有持仓时 available=%.4f  ctp_available=%.4f  差=%.6f",
		last.Available, last.CTPAvail, last.Available-last.CTPAvail)
	r.Logf("     这是「快期口径 vs CTP 口径」的**首次有效测量**——空仓时二者必然相等，")
	r.Logf("     此前的相等什么都不证明。")
}

// flatten 把某个方向的持仓平掉，让账户回到可复现的起点。
func (r *Runner) flatten(sym string, dir kq.Direction) error {
	cli := r.cli
	p := cli.PositionOf(sym)
	closeDir := kq.Sell
	volToday := kq.MustNum(p, "volume_long_today")
	if dir == kq.Sell {
		closeDir = kq.Buy
		volToday = kq.MustNum(p, "volume_short_today")
	}
	if volToday <= 0 {
		return nil
	}
	q, ok := cli.WaitQuoteReady(sym, 20*time.Second)
	if !ok {
		return fmt.Errorf("平仓时行情未就绪，**持仓仍在**：%s %.0f 手，请手工处理", sym, volToday)
	}
	ex, inst := splitSymbol(sym)

	// ⚠️ 平今与平昨不是可选风格：SHFE / INE / CFFEX 必须显式声明平今，
	// DCE / CZCE 只接受普通平仓。这里按今仓平，用 CLOSETODAY；**被拒**才回退 CLOSE。
	//
	// ⚠️ 「被拒」和「没成交」必须分开，这里曾经把两者混为一谈：
	// 原判据是「30 秒内没有全成就回退」，而注释写的是「被拒则回退」——
	// **注释描述的是一个代码里并不存在的条件**。两者的差别在有昨仓时是致命的：
	//
	//	CLOSETODAY 被拒（DCE/CZCE 不认这个标志）    → 回退 CLOSE 是对的
	//	CLOSETODAY 只是没成交（限价没被打到 / 行情快）→ 回退 CLOSE 是另一回事
	//
	// 因为实测（快期模拟·语义，§9）：`CLOSE` 在 `UseHistory` 交易所上
	// **被解释为平昨**。账上同时有昨仓（过夜种子）与今仓（实验腿）时，
	// 一次超时回退会去平掉种子——而种子是六条实验共用、当晚不可再生的资源。
	// 日志却只会说「换一种开平标志再试」：**它不是没平掉，它是平了别的。**
	yesterdayKey := "volume_long_his"
	if dir == kq.Sell {
		yesterdayKey = "volume_short_his"
	}
	volHis := kq.MustNum(p, yesterdayKey)

	for _, off := range []kq.Offset{kq.CloseToday, kq.Close} {
		// ⚠️ 守卫三（最省的一条）：账上有昨仓时，一律禁止 CLOSE 回退。
		//
		// 在 `UseHistory` 交易所上，`CLOSE` 必然打在昨仓上。这条不需要判断拒因，
		// 只看账上有没有昨仓——**一个不需要解析对方措辞的守卫，比一个需要的可靠**。
		if off == kq.Close && volHis > 0 {
			return fmt.Errorf("⚠️ %s 上有 %.0f 手**昨仓**，禁止回退到 CLOSE —— "+
				"实测 CLOSE 在此类交易所上被解释为平昨，回退会平掉昨仓。"+
				"今仓 %.0f 手**仍未平掉**，请手工处理", sym, volHis, volToday)
		}
		req := kq.OrderReq{Exchange: ex, Instrument: inst, Direction: closeDir,
			Offset: off, Volume: int(volToday), LimitPrice: q.AggressivePrice(closeDir)}
		id, err := cli.InsertOrder(r.guard(), req)
		if err != nil {
			return err
		}
		st, done := cli.WaitOrderFinished(id, 30*time.Second)
		if done && st.VolumeLeft == 0 {
			r.Logf("  已平仓 %s %.0f 手（%s）", sym, volToday, off)
			return nil
		}
		if !done {
			// ⚠️ 守卫二：超时**不是**被拒，委托很可能还活着。
			// 不撤就换标志再发，两笔可能**都成交**——今仓昨仓各被平掉一手。
			r.Logf("  ⚠️ %s 委托 30 秒未到终态，**撤单**并停止（超时不等于被拒，不回退）", off)
			if err := cli.CancelOrder(id); err != nil {
				r.Logf("  ⚠️ 撤单也失败：%v", err)
			}
			return fmt.Errorf("⚠️ %s 平仓委托超时未成交（status=%q msg=%q），已发撤单。"+
				"**这不说明它被拒**，所以不换开平标志重试；持仓仍在，请人工确认",
				off, st.Status, st.LastMsg)
		}
		// 到这里是「终态但没全成」= 被拒 / 被撤，这才是可以回退的情形。
		r.Logf("  用 %s 平仓被拒（status=%q msg=%q），换一种开平标志再试", off, st.Status, st.LastMsg)
	}
	return fmt.Errorf("⚠️ 平仓失败，**账户仍有持仓** %s %.0f 手，请手工处理", sym, volToday)
}

// expFlattenAll 把账户平回空仓，让下一条实验有一个可复现的起点。
//
// ⚠️ 它逐个方向、逐个开平标志地试，失败也继续试下一个，最后**如实汇报还剩什么**。
// 平不掉时绝不静默返回成功——账上留着仓而报告说干净，是最坏的一种结果。
func (r *Runner) expFlattenAll(ctx context.Context) error {
	cli := r.cli
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}

	// ⚠️ -symbols 在这里是**限定**，不是可选装饰。
	//
	// 起因是一次真实的操作失误：一条实验意外留下 2 手散仓，我用本命令去清，
	// 它把账上**所有**持仓一起平了——包括刻意留着过夜、准备第二天做昨仓实验的种子。
	// 一个「收拾干净」的命令顺手毁掉了刻意建立的状态。
	// 现在给了合约就只动那些合约；不给才是全平，而全平会先把要动的仓列出来。
	want := map[string]bool{}
	for _, s := range r.Symbols {
		want[s] = true
	}
	var syms []string
	for s := range cli.Positions() {
		if len(want) > 0 && !want[s] {
			continue
		}
		syms = append(syms, s)
	}
	sort.Strings(syms)
	if len(syms) == 0 {
		if len(want) > 0 {
			r.Logf("指定的 %d 个合约上没有持仓记录，无需处理", len(want))
		} else {
			r.Logf("账户无持仓记录，无需处理")
		}
		return nil
	}
	if err := cli.SubscribeQuotes(syms...); err != nil {
		return err
	}
	r.Logf("")
	if len(want) > 0 {
		r.Logf("== 平仓（限定 %d 个合约）==", len(want))
	} else {
		r.Logf("== 平回空仓（**全账户**，不限合约）==")
	}
	r.Logf("  账户共 %.0f 手，本次将处理以下合约：", cli.OpenLots())
	for _, sym := range syms {
		p := cli.PositionOf(sym)
		n := kq.MustNum(p, "volume_long") + kq.MustNum(p, "volume_short")
		if n > 0 {
			r.Logf("    %-14s %.0f 手", sym, n)
		}
	}

	var failed []string
	for _, sym := range syms {
		for _, dir := range []kq.Direction{kq.Buy, kq.Sell} {
			if err := r.flattenAny(sym, dir); err != nil {
				failed = append(failed, fmt.Sprintf("%s/%s: %v", sym, dir, err))
			}
		}
	}
	cli.WaitTrade(5 * time.Second)
	left := cli.OpenLots()
	r.Logf("")
	if len(want) > 0 {
		// 限定模式下账上本来就该剩别的仓，OpenLots 不是判据；只看指定合约。
		var rest float64
		for _, sym := range syms {
			p := cli.PositionOf(sym)
			rest += kq.MustNum(p, "volume_long") + kq.MustNum(p, "volume_short")
		}
		if rest > 0 || len(failed) > 0 {
			for _, f := range failed {
				r.Logf("  ⚠️ %s", f)
			}
			return fmt.Errorf("⚠️ **指定合约上仍有 %.0f 手持仓**，请人工处理", rest)
		}
		r.Logf("  指定合约已平净；账户其余持仓 %.0f 手**未动**", left)
		return nil
	}
	if left > 0 || len(failed) > 0 {
		for _, f := range failed {
			r.Logf("  ⚠️ %s", f)
		}
		return fmt.Errorf("⚠️ **账户仍有 %.0f 手持仓**，请人工处理", left)
	}
	r.Logf("  已平回空仓")
	return nil
}

// flattenAny 平掉某方向的今昨仓，今仓用平今、昨仓用平昨。
func (r *Runner) flattenAny(sym string, dir kq.Direction) error {
	cli := r.cli
	p := cli.PositionOf(sym)
	closeDir, todayKey, hisKey := kq.Sell, "volume_long_today", "volume_long_his"
	if dir == kq.Sell {
		closeDir, todayKey, hisKey = kq.Buy, "volume_short_today", "volume_short_his"
	}
	today, his := kq.MustNum(p, todayKey), kq.MustNum(p, hisKey)
	if today+his <= 0 {
		return nil
	}
	q, ok := cli.WaitQuoteReady(sym, 20*time.Second)
	if !ok {
		return fmt.Errorf("行情未就绪（原因未确定），仍有 %.0f 手", today+his)
	}
	ex, inst := splitSymbol(sym)
	for _, leg := range []struct {
		off kq.Offset
		vol float64
	}{{kq.CloseToday, today}, {kq.Close, his}} {
		for i := 0; i < int(leg.vol); i++ {
			id, err := cli.InsertOrder(r.guard(), kq.OrderReq{
				Exchange: ex, Instrument: inst, Direction: closeDir,
				Offset: leg.off, Volume: 1, LimitPrice: q.AggressivePrice(closeDir)})
			if err != nil {
				return err
			}
			st, done := cli.WaitOrderFinished(id, 30*time.Second)
			if !done {
				// ⚠️ 超时的委托可能还活着；不撤就走，它会在无人看管时成交。
				if err := cli.CancelOrder(id); err != nil {
					r.Logf("  ⚠️ 撤单失败：%v", err)
				}
				return fmt.Errorf("%s 第 %d 手 30 秒未到终态（status=%q msg=%q），已发撤单",
					leg.off, i+1, st.Status, st.LastMsg)
			}
			if st.VolumeLeft != 0 {
				return fmt.Errorf("%s 第 %d 手终态未全成（status=%q msg=%q）", leg.off, i+1, st.Status, st.LastMsg)
			}
		}
	}
	return nil
}
