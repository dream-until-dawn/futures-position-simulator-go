package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// rejectCase 是一条构造出来的**应当被拒**的报单。
type rejectCase struct {
	Name   string
	Expect string // 我方预期的拒因，用来和柜台原话对照
	Build  func(q kq.Quote, ex, inst string) kq.OrderReq
}

// rejectCases 覆盖 cn-futures-rules.md §9 里能在客户端侧构造出来的几类校验。
//
// ⚠️ 手数上下限、限仓、可用资金不足这三类没有列进来：
// 前两类要先知道合约的 max/min_limit_order_volume（免费行情不下发），
// 后一类要把账户打到接近爆仓，会污染后续实验的起点。**列出来是为了说明它们是**
// **被有意排除的，不是被忘掉的**——一份看起来完整的清单最容易掩盖缺口。
var rejectCases = []rejectCase{
	{
		Name:   "价格超过涨停",
		Expect: "价格超出涨跌停范围",
		Build: func(q kq.Quote, ex, inst string) kq.OrderReq {
			return kq.OrderReq{Exchange: ex, Instrument: inst, Direction: kq.Buy,
				Offset: kq.Open, Volume: 1, LimitPrice: q.UpperLimit * 1.05}
		},
	},
	{
		Name:   "价格低于跌停",
		Expect: "价格超出涨跌停范围",
		Build: func(q kq.Quote, ex, inst string) kq.OrderReq {
			return kq.OrderReq{Exchange: ex, Instrument: inst, Direction: kq.Sell,
				Offset: kq.Open, Volume: 1, LimitPrice: q.LowerLimit * 0.95}
		},
	},
	{
		Name:   "空仓上平仓",
		Expect: "平仓量超过可平量",
		Build: func(q kq.Quote, ex, inst string) kq.OrderReq {
			return kq.OrderReq{Exchange: ex, Instrument: inst, Direction: kq.Sell,
				Offset: kq.Close, Volume: 1, LimitPrice: q.LowerLimit}
		},
	},
	{
		Name:   "空仓上平今",
		Expect: "平今量超过今仓量；DCE/CZCE 可能另报「不支持平今」",
		Build: func(q kq.Quote, ex, inst string) kq.OrderReq {
			return kq.OrderReq{Exchange: ex, Instrument: inst, Direction: kq.Sell,
				Offset: kq.CloseToday, Volume: 1, LimitPrice: q.LowerLimit}
		},
	},
	{
		Name:   "不是最小变动价位的整数倍",
		Expect: "价格不符合最小变动价位",
		Build: func(q kq.Quote, ex, inst string) kq.OrderReq {
			return kq.OrderReq{Exchange: ex, Instrument: inst, Direction: kq.Buy,
				Offset: kq.Open, Volume: 1, LimitPrice: q.LowerLimit + 0.003}
		},
	},
}

// expRejectCode 是实验 6：报单被拒时柜台原话是什么。
//
// 它同时是**下单通路的端到端自检**——发得出去、回得来、能撤单。
// 非交易时段跑也有意义：那时的拒因本身就是一条要建模的规则。
func (r *Runner) expRejectCode(ctx context.Context) error {
	if len(r.Symbols) == 0 {
		return fmt.Errorf("实验 6 需要 -symbols 指定一个合约")
	}
	sym := r.Symbols[0]
	cli := r.cli

	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}
	q, ok := cli.WaitQuoteReady(sym, 30*time.Second)
	if !ok {
		return fmt.Errorf("%s 行情未就绪，拿不到涨跌停价，无法构造反例", sym)
	}
	if cli.OpenLots() > 0 {
		return fmt.Errorf("账户已有持仓，实验 6 的「空仓上平仓」用例要求空仓")
	}
	ex, inst := splitSymbol(sym)

	r.Logf("")
	r.Logf("== 实验 6：报单被拒时柜台原话 ==")
	r.Logf("  合约 %s  涨停=%.2f 跌停=%.2f 最新=%.2f", sym, q.UpperLimit, q.LowerLimit, q.LastPrice)

	// ⚠️ 下界断言用**实际条数**，不是 > 0。
	// len(cases) > 0 在用例被删到只剩一条时照样绿。
	const wantCases = 5
	if len(rejectCases) != wantCases {
		return fmt.Errorf("反例用例数应为 %d，实际 %d —— 用例被增删了，请同步更新下界",
			wantCases, len(rejectCases))
	}

	type result struct{ name, expect, status, msg string }
	var results []result

	for _, c := range rejectCases {
		req := c.Build(q, ex, inst)
		id, err := cli.InsertOrder(r.guard(), req)
		if err != nil {
			// 被本地安全阀拦下也是一种结果，如实记。
			results = append(results, result{c.Name, c.Expect, "本地拦截", err.Error()})
			continue
		}
		st, done := cli.WaitOrderFinished(id, 15*time.Second)
		status := st.Status
		if !done {
			// ⚠️ 超时不等于「没被拒」。可能挂住了，也可能截面还没推过来。
			status = "未在 15s 内到终态(status=" + st.Status + ")"
			if st.Status == "ALIVE" {
				_ = cli.CancelOrder(id) // 挂住了就撤掉，别留在账上污染后续实验
			}
		}
		results = append(results, result{c.Name, c.Expect, status, st.LastMsg})
	}

	r.Logf("")
	for _, x := range results {
		r.Logf("  %-24s status=%-34s", x.name, x.status)
		r.Logf("      预期：%s", x.expect)
		r.Logf("      柜台：%s", x.msg)
	}

	// 收尾：撤掉一切还活着的委托。
	alive := 0
	for id, v := range cli.Orders() {
		if m, ok := v.(map[string]any); ok {
			if s, _ := m["status"].(string); s == "ALIVE" {
				_ = cli.CancelOrder(id)
				alive++
			}
		}
	}
	if alive > 0 {
		r.Logf("")
		r.Logf("  收尾撤单 %d 条", alive)
		cli.WaitTrade(5 * time.Second)
	}
	if n := cli.OpenLots(); n > 0 {
		return fmt.Errorf("⚠️ 实验 6 结束时账户出现了 %.0f 手持仓 —— 有反例意外成交了，请人工检查", n)
	}

	return r.dump("exp6-reject-code", "实验 6：五类构造出来的报单被拒，记录柜台原话")
}
