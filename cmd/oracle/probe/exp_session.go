package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// quoteDriven 是**允许**在同一交易日内变动的持仓字段：它们全部由行情驱动。
//
// ⚠️ 这张表是本实验的判据本身，所以它是白名单而不是黑名单：
// 名单外的任何字段变了都算发现。黑名单漏一个 → 静默放过；
// 白名单漏一个 → 报一个假发现，而假发现会被人看见。
var quoteDriven = map[string]bool{
	"last_price":            true,
	"float_profit":          true,
	"float_profit_long":     true,
	"float_profit_short":    true,
	"position_profit":       true,
	"position_profit_long":  true,
	"position_profit_short": true,
	"market_value":          true,
	"market_value_long":     true,
	"market_value_short":    true,
	"market_status":         true,
}

// expSessionCheck 是 settle-check 的**对照组**：跨一个时段边界，但**不跨结算**。
//
// ⚠️ 它的存在理由是归因，而不是好奇。
//
// 今晚的第 0 步要判「结算做了什么」，做法是拿今晚的截面与交易日 20260908 的基线比。
// 但周一夜盘收盘（23:00）到周二日盘开盘（09:00）之间**也隔着一个时段边界**，
// 而那里没有结算。若不先量出「只跨时段时什么**不变**」，
// 今晚看到的变化就无法归因——**它可能只是又跨了一个时段。**
//
// 判据：与**同一交易日**的既有基线相比，只有 quoteDriven 名单里的字段允许变。
// 今昨仓量、开仓价、持仓均价（逐日盯市基线）、保证金若在同一交易日内变了，
// 那本身就是一条结论。
func (r *Runner) expSessionCheck(ctx context.Context) error {
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

	todayNatural := time.Now().Format("2006-01-02")
	base, err := latestSameDayBaseline(r.DumpDir, day, todayNatural)
	if err != nil {
		return err
	}
	r.Logf("")
	r.Logf("== 时段边界对照组：同一交易日内，跨时段但**不跨结算** ==")
	r.Logf("  基线：%s（交易日 %s，采于 %s）", base.Path, base.TradingDay, base.CapturedAt)
	r.Logf("  现在：交易日 %s，自然日 %s —— 基线在自然日 %s，中间跨过停盘",
		day, todayNatural, base.CapturedAt[:10])

	// ⚠️ 样本守卫一：基线必须**含有持仓**，否则「今昨仓没滚」无从观察。
	if len(base.Positions) == 0 {
		return fmt.Errorf("⚠️ 基线 %s 里没有持仓截面 —— 本实验要观察的正是持仓字段变没变，"+
			"没有持仓则判据不成立", base.Path)
	}

	// ⚠️ 样本守卫二：基线之后**不得发生过成交**。
	//
	// 这是首跑那次假阳性的真正病因：基线采于我建种子之前，
	// 于是「我自己下的单」被报成了「时段边界改动了持仓」。
	// commission 与 close_profit 只要有一笔成交就会动，用它们核最省。
	acc := cli.Account()
	nowComm := kq.MustNum(acc, "commission")
	nowClose := kq.MustNum(acc, "close_profit")
	r.Logf("  期间成交核对：commission %.4f → %.4f   close_profit %.4f → %.4f",
		base.Commission, nowComm, base.CloseProfit, nowClose)
	if nowComm != base.Commission || nowClose != base.CloseProfit {
		return fmt.Errorf("⚠️ 基线 %s 之后**发生过成交**（commission %.4f→%.4f，"+
			"close_profit %.4f→%.4f）—— 持仓字段的变化归因不了给时段边界，**判据不成立**。"+
			"需要一份「此后没有再交易」的基线",
			base.Path, base.Commission, nowComm, base.CloseProfit, nowClose)
	}

	changedOutside, checked := 0, 0
	var syms []string
	for s := range base.Positions {
		syms = append(syms, s)
	}
	sort.Strings(syms)

	for _, sym := range syms {
		pb := base.Positions[sym]
		if kq.MustNum(pb, "volume_long")+kq.MustNum(pb, "volume_short") <= 0 {
			continue // 零手数的记录残留，没有观察价值
		}
		pn := cli.PositionOf(sym)
		if len(pn) == 0 {
			r.Logf("  ⚠️ %s 在当前截面里不存在了 —— 持仓记录消失本身就是一条发现", sym)
			changedOutside++
			continue
		}
		var moved, held []string
		for k, vb := range pb {
			vn, ok := pn[k]
			if !ok {
				continue
			}
			if fmt.Sprint(vb) == fmt.Sprint(vn) {
				continue
			}
			if quoteDriven[k] {
				moved = append(moved, k)
				continue
			}
			held = append(held, fmt.Sprintf("%s %v → %v", k, vb, vn))
		}
		checked++
		sort.Strings(held)
		r.Logf("")
		r.Logf("  %s：行情驱动字段变了 %d 个；**名单外**变了 %d 个", sym, len(moved), len(held))
		for _, h := range held {
			r.Logf("     ⚠️ %s", h)
			changedOutside++
		}
		// 关键字段单独打出来，便于人眼复核。
		for _, k := range []string{"volume_long_today", "volume_long_his",
			"open_price_long", "position_price_long", "margin_long", "last_price"} {
			r.Logf("     %-22s %v → %v", k, pb[k], pn[k])
		}
	}

	if err := r.dump("session-boundary", "时段边界对照组：同一交易日内跨时段，不跨结算"); err != nil {
		return err
	}

	if checked == 0 {
		return fmt.Errorf("⚠️ 基线里没有一个带手数的合约 —— 本次没有观察对象，判据不成立")
	}

	r.Logf("")
	if changedOutside == 0 {
		r.Logf("  【结论】跨时段边界，**行情之外的字段一个都没变**（%d 个合约）。", checked)
		r.Logf("        今昨仓不滚、逐日盯市基线不重置、保证金不重算。")
		r.Logf("        ⚠️ 这条本身不新鲜，它的用处是**归因**：")
		r.Logf("           今晚若这些字段变了，就只能归给**结算**，不能归给「又跨了个时段」。")
	} else {
		r.Logf("  ⚠️ 【结论】同一交易日内有 %d 处**行情之外**的变动。", changedOutside)
		r.Logf("     那意味着时段边界本身会动这些字段 —— ")
		r.Logf("     今晚 settle-check 看到的变化将**无法单独归因给结算**。")
	}
	return nil
}

