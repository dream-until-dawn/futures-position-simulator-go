package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata/exchange"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestRunRefusesDefaultingToToday 断言 `-day` **没有默认值**。
//
// ⚠️ 这条不是洁癖，它正对着本项目 20260910 栽过的那件事：
// 自然日 2026-09-09 的夜盘与自然日 2026-09-10 的日盘**是同一个交易日 20260910**。
// 一个「默认今天」会在夜盘时段整体偏一个交易日，
// 而偏出来的那个日期**看起来完全正常** —— 八位、合法、就是昨天。
//
//	⚠️ 交易日不是自然日，而本库任何地方都不自行推算交易日。
func TestRunRefusesDefaultingToToday(t *testing.T) {
	err := run("", "SHFE.rb2701", "", 0, time.Second)
	if err == nil {
		t.Fatal("⚠️ 没给 -day 却跑起来了 —— 它只能默认成「今天」，而交易日不是自然日")
	}
	if !strings.Contains(err.Error(), "不默认") {
		t.Errorf("⚠️ 报错了，但不是那条诊断：%v", err)
	}
	// ⚠️ 判别力：格式错的也必须被拒，且理由不同。
	// 只测空串那一侧的话，一个「永远报错」的实现也能过。
	if err := run("2026-09-10", "SHFE.rb2701", "", 0, time.Second); err == nil {
		t.Error("⚠️ 带横杠的日期被接受了 —— ParseTradingDay 要的是八位")
	}
}

// TestProbeCalendarStopsOnError 断言**探不动就停，不跳过**。
//
// ⚠️ 这条守卫此前一次都没被验过：它只有连着上期所的服务器、
// 并且那台服务器恰好出故障时才走得到。
//
//	一条只有在上游出故障时才执行的分支，正是最不可能被人看见的那种。
//
// 跳过的后果：日历里留一个**看不出来的洞** —— 那一天既不在「交易日」里
// 也不在「非交易日」里，而列表本身长得完全正常。
// 一份缺了几天的交易日历比没有日历更危险。
func TestProbeCalendarStopsOnError(t *testing.T) {
	asked := 0
	ask := func(_ context.Context, day types.TradingDay) (exchange.DayStatus, error) {
		asked++
		if asked == 2 {
			return exchange.DayUnknown, fmt.Errorf("模拟的上游故障")
		}
		return exchange.DayTrading, nil
	}
	err := probeCalendar(context.Background(), 20260910, 5, "", ask)
	if err == nil {
		t.Fatal("⚠️ 中途探不动却照样跑完了 —— 那会在日历里留一个看不出来的洞")
	}
	if !strings.Contains(err.Error(), "就此停下") {
		t.Errorf("⚠️ 报错了，但不是那条诊断：%v", err)
	}
	// ⚠️ 判别力所在：**停下**意味着后面三天一次都没问过。
	// 只查「返回了错误」的话，一个「问完五天再报错」的实现也能过 ——
	// 而那个实现会把三次多余的请求打到上期所，且日历照样是残的。
	if asked != 2 {
		t.Errorf("⚠️ 探了 %d 天，应当在第 2 天就停 —— "+
			"「报了错」与「停下了」是两件事", asked)
	}
	// 反面：一路顺利时必须问满 5 天。否则上面那个 asked==2
	// 也可能只是因为它根本没在循环。
	asked = 0
	ok := func(context.Context, types.TradingDay) (exchange.DayStatus, error) {
		asked++
		return exchange.DayTrading, nil
	}
	if err := probeCalendar(context.Background(), 20260910, 5, "", ok); err != nil {
		t.Fatalf("⚠️ 一路顺利却报错了：%v", err)
	}
	if asked != 5 {
		t.Errorf("⚠️ 顺利时只探了 %d 天，应为 5 —— 上面那个「第 2 天就停」可能是假的", asked)
	}
}
