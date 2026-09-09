package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expRejectTickVsLimit 只问一件事：**同时越涨停又不是整数倍时，柜台报哪一个。**
//
// # 它为什么要单独拉出来
//
// 20260909 两次运行给了**相反**的答案：
//
//	02:0x（盘中）  价格 20489.7      → 「已撤单报单被拒绝价格超出涨停板」
//	03:0x（收盘后）价格 19524.3333   → 「下单价格不是价格单位的整倍数」
//
// 两次之间**同时变了两样**：价格的零头（0.7 对 1/3）与所处时段（盘中对收盘后）。
// ⚠️ 一次变两个自变量的实验，问不出是哪一个在起作用 ——
// 而两次运行各自的日志都看起来完全正常、各自都打印出了一个明确的结论。
//
// 这条实验把**零头**这一维单独扫一遍：同一时刻、同一合约、同一越界幅度，
// 只改零头。零头不影响答案 ⇒ 变量是时段；影响 ⇒ 变量是零头。
//
// ⚠️ 它**不能**单独判定时段那一维 —— 那要在另一个时段再跑一次同样的扫描。
// 这一点必须写出来：一条只扫了一维的实验，与一条扫了两维的实验，
// 在结论那一行长得一模一样。
func (r *Runner) expRejectTickVsLimit(ctx context.Context) error {
	if len(r.Symbols) == 0 {
		return fmt.Errorf("reject-tick-vs-limit 需要 -symbols 指定一个合约")
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
		return fmt.Errorf("%s 行情未就绪", sym)
	}
	tick, err := tickFromSpecs(r.Specs, sym)
	if err != nil {
		return err
	}
	if n := symbolLots(cli, sym); n > 0 {
		return fmt.Errorf("⚠️ %s 上已有 %.0f 手持仓 —— 本实验要求空仓", sym, n)
	}
	ex, inst := splitSymbol(sym)

	r.Logf("")
	r.Logf("== 越涨停 vs 不是整数倍：只扫「零头」这一维 ==")
	r.Logf("  合约 %s  涨停=%.2f tick=%v  时刻=%s",
		sym, q.UpperLimit, tick, time.Now().Format("15:04:05"))

	// 越界幅度固定为 +10 个 tick，只改零头。
	over := q.UpperLimit + 10*tick
	type probeCase struct {
		name  string
		price float64
	}
	cases := []probeCase{
		{"越界·整数倍（标尺）", over},
		{"价内·非整数倍（标尺）", q.LowerLimit + tick/3},
		{"越界·零头 1/10", over + tick/10},
		{"越界·零头 1/3", over + tick/3},

		// —— 0.5 附近这四个是 20260909 补的，目的与外面那几个**不同** ——
		//
		// 外面那一圈（1/10、1/3、7/10、9/10）问的是「零头这一维起不起作用」，
		// 它已经答完了。⚠️ 剩下的问题是**边界在哪**：
		// 盘中至今只有 1/3（报不是整数倍）与 0.7（报涨跌停）两个点，
		// 而 (0.34, 0.69) 这一整段是空的 —— 落在这段里的任何边界
		// 都同样解释得了现有的盘中样本（守卫 TestTickScanInSessionCoverage）。
		//
		// ⚠️ 四个都要：只加 0.5 的话，若它报「不是整数倍」（与盘后相反），
		// 边界就落在 (0.5, 0.7) 里而这一轮同样定不了位。
		{"越界·零头 2/5", over + tick*2/5},
		{"越界·零头 45/100", over + tick*45/100},
		{"越界·零头 1/2", over + tick/2},
		{"越界·零头 55/100", over + tick*55/100},
		{"越界·零头 3/5", over + tick*3/5},
		{"越界·零头 7/10", over + tick*7/10},
		{"越界·零头 9/10", over + tick*9/10},
		// ⚠️ 20260909 02:0x 那一笔的**原样重现**：涨停价 × 1.05。
		{"越界·原样重现 涨停×1.05", q.UpperLimit * 1.05},

		// —— 第二组：**价内**同样扫一遍零头 ——
		//
		// ⚠️ 它回答的是「越界这件事到底有没有参与」。价内没有涨跌停可报，
		// 于是零头 ≥ 半个 tick 的那几笔会怎样，只有两种可能：
		//
		//	仍报「不是整数倍」 ⇒ 零头这一维只在**越界时**改变答案
		//	**被接受**        ⇒ 柜台在悄悄把价格取整 —— 那是个大得多的发现
		//
		// 全部报在跌停价附近（买在地板上），即便被接受也不会成交。
		{"价内·零头 1/10", q.LowerLimit + tick/10},
		{"价内·零头 1/2", q.LowerLimit + tick/2},
		{"价内·零头 7/10", q.LowerLimit + tick*7/10},
		{"价内·零头 9/10", q.LowerLimit + tick*9/10},
	}

	type row struct{ name, price, frac, status, msg string }
	var rows []row
	for _, c := range cases {
		req := kq.OrderReq{Exchange: ex, Instrument: inst, Direction: kq.Buy,
			Offset: kq.Open, Volume: 1, LimitPrice: c.price}
		id, err := cli.InsertOrder(r.guard(), req)
		if err != nil {
			rows = append(rows, row{c.name, fmt.Sprintf("%.4f", c.price),
				fracLabel(c.price, tick), "本地拦截", err.Error()})
			continue
		}
		st, done := cli.WaitOrderFinished(id, 15*time.Second)
		status := st.Status
		if !done {
			status = "未在 15s 内到终态(status=" + st.Status + ")"
			if st.Status == "ALIVE" {
				_ = cli.CancelOrder(id)
			}
		}
		rows = append(rows, row{c.name, fmt.Sprintf("%.4f", c.price),
			fracLabel(c.price, tick), status, st.LastMsg})
	}

	r.Logf("")
	msgs := map[string]int{}
	for _, x := range rows {
		r.Logf("  %-26s @%-12s 零头=%-8s %s", x.name, x.price, x.frac, x.status)
		r.Logf("      柜台：%s", x.msg)
		msgs[x.msg]++
	}
	r.Logf("")
	r.Logf("  不同原话 %d 种：", len(msgs))
	for m, n := range msgs {
		r.Logf("      %d 笔 · %s", n, m)
	}
	// ⚠️ 判别力：两把标尺必须给出**不同**的原话，否则整张表什么都不说明。
	if len(rows) >= 2 && rows[0].msg == rows[1].msg {
		r.Logf("  ⚠️ 两把标尺（越界·整数倍 / 价内·非整数倍）原话相同 —— " +
			"⚠️ **这张表没有判别力**，下面无论怎么解读都不成立")
	}

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
		r.Logf("  收尾撤单 %d 条", alive)
		cli.WaitTrade(5 * time.Second)
	}
	if n := symbolLots(cli, sym); n > 0 {
		return fmt.Errorf("⚠️ 结束时 %s 上出现了 %.0f 手持仓，请人工检查", sym, n)
	}
	return r.dump("exp-reject-tick-vs-limit",
		"越涨停 vs 不是整数倍：固定越界幅度，只扫价格零头这一维")
}

// fracLabel 把**实际构造出来的**零头打成一列，供人一眼核对。
//
// ⚠️ 打的是从价格反算的零头，不是构造时写的那个分数 ——
// 两者不一致正是要看见的东西（涨停价没对齐 tick 时就会不一致）。
func fracLabel(price, tick float64) string {
	f := tickFrac(price, tick)
	if f < 0 {
		return "tick未知"
	}
	return fmt.Sprintf("%.4f", f)
}
