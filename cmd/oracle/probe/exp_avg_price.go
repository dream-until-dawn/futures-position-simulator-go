package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expAvgPrice 让**加权均价**第一次被真正考验。
//
// ⚠️ 起因是一次横向统计（probes.md §9）：现有 20 份 2 手样本里，
// 两笔全是同一个价 —— 于是 `open_price_long = 单笔价`，
// 而「柜台到底算不算加权平均」这件事**一次都没被问过**。
// 单看持仓截面看不出这一点，是成交截面进夹具之后才看见的。
//
// 判据设计成**输出上不可伪造**：
//
//	已有 2 手 @P，再加 1 手 @P±1tick
//	→ 均价 = (2P + P±1)/3 = P ± 1/3 tick
//
// 那是一个**任何单笔成交价都取不到的值**。柜台若不做加权平均，
// 无论它取首笔、末笔、还是最新价，都给不出这个数。
//
// ⚠️ 而它的价值在今晚才完全兑现：这批仓过夜结算之后，
// `open_price` 停在这个加权均价上（逐笔对冲基线，永不改变），
// `position_price` 被重置成昨结算价（逐日盯市基线）——
// **两个基线第一次会取到不同的数**，而那正是本项目最核心的那条区分。
// 在此之前所有样本上二者恒等，也就是那条区分从未被验证过。
func (r *Runner) expAvgPrice(ctx context.Context) error {
	if len(r.Symbols) != 1 {
		return fmt.Errorf("本实验需要 -symbols 指定**恰好一个**合约 —— " +
			"多个合约会让「加了哪一手」与「均价怎么变」对不上号")
	}
	sym := r.Symbols[0]
	cli := r.cli
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}

	r.Logf("")
	r.Logf("== 加权均价判别：%s ==", sym)

	before, err := avgSnapshot(cli, sym)
	if err != nil {
		return err
	}
	r.Logf("  加仓前：多头 %.0f 手，开仓均价 %v，持仓均价 %v",
		before.volume, fmtMaybe(before.openPrice, before.hasOpen),
		fmtMaybe(before.posPrice, before.hasPos))
	if before.volume < 1 {
		return fmt.Errorf("%s 多头无持仓 —— 本实验要在**已有持仓**上加一手，"+
			"从零开始加第一手得不到加权平均", sym)
	}
	if !before.hasOpen {
		return fmt.Errorf("%s 有持仓却没有开仓均价 —— 截面自相矛盾，先查清楚再做实验", sym)
	}

	// ⚠️ 先记下逐笔成交里这个合约的开仓价集合。
	// 判据要求「新的一手与已有的价**不同**」，而已有的价必须从**成交**读，
	// 不能从均价读 —— 均价是有损压缩，正是本实验要证明的那件事。
	priorPrices := openPricesOf(cli, sym)
	r.Logf("  已有开仓成交价：%v（来自成交截面，不是均价）", priorPrices)

	st, err := r.openOneLot(sym, kq.Buy)
	if err != nil {
		return fmt.Errorf("加仓失败：%w", err)
	}
	r.Logf("  加了 1 手，委托 %s 已全成", st.OrderID)

	// 等持仓截面把这一手算进去。
	deadline := time.Now().Add(20 * time.Second)
	var after avgState
	for time.Now().Before(deadline) {
		after, err = avgSnapshot(cli, sym)
		if err == nil && after.volume == before.volume+1 {
			break
		}
		cli.WaitTrade(500 * time.Millisecond)
	}
	if after.volume != before.volume+1 {
		return fmt.Errorf("等了 20 秒，持仓仍是 %.0f 手（加仓前 %.0f）—— "+
			"⚠️ 截面没跟上，此时读到的均价是半截的，**不据此下结论**",
			after.volume, before.volume)
	}

	// ⚠️ 成交价必须从**成交**截面按 order_id 取，不能拿委托上的价：
	// openOneLot 下的是涨跌停价的限价单，委托价是 3xxx 的涨停价，
	// 而成交价是对手价 —— 两者差得很远，混用会让整条判据算在一个错的数上。
	newPrice, ok := fillPriceOf(cli, st.OrderID)
	if !ok {
		return fmt.Errorf("委托 %s 已全成，却在成交截面里找不到它的成交价 —— "+
			"两个截面不一致，此时任何均价判据都不可信", st.OrderID)
	}
	r.Logf("")
	r.Logf("  新成交价     %.4f", newPrice)
	r.Logf("  加仓后均价   开仓 %.4f  持仓 %.4f", after.openPrice, after.posPrice)

	// —— 判据 ——
	//
	// ⚠️ 判别力的条件只用**输出**表述，不去反推「当前持有的是哪几手」：
	// 从成交列表反推持有哪几手，依赖平仓的消耗顺序，而那正是实验 4 还没答的问题
	// —— 拿一个未知去支撑另一个实验的判据，是把待测项当成已知。
	//
	// 真正要的性质是：**结果均价落在任何单笔成交价都取不到的位置上**。
	// 那时柜台无论取首笔、末笔还是最新价，都给不出这个数。
	reachable := append(append([]float64{}, priorPrices...), newPrice)
	discriminating := true
	for _, p := range reachable {
		if after.openPrice == p {
			discriminating = false
			break
		}
	}
	if !discriminating {
		r.Logf("")
		r.Logf("  ⚠️ **本次没有判别力**：均价 %.4f 恰好等于某个单笔成交价。", after.openPrice)
		r.Logf("     加不加权都能给出这个数 —— 这不是失败，是样本不合用。")
		r.Logf("     行情动一动再跑一次；夹具照落，它记录的是「这一次没分开」。")
	} else {
		want := (before.openPrice*before.volume + newPrice) / after.volume
		diff := after.openPrice - want
		if diff < 0 {
			diff = -diff
		}
		r.Logf("")
		r.Logf("  加权平均预期 %.6f，柜台给 %.6f，差 %.6f", want, after.openPrice, diff)
		if diff > 1e-6 {
			r.Logf("  ⚠️ **对不上**。柜台的开仓均价不是简单加权平均，这条要单独查。")
		} else {
			r.Logf("  ✓ 柜台确实做加权平均 —— 而这个数**任何单笔成交价都取不到**")
			r.Logf("    （已排除 %v），所以它不可能是「取首笔/末笔/最新价」碰巧撞上的。", reachable)
		}
		if after.openPrice == after.posPrice {
			r.Logf("")
			r.Logf("  ⓘ 开仓均价 == 持仓均价（%.6f）—— 今仓下二者基线相同，本就该相等。", after.openPrice)
			r.Logf("    ⚠️ 所以**今仓样本对「两条基线」这条区分没有判别力**；")
			r.Logf("    要等今晚结算把它们分开：open_price 不动，position_price ← 昨结算价。")
		}
	}

	return r.dump("avg-price", fmt.Sprintf(
		"加权均价判别：%s 在已有 %.0f 手基础上加 1 手 @%.4f，"+
			"已有开仓成交价 %v。⚠️ 本份的用处在今晚结算之后："+
			"open_price 与 position_price 将第一次取到不同的数",
		sym, before.volume, newPrice, priorPrices))
}