// sameDayBaseline 是同一交易日里可用作对照的一份截面。
type sameDayBaseline struct {
	Path        string
	TradingDay  string
	CapturedAt  string
	Commission  float64
	CloseProfit float64
	Positions   map[string]map[string]any
}

// latestSameDayBaseline 找**同一交易日**里最近的一份含持仓的夹具。
//
// ⚠️ 「怎么算一个合格的对照基线」——这一条第一版没写，于是首跑就假阳性了。
//
// 第一版取的是当日**最早**的一份，理由是「时段边界越多，对照越强」。
// 那个理由只考虑了时段数，**漏了期间有没有交易**：最早那份采于种子建立之前，
// 于是 `volume_long_today 1 → 2`、`open_price_long 3149 → 3151` 全都被报成
// 「时段边界改动了持仓」——而它们其实是**我自己下的单**。
//
// 合格的对照基线要同时满足两条：
//
//	① 同一交易日（否则中间隔着结算，那是 settle-check 的事）
//	② **此后没有发生过成交**（否则变化归因不了给时段边界）
//
//	③ 与现在**不在同一个自然日**
//
// ⚠️ 第 ③ 条是第二次修补：只加第 ② 条之后它挑了 2 分钟前的截面，
// `last_price 3158 → 3158`，**一道时段边界都没跨**——判据当场空转。
// 第一版注释里我自己写过这个权衡（「取最近的会让跨了几个时段缩到 0」），
// 然后从另一头撞了上去。
//
// 用**自然日**而不是交易日来判，是因为时段是墙钟上的东西：
// 跨自然日就必然跨过 23:00–09:00 那道停盘。
// （这正好是刚立的日期约定在代码里第一次派上用场。）
//
// 第 ② 条在调用处用 commission / close_profit 是否变动来核，那两个字段只要有一笔成交就会动。
func latestSameDayBaseline(dir, day, todayNatural string) (sameDayBaseline, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return sameDayBaseline{}, err
	}
	sort.Strings(paths)
	var best sameDayBaseline
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var f struct {
			TradingDay string                    `json:"trading_day"`
			CapturedAt string                    `json:"captured_at"`
			Account    map[string]any            `json:"account"`
			Positions  map[string]map[string]any `json:"positions"`
		}
		if json.Unmarshal(b, &f) != nil || f.TradingDay != day || len(f.Positions) == 0 {
			continue
		}
		lots := 0.0
		for _, pos := range f.Positions {
			lots += kq.MustNum(pos, "volume_long") + kq.MustNum(pos, "volume_short")
		}
		if lots <= 0 {
			continue
		}
		// ③ 必须与现在不在同一个自然日 —— 否则没跨过停盘，对照为空。
		if len(f.CapturedAt) < 10 || f.CapturedAt[:10] == todayNatural {
			continue
		}
		if best.Path == "" || f.CapturedAt > best.CapturedAt {
			best = sameDayBaseline{filepath.Base(p), f.TradingDay, f.CapturedAt,
				kq.MustNum(f.Account, "commission"), kq.MustNum(f.Account, "close_profit"),
				f.Positions}
		}
	}
	if best.Path == "" {
		return sameDayBaseline{}, fmt.Errorf("交易日 %s 里找不到「带持仓、且不在自然日 %s」的基线截面 —— "+
			"没有跨过停盘的基线就没有对照，本实验不成立", day, todayNatural)
	}
	return best, nil
}
