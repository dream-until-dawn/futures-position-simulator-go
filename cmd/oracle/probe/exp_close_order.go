package probe

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expCloseOrder 是实验 4：`NoUseHistory` 合约上，柜台按什么顺序消耗今昨仓。
//
// 候选：① 先平昨、后平今  ② 先平今、后平昨  ③ 先开先平（FIFO）
//
// 做法：账上同时有昨仓与今仓，报一笔**普通平仓**，看哪一边减了。
//
// ⚠️ **本样本分不开候选 ① 与 ③。** 种子里的昨仓比今仓早开，所以
// 「先平昨」与「先开先平」给出同一个结果。要分开它们需要两笔开仓时间不同的**昨仓**，
// 即连续两个交易日各建一次种子。这一点必须写在结论里，
// 而不是把 ① 当成唯一答案——**在最常见的样本上，两个候选常常同值**。
//
// # ⚠️⚠️ 20260909 起：这个实验在**这个口子上根本跑不起来**
//
// 不是「还没跑」，是**结构性地没有可测的场合**，两条腿各堵一半：
//
//	NoUseHistory（DCE）  持仓**从来没有昨仓** —— 20260908 当时 419 个持仓截面里
//	                    大商所占 190 个（⚠️ 这两个数是**那一刻**的，语料还在长；
//	                    当前值由守卫自己数，见下），
//	                     有昨仓的 **0 个**。没有今昨，就没有顺序可言
//	UseHistory（SHFE）   CLOSETODAY 平今、CLOSE 平昨，**开平标志已经指定了哪一边**
//	                     （kq_facts 32）—— 柜台不需要选
//
// 也就是说柜台**从来不需要在今昨之间做选择**。
// 这与 kq_facts 5（单向大边在此口子上未启用）同形：结论是**要换口子**，
// 不是再多跑几次实验。守卫见 `conformance/fixture` 的
// `TestCloseOrderIsStructurallyUnmeasurable` —— 大商所哪天出现昨仓，它会红，
// 那时这个实验才第一次有意义。
//
// ⚠️ 下面的前置检查因此**必然**在 NoUseHistory 合约上失败（`requireYesterday`
// 取不到昨仓）。留着这段代码不是摆设：它是「换了口子就能立刻跑」的那一份，
// 而把它删掉等于把这个问题一起删掉。
func (r *Runner) expCloseOrder(ctx context.Context) error {
	if len(r.Symbols) == 0 {
		return fmt.Errorf("实验 4 需要 -symbols 指定一个 NoUseHistory 合约（如 DCE.m2701）")
	}
	sym := r.Symbols[0]
	cli := r.cli

	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}
	hisBefore, err := requireYesterday(cli, sym, kq.Buy)
	if err != nil {
		return err
	}

	r.Logf("")
	r.Logf("== 实验 4：NoUseHistory 合约的平仓消耗顺序 ==")
	r.Logf("  ⚠️ 本样本**分不开**「先平昨」与「先开先平」——种子里的昨仓比今仓早开，")
	r.Logf("     两者给出同一个结果。要分开需连续两日各建一次种子。")

	// 先补一手今仓，让今昨都有量。
	todayBefore := kq.MustNum(cli.PositionOf(sym), "volume_long_today")
	if todayBefore == 0 {
		r.Logf("")
		r.Logf("  今仓为零，先开一手（没有今仓的话三个候选同值，实验白做）")
		if _, err := r.openOneLot(sym, kq.Buy); err != nil {
			return err
		}
		if !cli.WaitUntil(20*time.Second, func() bool {
			return kq.MustNum(cli.PositionOf(sym), "volume_long_today") > 0
		}) {
			return fmt.Errorf("补今仓后 20 秒内截面未更新")
		}
		todayBefore = kq.MustNum(cli.PositionOf(sym), "volume_long_today")
	}

	before := cli.PositionOf(sym)
	accBefore := cli.Account()
	commBefore := kq.MustNum(accBefore, "commission")
	closeProfitBefore := kq.MustNum(accBefore, "close_profit")
	r.Logf("")
	r.Logf("  平仓前：昨仓=%.0f 今仓=%.0f  累计手续费=%.4f  累计平仓盈亏=%.4f",
		hisBefore, todayBefore, commBefore, closeProfitBefore)

	// —— 附带的一条：NoUseHistory 合约收不收 CLOSETODAY ——
	q, ok := cli.WaitQuoteReady(sym, 30*time.Second)
	if !ok {
		return fmt.Errorf("%s 行情未就绪（原因未确定）", sym)
	}
	ex, inst := splitSymbol(sym)
	r.Logf("")
	r.Logf("  附带观测：先试一笔 CLOSETODAY，看该交易所收不收")
	idT, err := cli.InsertOrder(r.guard(), kq.OrderReq{
		Exchange: ex, Instrument: inst, Direction: kq.Sell, Offset: kq.CloseToday,
		Volume: 1, LimitPrice: q.FarPrice(kq.Sell), // 用挂得住的价，避免它真成交污染主实验
	})
	if err != nil {
		r.Logf("    本地拦截：%v", err)
	} else {
		st, done := cli.WaitOrderFinished(idT, 12*time.Second)
		if !done && st.Status == "ALIVE" {
			r.Logf("    CLOSETODAY **被接受并挂住**（status=ALIVE）→ 该合约接受平今标志，随即撤单")
			_ = cli.CancelOrder(idT)
			cli.WaitTrade(5 * time.Second)
		} else {
			r.Logf("    CLOSETODAY status=%s  柜台原话：%s", st.Status, st.LastMsg)
		}
	}

	// —— 主实验：一笔普通平仓 ——
	r.Logf("")
	r.Logf("  主实验：报一笔普通平仓（CLOSE）1 手")
	id, err := cli.InsertOrder(r.guard(), kq.OrderReq{
		Exchange: ex, Instrument: inst, Direction: kq.Sell, Offset: kq.Close,
		Volume: 1, LimitPrice: q.AggressivePrice(kq.Sell),
	})
	if err != nil {
		return err
	}
	st, done := cli.WaitOrderFinished(id, 30*time.Second)
	if !done {
		return fmt.Errorf("平仓委托 30 秒内未到终态（status=%q msg=%q）——"+
			"这只说明没等到，不说明被拒", st.Status, st.LastMsg)
	}
	if st.VolumeLeft != 0 {
		return fmt.Errorf("平仓未全成 volume_left=%d msg=%q，**判据不成立**", st.VolumeLeft, st.LastMsg)
	}
	cli.WaitTrade(5 * time.Second)

	after := cli.PositionOf(sym)
	accAfter := cli.Account()
	hisAfter := kq.MustNum(after, "volume_long_his")
	todayAfter := kq.MustNum(after, "volume_long_today")
	dComm := kq.MustNum(accAfter, "commission") - commBefore
	dProfit := kq.MustNum(accAfter, "close_profit") - closeProfitBefore

	r.Logf("")
	r.Logf("  平仓后：昨仓=%.0f（%+.0f） 今仓=%.0f（%+.0f）",
		hisAfter, hisAfter-hisBefore, todayAfter, todayAfter-todayBefore)
	r.Logf("  本笔手续费=%.4f  本笔平仓盈亏=%.4f", dComm, dProfit)

	r.Logf("")
	switch {
	case hisAfter == hisBefore-1 && todayAfter == todayBefore:
		r.Logf("  结论：消耗的是**昨仓** → 候选 ①「先平昨」或候选 ③「先开先平」")
		r.Logf("        ⚠️ 本样本**分不开这两个**，见开头说明。不得把 ① 写成唯一答案。")
	case todayAfter == todayBefore-1 && hisAfter == hisBefore:
		r.Logf("  结论：消耗的是**今仓** → 候选 ②「先平今」")
		r.Logf("        ⚠️ 这排除了 ① 与 ③，是本实验能给出的最强结论。")
	default:
		r.Logf("  ⚠️ 今昨变动不符合「恰好减一手」的任何一种形态，**判据不成立**。")
		r.Logf("     原始截面已落盘，需人工看。")
	}

	// 手续费能否佐证：与「昨仓开仓价」「今仓开仓价」算出的平仓盈亏对比。
	openHis := kq.MustNum(before, "open_price_long_his")
	openAll := kq.MustNum(before, "open_price_long")
	r.Logf("")
	r.Logf("  佐证：平仓价≈%.4f  昨仓开仓价=%.4f  整体开仓均价=%.4f  昨结算=%.4f",
		q.LastPrice, openHis, openAll, q.PreSettlement)
	if math.Abs(dProfit) > 1e-9 {
		r.Logf("     ⚠️ 平仓盈亏的基线（平昨用昨结算价 / 平今用开仓价）在这里能交叉验证消耗顺序，")
		r.Logf("       但需要合约乘数才能算绝对值。乘数来自 refdata，本轮不引入——")
		r.Logf("       **持仓量的变化已经是更直接的判据**，不必为一个约不掉的量押上另一条未验证的链路。")
	} else {
		r.Logf("     本笔平仓盈亏为零，佐证不成立（不是「基线正确」，是「测不出」）")
	}

	return r.dump("exp4-close-order", "实验 4：NoUseHistory 合约的平仓消耗顺序")
}
