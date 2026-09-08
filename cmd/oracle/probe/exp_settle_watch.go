package probe

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// watched 是 settle-watch 盯着的账户字段。
//
// ⚠️ 名单里**没有** balance / available / risk_ratio / float_profit / position_profit：
// 那几个随行情连续变动，盯着它们等于每一拍都「有变化」，
// 于是真正的转变会淹没在噪声里——**一个每拍都响的观察器，和不观察是一回事**。
// 这与 quoteDriven 那张白名单是同一条原理的两半。
var watchedAccount = []string{
	"pre_balance", "static_balance", "close_profit", "commission", "deposit", "withdraw",
	"frozen_margin", "frozen_commission", "margin",
}

// watchedPosition 是每个合约上盯着的持仓字段：只看结构，不看盈亏。
var watchedPosition = []string{
	"volume_long_today", "volume_long_his", "volume_short_today", "volume_short_his",
	"open_price_long", "open_price_short", "position_price_long", "position_price_short",
	"margin_long", "margin_short",
}

// watchedQuote 是每个合约上盯着的行情字段：只看结算相关的静态值。
var watchedQuote = []string{"pre_settlement", "settlement", "upper_limit", "lower_limit"}

// expSettleWatch 定时采样，**只在被盯的字段真的变了时**记录一次。
//
// 它一次回答三个欠着的问题：
//
//	结算发生在哪个时刻          —— pre_balance / close_profit 何时跳
//	今结算价何时出现            —— quote.settlement 何时由 "-" 变成有值
//	交易日跳变的秒级时刻        —— trading_day 何时改变（欠数据层会话的采样点）
//
// ⚠️ 此前这三件事记的都是**区间**而不是**点**：
// 「21:03 已经是 20260909」只能说明跳变发生在 21:03 之前。**区间不是点。**
//
// ⚠️ 只在变化时落盘是刻意的：按固定节奏落盘会把夹具目录塞满近乎相同的截面，
// 而 fixture_count 是确切数守卫——噪声会把那条守卫变成每次都要改的负担，
// 负担会让人把守卫关掉。
func (r *Runner) expSettleWatch(ctx context.Context) error {
	cli := r.cli
	every := r.Every
	if every <= 0 {
		every = 5 * time.Second
	}
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if len(r.Symbols) > 0 {
		if err := cli.SubscribeQuotes(r.Symbols...); err != nil {
			return err
		}
	}
	cli.WaitTrade(2 * time.Second)

	r.Logf("")
	r.Logf("== 结算观察：每 %v 采一次，只在被盯字段变动时记录 ==", every)
	r.Logf("  起始交易日 %s，自然日 %s", cli.TradingDay(), time.Now().Format("2006-01-02"))
	r.Logf("  盯：账户 %d 字段 / 每合约持仓 %d 字段 / 每合约行情 %d 字段",
		len(watchedAccount), len(watchedPosition), len(watchedQuote))
	r.Logf("  ⚠️ 不盯 balance / 浮动盈亏 —— 它们每拍都在动，会把真正的转变淹掉")

	snap := func() map[string]string {
		m := map[string]string{}
		m["trading_day"] = cli.TradingDay()
		acc := cli.Account()
		for _, k := range watchedAccount {
			m["account."+k] = numOrDash(acc, k)
		}
		for _, sym := range r.Symbols {
			p := cli.PositionOf(sym)
			for _, k := range watchedPosition {
				m["pos."+sym+"."+k] = numOrDash(p, k)
			}
			if q, ok := cli.QuoteOf(sym); ok {
				for _, k := range watchedQuote {
					m["quote."+sym+"."+k] = numOrDash(q.Raw, k)
				}
			}
		}
		return m
	}

	// ⚠️ 起始快照必须等行情到齐再取。
	//
	// 首跑时没等，于是第一次「变动」是 8 项 `"" → 值` —— 那是**行情订阅落地**，
	// 不是任何转变。它会以一条看起来完全合理的记录进夹具，
	// 而落盘之后与真实的结算跳变**无法区分**。
	// 这和 `status` 那次「持仓 4 手而 margin=0」是同一个病：
	// **增量协议下读早了一拍，拿到的不是空值，是一个看起来合法的数。**
	for _, sym := range r.Symbols {
		if _, ok := cli.WaitQuoteReady(sym, 30*time.Second); !ok {
			return fmt.Errorf("⚠️ %s 行情 30 秒未就绪（涨跌停价缺失）—— "+
				"此时取起始快照，第一次「变动」会是行情落地而不是结算，本次观察作废", sym)
		}
	}
	cli.WaitTrade(1500 * time.Millisecond)

	prev := snap()
	r.Logf("")
	r.Logf("  [%s] 起始快照已记录（%d 项，行情已就绪）", time.Now().Format("15:04:05"), len(prev))
	if err := r.dump("watch-start", "结算观察：起始快照"); err != nil {
		return err
	}

	changes := 0
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Logf("")
			if changes == 0 {
				// ⚠️ 阴性结果不能当阳性用。
				r.Logf("  观察结束：**一次变动都没有**。")
				r.Logf("  ⚠️ 这不说明「结算不发生」，只说明**在这段窗口里没发生**。")
				r.Logf("     换一个覆盖结算时刻的窗口重跑。")
			} else {
				r.Logf("  观察结束：记录到 %d 次变动，逐次已落盘。", changes)
			}
			return nil
		case <-tick.C:
		}
		// ⚠️ 每一拍先查连接死没死，再采样。
		//
		// 起因是一次真实的漏测：2026-09-08 结算那天，第二段刚起头两条流就断了
		// （`failed to read frame header: EOF`），而本循环继续按秒采样 ——
		// 采到的永远是最后那份截面，于是 45 分钟里一条变动都没有，
		// **日志上与「一切平静」完全一样**。结算恰好落在那段时间里。
		//
		// ⚠️ 靠「多久没消息」判断不行：休市时长时间没消息是正常的。
		// 而读循环早就看见了那个 EOF —— 准确的信号一直在，只是没被查。
		//
		// 查到就**返回错误**而不是继续：分段脚本会起下一段，
		// 那一段带着新连接。继续跑只会产出一段看起来平静的假证据。
		if err := cli.DeadErr(); err != nil {
			r.Logf("")
			r.Logf("  ⚠️⚠️ **连接已断，本段就此结束**：%v", err)
			r.Logf("     已记录 %d 次变动。⚠️ 断开之后的这段时间**没有观测**，", changes)
			r.Logf("     不要把它读成「没有变动」——**它是一段空白，不是一段平静**。")
			return fmt.Errorf("读循环终止，观测中断：%w", err)
		}
		cur := snap()
		var diff []string
		keys := make([]string, 0, len(cur))
		for k := range cur {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if prev[k] != cur[k] {
				diff = append(diff, fmt.Sprintf("%s %s → %s", k, prev[k], cur[k]))
			}
		}
		if len(diff) == 0 {
			continue
		}
		changes++
		now := time.Now()
		r.Logf("")
		r.Logf("  ⚡ [%s] 第 %d 次变动，%d 项：", now.Format("15:04:05.000"), changes, len(diff))
		for _, d := range diff {
			r.Logf("     %s", d)
		}
		if prev["trading_day"] != cur["trading_day"] {
			r.Logf("     ⚠️ **交易日跳变**：%s → %s，墙钟 %s",
				prev["trading_day"], cur["trading_day"], now.Format("2006-01-02 15:04:05.000"))
			r.Logf("        这是一个**点**，不是区间 —— 采样间隔 %v，所以精度是 ±%v", every, every)
		}
		name := fmt.Sprintf("watch-%02d", changes)
		if err := r.dump(name, "结算观察：第 "+strings.TrimSpace(fmt.Sprint(changes))+" 次变动"); err != nil {
			r.Logf("     ⚠️ 落盘失败：%v", err)
		}
		prev = cur
	}
}

