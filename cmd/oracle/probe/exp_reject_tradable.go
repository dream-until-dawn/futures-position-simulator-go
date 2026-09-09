package probe

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expRejectTradable 问「合约不可交易」这一项排在哪。
//
// # 判据
//
// 拿一个**根本不存在**的合约（同品种、一个不会有的月份）下单，
// 再拿同一个价格在**真实**合约上下一遍：
//
//	真实合约 + 偏离整数倍的价  →「下单价格不是价格单位的整倍数」（已知）
//	假合约   + 同一个价        →  报什么？
//
// 若两者原话相同，说明柜台先查了价格；不同则说明先查了合约。
// ⚠️ 逐字比，不认关键字。
//
// # 安全
//
// 不存在的合约**不可能成交**；真实合约那两笔一笔越界、一笔偏离整数倍，
// 都必然被拒。手数一律 1，仍然过安全阀。
//
// ⚠️ 这条实验**不需要**假合约的行情（拿不到），所以价格是写死的常数 ——
// 而写死的常数必须落在真实合约的涨跌停内，否则「偏离整数倍」这一项
// 会被涨跌停盖过去。它由 -symbols 指定的真实合约的行情当场核对。
func (r *Runner) expRejectTradable(ctx context.Context) error {
	if len(r.Symbols) == 0 {
		return fmt.Errorf("reject-tradable 需要 -symbols 指定一个**真实**合约")
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
	ex, inst := splitSymbol(sym)
	fake := fakeMonthOf(inst)
	if fake == "" {
		return fmt.Errorf("从 %s 里认不出品种，构造不出「不存在的合约」", inst)
	}

	// 价格：贴着**跌停**往上五个 tick，再加 1/3 个 tick。
	//
	// ⚠️ 不取涨跌停正中间。中间价紧贴市价，而真实合约那一笔是 BUY ——
	// 万一柜台没把它当偏离（半 tick 那条边界就是这么来的），它会**当场成交**。
	// 买在地板附近成不了交，代价只是这笔单离市价远，而这条实验不在乎它离多远。
	onTick := math.Ceil(q.LowerLimit/tick)*tick + 5*tick
	offTickPrice := onTick + tick/3
	if !offTick(offTickPrice, tick) {
		return fmt.Errorf("⚠️ 构造出来的 %.4f 按柜台的判据**不算偏离** —— "+
			"这条实验的对照组不成立", offTickPrice)
	}
	if offTickPrice > q.UpperLimit || offTickPrice < q.LowerLimit {
		return fmt.Errorf("⚠️ 构造出来的 %.4f 越了涨跌停 —— "+
			"那一项会盖过「偏离整数倍」，对照组不成立", offTickPrice)
	}

	r.Logf("")
	r.Logf("== 合约不可交易 vs 价格类校验 ==")
	r.Logf("  真实合约 %s（涨停 %.2f 跌停 %.2f tick %v）", sym, q.UpperLimit, q.LowerLimit, tick)
	r.Logf("  假合约   %s.%s —— 这个月份不存在", ex, fake)
	r.Logf("  价格     整数倍 %.4f / 偏离 %.4f", onTick, offTickPrice)

	cases := []struct {
		name       string
		inst       string
		price      float64
		wantExists bool
	}{
		{"真实合约 · 偏离整数倍（标尺）", inst, offTickPrice, true},
		{"假合约 · 整数倍", fake, onTick, false},
		{"假合约 · 偏离整数倍", fake, offTickPrice, false},
	}
	// ⚠️ 柜台不一定把拒因写进委托记录。DIFF 还有一条 `notify` 通道，
	// 而**本仓库此前所有的拒因实验都只读委托记录**——
	// 一条只走 notify 的拒因，在那些实验里长得像「柜台没反应」。
	//
	// ⚠️ 归因用 notify 的**数值码**，因为它是三笔单**都有**的那条通道：
	// 不存在的合约压根不进委托记录，拿 last_msg 去比等于拿空串去比。
	seenNotify := len(cli.Notifies())
	codes := make([]int, 0, len(cases))
	msgs := make([]string, 0, len(cases))
	for _, c := range cases {
		req := kq.OrderReq{Exchange: ex, Instrument: c.inst, Direction: kq.Buy,
			Offset: kq.Open, Volume: 1, LimitPrice: c.price}
		id, err := cli.InsertOrder(r.guard(), req)
		if err != nil {
			r.Logf("")
			r.Logf("  %-26s 本地拦截：%v", c.name, err)
			msgs = append(msgs, "")
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
		r.Logf("")
		r.Logf("  %-26s @%.4f  status=%s", c.name, c.price, status)
		r.Logf("      柜台：%s", st.LastMsg)
		code := 0
		if all := cli.Notifies(); len(all) > seenNotify {
			for _, n := range all[seenNotify:] {
				r.Logf("      ⓘ notify code=%d level=%s %s", n.Code, n.Level, n.Content)
				code = n.Code // 取最后一条：一笔单最多引出一条拒因
			}
			seenNotify = len(all)
		}
		codes = append(codes, code)
		msgs = append(msgs, st.LastMsg)
	}

	r.Logf("")
	r.Logf("  notify 数值码：%v", codes)
	switch {
	case len(codes) != 3:
		r.Logf("  ⚠️ 用例数对不上，不下结论")
	case codes[0] == 0 || codes[1] == 0 || codes[2] == 0:
		r.Logf("  ⚠️ 有一笔没拿到 notify —— 这一轮判不了先后")
	case codes[0] == codes[1]:
		r.Logf("  ⚠️ 两把标尺（真实合约偏离价 / 假合约整数倍价）码相同 ——")
		r.Logf("     **这一轮没有判别力**，无论下面怎么解读都不成立")
	case codes[2] == codes[1]:
		r.Logf("  ✅ 假合约的偏离价报的是「合约」那个码（%d），不是「整数倍」那个（%d）——",
			codes[1], codes[0])
		r.Logf("     **合约这一项排在价格类校验之前**，与 §9 的文档表一致")
	case codes[2] == codes[0]:
		r.Logf("  ❌ 假合约的偏离价报的是「整数倍」那个码 ——")
		r.Logf("     **价格类校验排在合约之前**，与 §9 的文档表相反")
	default:
		r.Logf("  ⚠️ 第三个码：既不等于 %d 也不等于 %d，照抄在上面，不硬塞进两选一",
			codes[0], codes[1])
	}
	// ⚠️ 顺带记一条结构性的事实：不存在的合约上的报单**不进委托记录**。
	if len(msgs) == 3 && msgs[1] == "" && msgs[2] == "" {
		r.Logf("")
		r.Logf("  ⚠️ 假合约那两笔的委托记录里**一个字都没有**（status 与 last_msg 全空）——")
		r.Logf("     柜台只从 notify 通道回。只读委托记录的实验会把这种拒绝")
		r.Logf("     读成「柜台没反应」，而那与「单子还挂着」长得一模一样。")
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
	return r.dump("exp-reject-tradable", "合约不可交易 vs 价格类校验：谁先被报出来")
}

// fakeMonthOf 把 rb2701 变成 rb9912 —— 同品种，一个不会有的月份。
//
// ⚠️ 用同品种是刻意的：换品种会同时改掉 tick 与乘数，
// 于是「合约不存在」与「价格不合法」又搅在一起。
func fakeMonthOf(inst string) string {
	for i := 0; i < len(inst); i++ {
		if inst[i] >= '0' && inst[i] <= '9' {
			if i == 0 {
				return ""
			}
			return inst[:i] + "9912"
		}
	}
	return ""
}
