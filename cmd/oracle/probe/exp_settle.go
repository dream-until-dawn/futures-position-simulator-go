package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// settleVerdict 是第 0 步的三种结果之一。
//
// ⚠️ **三种，不是两种。** 我原先的计划只写了「种子变昨仓 → 往下跑」与
// 「没变 → 整批作废」，而那把第三种情形归进了作废：
//
//	结算发生了，但今仓**没有**滚成昨仓。
//
// 那不是故障，是**一条真结论**：「快期做日终结算但不滚今昨」——
// 它会直接改写第 7 条（`volume_long_yd` vs `_his`）与第 4 条（平仓消耗顺序）的前提。
// ⚠️ 把它归进「作废」，等于把这批实验里可能最重要的发现丢掉。
type settleVerdict int

const (
	settleUnknown settleVerdict = iota
	settleNotRun                // 结算没发生
	settleNoRoll                // 结算发生了，但今仓没滚成昨仓 —— 第三种
	settleRolled                // 正常：结算发生且今仓已滚昨仓
)

// baseline 是上一个交易日留下的截面，用来判断「变了没有」。
type baseline struct {
	Path        string
	TradingDay  string
	PreBalance  float64
	CloseProfit float64
	// TodayLots 是基线截面里的今仓手数。
	//
	// ⚠️ 没有它，第三种结论会变成假阳性：**结算之后才建的仓本来就是今仓**，
	// 判它「没滚」等于判一件根本没到期的事。干跑时这条就地发生了——
	// 拿 09-07 当基线去看 09-08 才建的种子，探针报了「柜台不滚今昨仓」。
	TodayLots float64
}

// latestBaselineBefore 在夹具目录里找**交易日严格早于 day** 的最近一份账户截面。
//
// ⚠️ 它靠的正是「夹具只因被证明是错的而删」那条保留策略（probes.md §7.4）：
// 判断「结算发生过没有」需要一份**旧**截面，而旧截面恰恰是最容易被当成过期数据清掉的。
func latestBaselineBefore(dir, day string) (baseline, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return baseline{}, err
	}
	cur, _ := strconv.Atoi(day)
	var best baseline
	bestDay := 0
	sort.Strings(paths)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f struct {
			TradingDay string                    `json:"trading_day"`
			Account    map[string]any            `json:"account"`
			Positions  map[string]map[string]any `json:"positions"`
		}
		if json.Unmarshal(b, &f) != nil || len(f.Account) == 0 {
			continue
		}
		d, err := strconv.Atoi(f.TradingDay)
		if err != nil || d >= cur || d < bestDay {
			continue
		}
		bestDay = d
		best = baseline{Path: filepath.Base(p), TradingDay: f.TradingDay,
			PreBalance:  kq.MustNum(f.Account, "pre_balance"),
			CloseProfit: kq.MustNum(f.Account, "close_profit")}
	}
	if bestDay == 0 {
		return baseline{}, fmt.Errorf("夹具目录 %s 里找不到交易日早于 %s 的账户截面 —— "+
			"没有基线就判不出「变了没有」，本步作废", dir, day)
	}
	return best, nil
}