// avgState 是判据需要的那几个数。
type avgState struct {
	volume          float64
	openPrice       float64
	posPrice        float64
	hasOpen, hasPos bool
}

func avgSnapshot(cli *kq.Client, sym string) (avgState, error) {
	p := cli.PositionOf(sym)
	if p == nil {
		return avgState{}, fmt.Errorf("读不到 %s 的持仓截面", sym)
	}
	s := avgState{volume: kq.MustNum(p, "volume_long")}
	// ⚠️ 空仓方向柜台给的是字符串 "-"，不是 0（实测 188/188，probes.md §9）。
	// 用 Num 而不是 MustNum：读不到数就说读不到，不要把 "-" 变成 0。
	s.openPrice, s.hasOpen = kq.Num(p, "open_price_long")
	s.posPrice, s.hasPos = kq.Num(p, "position_price_long")
	return s, nil
}

// openPricesOf 从**成交截面**读该合约已有的开仓价，去重。
//
// ⚠️ 必须从成交读而不是从均价读：均价是有损压缩，
// 而「均价丢了什么」正是本实验要证明的东西 —— 拿它当输入就是循环论证。
func openPricesOf(cli *kq.Client, sym string) []float64 {
	_, inst := splitSymbol(sym)
	seen := map[float64]bool{}
	var out []float64
	for _, raw := range cli.Trades() {
		t, ok := raw.(map[string]any)
		if !ok || t["instrument_id"] != inst {
			continue
		}
		if off, _ := t["offset"].(string); off != "OPEN" {
			continue
		}
		p, ok := kq.Num(t, "price")
		if !ok || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// fillPriceOf 按 order_id 取这一笔的成交价。
//
// ⚠️ 一笔委托可能拆成多笔成交。本实验只下 1 手，所以至多一笔；
// 真出现多笔时**报错而不是取第一笔** —— 取第一笔会给出一个看起来正常的错值。
func fillPriceOf(cli *kq.Client, orderID string) (float64, bool) {
	var got float64
	n := 0
	for _, raw := range cli.Trades() {
		t, ok := raw.(map[string]any)
		if !ok || t["order_id"] != orderID {
			continue
		}
		p, ok := kq.Num(t, "price")
		if !ok {
			continue
		}
		got = p
		n++
	}
	if n != 1 {
		return 0, false
	}
	return got, true
}

func fmtMaybe(v float64, ok bool) string {
	if !ok {
		return `"-"（柜台声明无值）`
	}
	return fmt.Sprintf("%.4f", v)
}
