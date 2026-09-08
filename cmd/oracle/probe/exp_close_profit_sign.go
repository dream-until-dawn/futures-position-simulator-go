package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// closeProfitWindow 是第 1 步允许等待的上限。
//
// ⚠️ 它必须有上限，因为这一步**市场相关**：需要行情离开开仓区间。
// 而它排在整批最前面，等不到就会把整个窗口耗光。
// 昨仓批不是一次性的——种子过了下一次结算仍然是昨仓——
// **所以拿窗口去赌这一步，赔率是不对的**：等不到就跳过，下一个交易日开头再来。
const closeProfitWindow = 20 * time.Minute

// expCloseProfitSign 构造一笔**为正**的平仓盈亏，用来分开 CTP 的
// `AvailIncludeCloseProfit`（平仓盈利算不算进可用）。
//
// ⚠️ 这个参数只在平仓盈利**为正**时才有判别力：为负或为零时，
// 「包含」与「不包含」给出同一个数。全部既有夹具的 `close_profit` 都是负数，
// 所以此前所有的人工核对都发现不了它——见 silent-risks.md 第 19 条。
//
// ⚠️ 做法刻意**方向中性**：同一合约同时开一多一空，
// 行情往哪边走都会有一条腿盈利，平掉盈利那条即可。
// 这样不需要对方向下注——而**需要押注才能构造的样本，
// 它的成本和它的效度会一起随行情走**。
//
// 代价是多一手保证金与两笔手续费；收益是判据不依赖行情方向，只依赖行情**动**。
func (r *Runner) expCloseProfitSign(ctx context.Context) error {
	if len(r.Symbols) != 1 {
		return fmt.Errorf("本实验需要**恰好一个**合约（同合约一多一空），得到 %d 个", len(r.Symbols))
	}
	sym := r.Symbols[0]
	cli := r.cli
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}
	p := cli.PositionOf(sym)
	if v := kq.MustNum(p, "volume_long") + kq.MustNum(p, "volume_short"); v > 0 {
		return fmt.Errorf("%s 上已有 %.0f 手持仓，平仓时分不清平的是谁的手", sym, v)
	}

	// ⚠️ 前置守卫：当日累计的平仓盈亏必须还没被压成负数。
	//
	// close_profit 是**当日累计**字段。已经亏了 N 之后，要把它翻正就得先赚回 N——
	// 而每跑一条实验都会因手续费与买卖价差净亏，所以这个成本**单调上升**。
	// 这正是本实验必须排在新交易日最前面的原因。
	cli.WaitTrade(2 * time.Second)
	c0 := kq.MustNum(cli.Account(), "close_profit")
	if c0 < 0 {
		return fmt.Errorf("⚠️ 当日累计 close_profit 已是 %.2f，要翻正得先净赚 %.2f —— "+
			"**本实验必须排在新交易日的最前面**，此刻构造成本已经涨上去了。"+
			"改到下一个交易日开头再跑", c0, -c0)
	}

	r.Logf("")
	r.Logf("== 第 1 步：构造一笔为正的平仓盈亏（方向中性）==")
	r.Logf("  当日累计 close_profit=%.2f（必须 ≥ 0，已通过）", c0)

	// 两条腿。买单成交在卖一、卖单成交在买一。
	if _, err := r.openOneLot(sym, kq.Buy); err != nil {
		return err
	}
	if _, err := r.openOneLot(sym, kq.Sell); err != nil {
		_ = r.flatten(sym, kq.Buy)
		return err
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		q := cli.PositionOf(sym)
		return kq.MustNum(q, "volume_long_today") > 0 && kq.MustNum(q, "volume_short_today") > 0
	}) {
		_ = r.flatten(sym, kq.Buy)
		_ = r.flatten(sym, kq.Sell)
		return fmt.Errorf("两条腿没有同时建成")
	}
	cli.WaitTrade(2 * time.Second)
	p = cli.PositionOf(sym)
	longPx := kq.MustNum(p, "open_price_long")
	shortPx := kq.MustNum(p, "open_price_short")
	r.Logf("  多头开在 %.2f，空头开在 %.2f —— 等行情离开这个区间", longPx, shortPx)

	// 等到任意一条腿浮盈。多头要 买一 > 多头开仓价；空头要 卖一 < 空头开仓价。
	var winner kq.Direction
	got := cli.WaitUntil(closeProfitWindow, func() bool {
		q, ok := cli.QuoteOf(sym)
		if !ok {
			return false
		}
		if q.BidPrice1 > longPx {
			winner = kq.Buy
			return true
		}
		if q.AskPrice1 > 0 && q.AskPrice1 < shortPx {
			winner = kq.Sell
			return true
		}
		return false
	})
	if !got {
		e1 := r.flatten(sym, kq.Buy)
		e2 := r.flatten(sym, kq.Sell)
		// ⚠️ 阴性结果不能当阳性用：没等到行情动，不等于这个参数没判别力。
		return fmt.Errorf("⚠️ %v 内没有任何一条腿走出浮盈（多开 %.2f / 空开 %.2f），"+
			"**判据不成立**，不是结论。跳过本条去跑昨仓批，下个交易日开头再来。收尾：%v / %v",
			closeProfitWindow, longPx, shortPx, e1, e2)
	}
	q, _ := cli.QuoteOf(sym)
	r.Logf("  盘口 买一=%.2f 卖一=%.2f → **%s** 那条腿浮盈，平它", q.BidPrice1, q.AskPrice1, winner)

	if err := r.flatten(sym, winner); err != nil {
		_ = r.flatten(sym, kq.Buy)
		_ = r.flatten(sym, kq.Sell)
		return fmt.Errorf("平盈利腿失败：%w", err)
	}
	cli.WaitTrade(3 * time.Second)

	acc := cli.Account()
	closeP := kq.MustNum(acc, "close_profit")
	bal := kq.MustNum(acc, "balance")
	avail := kq.MustNum(acc, "available")
	margin := kq.MustNum(acc, "margin")
	fm := kq.MustNum(acc, "frozen_margin")
	fc := kq.MustNum(acc, "frozen_commission")
	fp := kq.MustNum(acc, "frozen_premium")

	r.Logf("")
	r.Logf("  平掉盈利腿之后：close_profit=%.4f  balance=%.4f  available=%.4f  margin=%.2f",
		closeP, bal, avail, margin)

	if err := r.dump("exp-close-profit-sign", "第 1 步：平仓盈利为正时 Available 怎么算"); err != nil {
		return err
	}

	// 收尾放在判定之前。
	if err := r.flatten(sym, kq.Buy); err != nil {
		r.Logf("  ⚠️ 收尾平多头失败：%v", err)
	}
	if err := r.flatten(sym, kq.Sell); err != nil {
		r.Logf("  ⚠️ 收尾平空头失败：%v", err)
	}

	// —— 样本守卫 ——
	if closeP <= 0 {
		return fmt.Errorf("⚠️ 平掉盈利腿之后 close_profit=%.4f，仍未为正 —— "+
			"两个取值在此恒等，**判据不成立**", closeP)
	}

	// —— 判定 ——
	inc := bal - margin - fm - fc - fp
	exc := inc - closeP
	r.Logf("")
	r.Logf("  两个候选各自预测 available：")
	r.Logf("    包含平仓盈利（'0'）  = balance − margin − 冻结            = %.4f", inc)
	r.Logf("    不包含（'2'）        = 上式 − close_profit(%.4f)          = %.4f", closeP, exc)
	r.Logf("    实测 available                                            = %.4f", avail)
	r.Logf("")
	switch {
	case abs(avail-inc) < 0.01:
		r.Logf("  结论：**平仓盈利算进可用**（`AvailIncludeCloseProfit = '0'`）。")
		r.Logf("        本库 §8 的 Available 公式无需改动。")
	case abs(avail-exc) < 0.01:
		r.Logf("  结论：**平仓盈利不算进可用**（`AvailIncludeCloseProfit = '2'`）。")
		r.Logf("        ⚠️ 本库 §8 的 Available 公式要加一项，且它**只在盈利为正时**生效——")
		r.Logf("           一个只在特定符号下出现的减项，比恒为 0 的减项更难被发现。")
	default:
		r.Logf("  ⚠️ 两个候选都对不上（差 %.4f / %.4f）—— 还有本实验没考虑的成分。",
			avail-inc, avail-exc)
		r.Logf("     **本实验不收敛**，不得写进规则文档的实测栏。")
	}
	return nil
}
