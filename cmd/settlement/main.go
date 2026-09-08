// Command settlement 取某个交易日的交易所结算价。
//
// ⚠️ 它是**独立于柜台**的那条通路。拿柜台自己的结算价去验柜台自己的逐日盯市，
// 是同义反复 —— 而逐日盯市的每一分钱都挂在结算价上。
//
//	go run ./cmd/settlement -day 20260908 -symbols SHFE.rb2701,SHFE.rb2610
//
// ⚠️ 目前只有上期所。其余五家在 probes.md §2 里探过端点、**没有实现**，
// 其中大商所返回 412 未打通。**少一家就是少一家，不拿别家的数据顶替，也不插值。**
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata/exchange"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

func main() {
	day := flag.String("day", "", "交易日，八位，如 20260908")
	symbols := flag.String("symbols", "", "只打这几个合约，逗号分隔；留空则只打汇总")
	out := flag.String("out", "", "把结果写成 JSON 落盘到这里")
	back := flag.Int("calendar-back", 0, "从 -day 往前探这么多个自然日，报出哪些是交易日")
	timeout := flag.Duration("timeout", 60*time.Second, "整体超时")
	flag.Parse()

	if err := run(*day, *symbols, *out, *back, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "失败：%v\n", err)
		os.Exit(1)
	}
}

func run(dayStr, symbols, out string, back int, timeout time.Duration) error {
	if dayStr == "" {
		// ⚠️ 不默认「今天」：交易日不是自然日，而本库任何地方都不自行推算交易日。
		return fmt.Errorf("必须用 -day 指定交易日（八位）—— " +
			"⚠️ 不默认「今天」：交易日不是自然日，夜盘属于下一个交易日")
	}
	day, err := types.ParseTradingDay(dayStr)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if back > 0 {
		return probeCalendar(ctx, day, back, out)
	}
	fmt.Printf("取上期所交易日 %s 的日行情\n", day)
	got, rep, err := exchange.FetchSHFE(ctx, day)
	if err != nil {
		return err
	}
	fmt.Println(rep)

	var want []string
	for _, s := range strings.Split(symbols, ",") {
		if s = strings.TrimSpace(s); s != "" {
			want = append(want, s)
		}
	}
	if len(want) > 0 {
		fmt.Println()
		fmt.Printf("  %-14s %12s %12s\n", "合约", "今结算", "昨结算")
		missing := 0
		for _, sym := range want {
			d, ok := got[sym]
			if !ok {
				// ⚠️ 缺席要明说：它与「结算价是 0」在下游长得完全不同，
				// 而后者会让逐日盯市把整个持仓算成归零。
				fmt.Printf("  %-14s %12s —— ⚠️ **不在这份日行情里**\n", sym, "缺席")
				missing++
				continue
			}
			pre := "无"
			if d.HasPre {
				pre = d.PreSettlement.String()
			}
			fmt.Printf("  %-14s %12s %12s\n", sym, d.Settlement, pre)
		}
		if missing > 0 {
			return fmt.Errorf("⚠️ %d 个要的合约不在这份日行情里 —— "+
				"非上期所合约请另找来源；**不拿别家的数据顶替**", missing)
		}
	}

	if out == "" {
		return nil
	}
	type wire struct {
		Instrument    string `json:"instrument"`
		Settlement    string `json:"settlement"`
		PreSettlement string `json:"pre_settlement,omitempty"`
	}
	rows := make([]wire, 0, len(got))
	for sym, d := range got {
		w := wire{Instrument: sym, Settlement: d.Settlement.String()}
		if d.HasPre {
			w.PreSettlement = d.PreSettlement.String()
		}
		rows = append(rows, w)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Instrument < rows[j].Instrument })
	payload := struct {
		Source     string `json:"source"`
		TradingDay string `json:"trading_day"`
		Note       string `json:"note"`
		Rows       []wire `json:"settlements"`
	}{
		Source:     fmt.Sprintf(exchange.SHFEDailyURL, day.String()),
		TradingDay: day.String(),
		Note: "上期所日行情。⚠️ 这是**交易所**的结算结果，独立于柜台 —— " +
			"拿柜台自己的结算价去验柜台自己的逐日盯市是同义反复。" +
			"⚠️ 只含上期所；其余五家未实现，大商所 412 未打通。",
		Rows: rows,
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	if err := enc.Encode(payload); err != nil {
		return err
	}
	fmt.Printf("\n已写入 %s（%d 个合约）\n", out, len(rows))
	return nil
}