// numOrDash 把一个字段渲染成可比较的文本：**有值给数，无值给 "-"**。
//
// ⚠️ 这不是显示上的讲究，它决定了今晚这批证据说的是不是实话。
//
// 上一版用的是 kq.MustNum，缺失与 "-" 都会变成 0.0000。结算发生时
// 「settlement 由 0 变成 3160」照样会触发记录，所以**探测本身是好的** ——
// 坏的是留在夹具里的那句话：柜台说的是「无值 → 3160」，而夹具写的是「0 → 3160」。
// 事后读这份证据的人分不出柜台当时报的是 0 还是没有。
//
// ⚠️ 实测（本交易日 14:2x 的 status 夹具）：结算之前 quotes.settlement
// 给的是字符串 "-"，不是 0；空仓方向的 margin_long 也是 "-"。
// 而「"-" 与 0 是两回事」正是 probes.md §9 那一批的结论 ——
// 那个结论此前只落进了库这一侧，采集器这一侧还留着旧写法。
func numOrDash(m map[string]any, key string) string {
	if m == nil {
		return "(无截面)"
	}
	v, ok := m[key]
	if !ok {
		// ⚠️ 「键不在」与「键在但柜台说无值」也要分开：
		// 前者可能是订阅没到齐，后者是柜台的一个答复。
		return "(缺字段)"
	}
	switch x := v.(type) {
	case float64:
		return fmt.Sprintf("%.4f", x)
	case string:
		return x
	case nil:
		return "(null)"
	}
	return fmt.Sprintf("(%T)", v)
}
