package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
)

// 需要昨仓的那批实验，共用同一份「过夜种子」。
//
// 昨仓造不出来——它只能等一次**结算**。夜盘 21:00 之后属于下一个交易日，
// 但中间没有结算；真正把今仓变成昨仓的是日终结算，所以种子必须隔一个完整交易日。
//
// 时间线：
//
//	D 日        overnight-setup   建仓，此时全是今仓
//	D 日日终     （交易所结算）      今仓 → 昨仓，昨结算价 ← 今结算价
//	D+1 日      margin-price-full / close-order / yd-vs-his

// seedPlan 描述一份过夜种子。
//
// ⚠️ 手数刻意不对称：`UseHistory` 那条腿留 2 手昨仓，
// 是为了让 D+1 日「平掉**少于昨仓量**的手数」成为可能——
// 平满了，三种消耗顺序给出同一个结果，实验白做。
var seedPlan = []struct {
	Symbol string
	Dir    kq.Direction
	Lots   int
	Why    string
}{
	{"SHFE.rb2701", kq.Buy, 2, "UseHistory 合约：实验 1b/2/7 用，2 手是为了能平「少于昨仓量」"},
	{"DCE.m2701", kq.Buy, 2, "NoUseHistory 合约：实验 4 用，同样要能平「少于昨仓量」"},
}

// protectedLegs 是**今天不许平**的持仓腿，交给 kq.Guard 在下单口上执行。
//
// ⚠️ 它与 seedPlan 是两份清单，刻意分开：
//
//	seedPlan       「今晚要**建**什么」—— 一次性的建仓脚本
//	protectedLegs  「今天不许**平**什么」—— 每一笔委托都要过的闸
//
// 20260909 发现这两份清单**对不上**：账上那一手空今仓是 reject / frozen
// 那几条实验顺带开出来的，seedPlan 里没有它，于是它不在任何机器可读的
// 保护之下 —— 而 roadmap.md 已经写着「别在结算前把这些仓平掉」。
// ⚠️ 一句只有人读得到的保护，与没有保护在出事那天是一样的。
//
// ⚠️ 每一条都必须带 TradingDay。过了那天保护自动失效，
// 因为那时它拦的已经是正当的收尾平仓了。
// ⚠️ 这张表**现在是空的**，而空着是对的：
// 20260909 那条护着 rb2701 空今仓的声明已经完成使命 —— 种子活过了结算、
// 变成空昨 1、判掉了 kq_facts 51 挂着的二选一（见 kq_facts 52）。
// 种子用掉之后再留着那条保护，它拦的就是**正当的收尾平仓**了。
//
// ⚠️ 顺带记一笔它是怎么退场的：那条声明带着 TradingDay: "20260909"，
// 于是交易日滚到 20260910 的那一刻它**自己就失效了** ——
// 不需要谁记得来删。到期机制在这里真的兑现了一次。
//
// ⚠️ 表空着的代价写在破坏 188 里：TestGuardCarriesProtectedLegs 会 t.Skip，
// 于是**接线可以在没有保护的日子里静默烂掉**。下次往这里加东西之前，
// 先看一眼那条接线测试是不是还活着。
var protectedLegs = []safety.ProtectedLeg{}

// expOvernightSetup 建立过夜种子。
func (r *Runner) expOvernightSetup(ctx context.Context) error {
	cli := r.cli
	syms := make([]string, 0, len(seedPlan))
	for _, p := range seedPlan {
		syms = append(syms, p.Symbol)
	}
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(syms...); err != nil {
		return err
	}

	if n := cli.OpenLots(); n > 0 {
		return fmt.Errorf("账户已有 %.0f 手持仓，种子要求从空仓开始（否则 D+1 日分不清哪些是种子）", n)
	}

	r.Logf("")
	r.Logf("== 过夜种子：为需要昨仓的实验建仓 ==")
	r.Logf("  ⚠️ 今晚建的是**今仓**。它要等 %s 日终结算之后才变成昨仓。", cli.TradingDay())

	for _, p := range seedPlan {
		q, ok := cli.WaitQuoteReady(p.Symbol, 30*time.Second)
		if !ok {
			return fmt.Errorf("%s 行情未就绪，种子未建全 —— **已建的腿仍在账上**，请重跑或手工平掉", p.Symbol)
		}
		r.Logf("  %s ×%d  %s", p.Symbol, p.Lots, p.Why)
		for i := 0; i < p.Lots; i++ {
			// 逐手下单：PROBE_MAX_VOLUME 通常是 1，且逐手成交更容易看清均价的形成。
			if _, err := r.openOneLot(p.Symbol, p.Dir); err != nil {
				return fmt.Errorf("第 %d 手失败：%w —— **已建的腿仍在账上**", i+1, err)
			}
		}
		_ = q
	}

	// 等截面把成交反映完，再记种子快照。
	cli.WaitTrade(5 * time.Second)
	r.Logf("")
	for _, p := range seedPlan {
		pos := cli.PositionOf(p.Symbol)
		q, _ := cli.QuoteOf(p.Symbol)
		r.Logf("  %-14s 今仓=%.0f 昨仓=%.0f  开仓均价=%.4f  昨结算=%.4f  最新=%.4f",
			p.Symbol,
			kq.MustNum(pos, "volume_long_today"), kq.MustNum(pos, "volume_long_his"),
			kq.MustNum(pos, "open_price_long"), q.PreSettlement, q.LastPrice)

		// ⚠️ 实验 1b 要分开「今仓用开仓价」与「今昨都用昨结算价」两个候选，
		// 前提是这两个价**不相等**。相等的话两个候选同值，实验白做。
		op := kq.MustNum(pos, "open_price_long")
		if op > 0 && q.PreSettlement > 0 && op == q.PreSettlement {
			r.Logf("    ⚠️ 开仓价恰好等于昨结算价 —— 实验 1b 在这条腿上**没有判别力**，")
			r.Logf("       两个候选会给出同一个数。D+1 日需换一个开仓价不同的样本。")
		}
	}

	r.Logf("")
	r.Logf("  ⚠️ 种子会**过夜持有**，隔夜有市场风险（模拟资金）。")
	r.Logf("     D+1 日跑完 margin-price-full / close-order / yd-vs-his 后记得平掉。")
	return r.dump("seed-overnight", "过夜种子：为需要昨仓的实验建仓")
}

// requireYesterday 是需要昨仓的实验的**前置守卫**。
//
// ⚠️ 没有昨仓时必须**拒绝运行**，而不是跑完给一个看起来合理的结论。
// 今仓下这几条实验的两个候选全部同值——跑出来的绿色什么都不说明，
// 这与实验 3 用单合约跑是同一个形状。
func requireYesterday(cli *kq.Client, sym string, dir kq.Direction) (float64, error) {
	return yesterdayLots(cli.PositionOf(sym), sym, dir)
}

// yesterdayLots 是 requireYesterday 的纯函数内核，拆出来是为了能单测。
func yesterdayLots(p map[string]any, sym string, dir kq.Direction) (float64, error) {
	key := "volume_long_his"
	if dir == kq.Sell {
		key = "volume_short_his"
	}
	his := kq.MustNum(p, key)
	if his <= 0 {
		return 0, fmt.Errorf("%s 上没有昨仓（%s=0），本实验**拒绝运行**：\n"+
			"    今仓下两个候选同值，跑出来的结论没有判别力。\n"+
			"    先在前一个交易日跑 `oracle probe -exp overnight-setup`，等一次结算之后再来",
			sym, key)
	}
	return his, nil
}