// probeCalendar 从 day 往前探 back 个自然日，报出哪些是交易日。
//
// ⚠️ 交易日列表是 refdata 快照缺的四块之一，而它**只能由交易所回答**：
// 「哪天开市」不是行情，是安排。本函数把它落成可复现的枚举。
//
// ⚠️ 三态照实记，不合并：
//
//	交易日（已结算）  日行情有且结算价填好了
//	交易日（未结算）  日行情已发布但结算价全空 —— 当天盘中就是这样
//	日行情取不到      对**过去的**日期等价于非交易日；对今天什么都不说明
//
// 把最后一类直接当成「非交易日」，会在每次「今天的还没发」时少算一个交易日，
// 而少一个交易日会让此后每一次今昨仓滚动错位，且不报错。
func probeCalendar(ctx context.Context, from types.TradingDay, back int, out string) error {
	fmt.Printf("从 %s 往前探 %d 个自然日\n", from, back)
	fmt.Println("⚠️ 每天一次请求，慢；结果照实记三态，不把「取不到」当成「非交易日」")

	type row struct {
		Day    string `json:"day"`
		Status string `json:"status"`
	}
	var rows []row
	counts := map[exchange.DayStatus]int{}
	d := from.CalendarDate()
	for i := 0; i < back; i++ {
		day := types.NewTradingDay(d.Year(), int(d.Month()), d.Day())
		st, err := exchange.ProbeDay(ctx, day)
		if err != nil {
			// ⚠️ 探不动就停，不跳过：跳过会在列表里留一个**看不出来的洞**，
			// 而一个缺了几天的交易日历比没有日历更危险。
			return fmt.Errorf("探 %s 失败：%w —— **就此停下**，"+
				"跳过会在日历里留一个看不出来的洞", day, err)
		}
		counts[st]++
		rows = append(rows, row{day.String(), st.String()})
		fmt.Printf("  %s  %s\n", day, st)
		d = d.AddDate(0, 0, -1)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Day < rows[j].Day })

	fmt.Println()
	fmt.Printf("交易日（已结算）%d  交易日（未结算）%d  取不到 %d\n",
		counts[exchange.DayTrading], counts[exchange.DaySettling],
		counts[exchange.DayNotPublished])
	// ⚠️ 判别力：一天交易日都没探到，说明端点或判据坏了 ——
	// 而那时输出的「全都不是交易日」看起来完全像一段长假。
	if counts[exchange.DayTrading]+counts[exchange.DaySettling] == 0 {
		return fmt.Errorf("⚠️ %d 天里一个交易日都没有 —— "+
			"那看起来像一段长假，也可能是端点或判据坏了。**先查清楚再用这份日历**", back)
	}
	if out == "" {
		return nil
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	if err := enc.Encode(struct {
		Source string `json:"source"`
		Note   string `json:"note"`
		Days   []row  `json:"days"`
	}{
		Source: "上期所日行情逐日探测",
		Note: "⚠️ 只覆盖上期所。三态照实记：「取不到」对过去的日期等价于非交易日，" +
			"对今天什么都不说明。⚠️ 交易日历是交易所的安排，不是行情。",
		Days: rows,
	}); err != nil {
		return err
	}
	fmt.Printf("\n已写入 %s（%d 天）\n", out, len(rows))
	return nil
}