// expSettleCheck 是昨仓批的**第 0 步**：结算到底发生了没有，今昨仓滚了没有。
//
// ⚠️ 它必须在任何昨仓实验之前跑，而且它的三种结果对应三种完全不同的后续动作。
// 判据刻意用了**与持仓无关**的指标，因为「持仓没滚」正是待判的事情之一：
//
//	account.pre_balance         是否推进成上一交易日的收盘结存
//	account.close_profit        是否归零（顺带把那句「推得」升成实测或就地推翻）
//	quote.pre_settlement        是否更新成上一交易日的结算价
func (r *Runner) expSettleCheck(ctx context.Context) error {
	cli := r.cli
	day := cli.TradingDay()
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if len(r.Symbols) > 0 {
		if err := cli.SubscribeQuotes(r.Symbols...); err != nil {
			return err
		}
	}
	cli.WaitTrade(2 * time.Second)

	base, err := latestBaselineBefore(r.DumpDir, day)
	if err != nil {
		return err
	}
	acc := cli.Account()
	preBal := kq.MustNum(acc, "pre_balance")
	closeP := kq.MustNum(acc, "close_profit")

	r.Logf("")
	r.Logf("== 第 0 步：结算发生了没有，今昨仓滚了没有 ==")
	r.Logf("  基线：%s（交易日 %s）", base.Path, base.TradingDay)
	r.Logf("  当前交易日：%s", day)
	r.Logf("")
	r.Logf("  与持仓无关的三个指标：")
	r.Logf("    pre_balance   基线 %.4f → 现在 %.4f", base.PreBalance, preBal)
	r.Logf("    close_profit  基线 %.4f → 现在 %.4f", base.CloseProfit, closeP)
	r.Logf("    基线截面的今仓：%.0f 手", base.TodayLots)

	// —— 指标一：pre_balance 推进 ——
	advanced := preBal != base.PreBalance

	// —— 指标二：close_profit 归零。⚠️ 它同时是对一句「推得」的实测。 ——
	r.Logf("")
	switch {
	case base.CloseProfit == 0:
		r.Logf("  ⚠️ 基线的 close_profit 本来就是 0 —— 这次读到 0 **什么也不说明**。")
		r.Logf("     「跨结算归零」这句仍然只是**推得**，判别力为零。")
	case closeP == 0:
		r.Logf("  ✅ close_profit 由 %.2f 归零 —— 「跨结算归零」由**推得**升为**实测**。", base.CloseProfit)
		r.Logf("     第 11 条的便宜路径成立：一笔赚 1 个 tick 的往返即可。")
	default:
		r.Logf("  ⚠️ close_profit 仍是 %.2f，**没有归零** —— 那句「推得」当场被推翻。", closeP)
		r.Logf("     ⚠️ 第 11 条的便宜路径**作废**：构造一笔正的平仓盈利要先净赚 %.2f。", -closeP)
		r.Logf("     正确动作是**跳过第 11 条**去跑昨仓批，不要在窗口里追一个已经不成立的前提。")
	}

	// —— 指标三：昨结算价更新 ——
	updated := 0
	for _, sym := range r.Symbols {
		if q, ok := cli.QuoteOf(sym); ok {
			r.Logf("    %-14s 昨结算价 %.4f  最新 %.4f", sym, q.PreSettlement, q.LastPrice)
			if q.PreSettlement > 0 {
				updated++
			}
		}
	}

	// —— 持仓：今昨仓滚了没有 ——
	var todayLots, hisLots float64
	for sym := range cli.Positions() {
		p := cli.PositionOf(sym)
		todayLots += kq.MustNum(p, "volume_long_today") + kq.MustNum(p, "volume_short_today")
		hisLots += kq.MustNum(p, "volume_long_his") + kq.MustNum(p, "volume_short_his")
	}
	r.Logf("")
	r.Logf("  持仓：今仓 %.0f 手 / 昨仓 %.0f 手", todayLots, hisLots)

	if err := r.dump("step0-settle-check", "第 0 步：结算与今昨仓滚动的三态判定"); err != nil {
		return err
	}

	// —— 三态判定 ——
	// ⚠️ 「基线里有今仓」是第三种结论的**前提**，不是细节。
	//
	// 结算之后才建的仓本来就是今仓，判它「没滚」是在判一件没到期的事。
	// 干跑时正是这样：拿 20260907 当基线去看 20260908 才建的种子，
	// 探针一路走到第三种结论并报了「柜台不滚今昨仓」——**假阳性，而且是最贵的那个**。
	baselineHadToday := base.TodayLots > 0

	verdict := settleUnknown
	switch {
	case !advanced:
		verdict = settleNotRun
	case hisLots > 0:
		verdict = settleRolled
	case todayLots > 0 && baselineHadToday:
		verdict = settleNoRoll
	}

	r.Logf("")
	switch verdict {
	case settleRolled:
		r.Logf("  【结论】结算已发生（pre_balance 已推进），且今仓已滚成昨仓 %.0f 手。", hisLots)
		r.Logf("          昨仓批可以往下跑。")
	case settleNotRun:
		r.Logf("  ⚠️ 【结论】**结算没有发生**：pre_balance 仍是 %.4f，与基线相同。", preBal)
		r.Logf("     昨仓批**整批作废** —— 没有昨仓，那些实验测的是别的东西。")
		return fmt.Errorf("结算未发生，昨仓批不具备前提")
	case settleNoRoll:
		r.Logf("  ⚠️ 【结论】**第三种情形**：结算发生了（pre_balance 已推进），")
		r.Logf("     但持仓仍全是今仓（今 %.0f / 昨 %.0f）。", todayLots, hisLots)
		r.Logf("     ⚠️ 这**不是故障，是一条结论**：本柜台做日终结算但**不滚今昨仓**。")
		r.Logf("     它直接改写第 7 条与第 4 条的前提 —— 那两条问的是「昨仓怎么算」，")
		r.Logf("     而在这个口子上昨仓根本不出现。**先报告，不要继续跑昨仓批。**")
		return fmt.Errorf("结算已发生但今昨仓未滚动 —— 这是结论，不是故障，需人工裁决后续")
	default:
		if todayLots > 0 && !baselineHadToday {
			r.Logf("  ⚠️ 【判据不成立】结算已发生，账上有 %.0f 手今仓，", todayLots)
			r.Logf("     但**基线截面里一手今仓都没有**（基线 %s，交易日 %s）。", base.Path, base.TradingDay)
			r.Logf("     这些仓很可能是在那次结算**之后**建的 —— 它们本来就该是今仓，")
			r.Logf("     判它们「没滚」是在判一件没到期的事。")
			r.Logf("     ⚠️ 换一份**含有这批今仓**的基线重跑；在那之前，")
			r.Logf("        既不能下「不滚今昨」的结论，也不能下「一切正常」的结论。")
			return fmt.Errorf("基线截面里没有今仓，第三种结论无判别力")
		}
		r.Logf("  ⚠️ 【结论】账上没有持仓，判不出滚动与否。")
		r.Logf("     pre_balance 已推进=%v。种子可能被平掉了。", advanced)
		return fmt.Errorf("账上无持仓，第 0 步无判别力")
	}
	return nil
}
