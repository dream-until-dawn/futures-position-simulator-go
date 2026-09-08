package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expFrozen 量**挂单冻结**：一笔挂得住、不成交的委托冻结了什么，以及它怎么进 Available。
//
// ⚠️ 这条实验是被前一条实测**逼出来的**。
// 量 Available 时四个冻结项全是 0，于是 `available = balance - margin` 精确成立——
// 而那只验证了公式的一半：
//
//	Available = Balance - Margin - FrozenMargin - FrozenCommission - FrozenCash
//
// 后三项一次也没参与过运算。**一个恒为 0 的减项不会暴露自己被漏掉**，
// 漏了它的实现在空挂单的账户上与正确实现完全同值。
// 而这正是「回测以为自己比真实账户有钱」的来源：挂了一堆单还觉得能开仓。
//
// 做法：在远离盘口的价位挂一手（买挂跌停、卖挂涨停），让它挂住不成交，
// 前后各取一次账户截面，然后撤单、再取一次。
//
// 顺带还能分开一件事：**冻结保证金按昨结算价算，还是按委托价算。**
// 买单挂在跌停上时两者差得很开，够分。
func (r *Runner) expFrozen(ctx context.Context) error {
	if len(r.Symbols) != 1 {
		return fmt.Errorf("挂单冻结实验需要**恰好一个**合约，得到 %d 个", len(r.Symbols))
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
		return fmt.Errorf("%s 上已有 %.0f 手持仓，冻结量会与持仓保证金混在一起，本实验作废", sym, v)
	}

	r.Logf("")
	r.Logf("== 挂单冻结：冻结了什么，怎么进 Available ==")

	snap := func(tag string) map[string]float64 {
		a := cli.Account()
		m := map[string]float64{}
		for _, k := range []string{"balance", "available", "margin", "frozen_margin",
			"frozen_commission", "frozen_premium", "commission", "position_profit"} {
			m[k] = kq.MustNum(a, k)
		}
		r.Logf("  [%s] balance=%.4f available=%.4f margin=%.2f frozen_margin=%.4f frozen_commission=%.4f",
			tag, m["balance"], m["available"], m["margin"], m["frozen_margin"], m["frozen_commission"])
		return m
	}

	cli.WaitTrade(1500 * time.Millisecond)
	before := snap("挂单前")

	q, ok := cli.WaitQuoteReady(sym, 30*time.Second)
	if !ok {
		return fmt.Errorf("%s 行情未就绪，取不到远价", sym)
	}
	far := q.FarPrice(kq.Buy)
	ex, inst := splitSymbol(sym)
	id, err := cli.InsertOrder(r.guard(), kq.OrderReq{
		Exchange: ex, Instrument: inst, Direction: kq.Buy, Offset: kq.Open,
		Volume: 1, LimitPrice: far,
	})
	if err != nil {
		return err
	}
	r.Logf("  挂单 %s 买开 1 手 @%.2f（跌停，挂得住）", sym, far)

	// ⚠️ 必须确认它**挂住了且没成交**。成交了的话后面量的是持仓保证金，
	// 那是另一个问题，而两者数量级相近，混起来看不出来。
	alive := cli.WaitUntil(20*time.Second, func() bool {
		st, ok := cli.OrderOf(id)
		return ok && st.Status == "ALIVE" && st.VolumeLeft == 1
	})
	if !alive {
		st, _ := cli.OrderOf(id)
		_ = cli.CancelOrder(id)
		return fmt.Errorf("⚠️ 委托没有停在「挂住且未成交」的状态（status=%q volume_left=%d msg=%q），"+
			"本次结论作废；已发撤单", st.Status, st.VolumeLeft, st.LastMsg)
	}
	if v := kq.MustNum(cli.PositionOf(sym), "volume_long"); v > 0 {
		_ = cli.CancelOrder(id)
		return fmt.Errorf("⚠️ 挂在跌停上的买单竟然成交了（持仓 %.0f 手）——"+
			"量到的将是持仓保证金而非冻结，本次结论作废", v)
	}

	cli.WaitTrade(3 * time.Second)
	during := snap("挂单中")

	if err := r.dump("exp-frozen", "挂单冻结：一笔挂住不成交的委托冻结了什么"); err != nil {
		return err
	}

	// 撤单放在判定**之前**：判定里任何一条 return 都不该把单留在账上。
	cancelErr := cli.CancelOrder(id)
	gone := cli.WaitUntil(20*time.Second, func() bool {
		st, ok := cli.OrderOf(id)
		return ok && st.Status != "ALIVE"
	})
	cli.WaitTrade(2 * time.Second)
	after := snap("撤单后")
	if cancelErr != nil || !gone {
		r.Logf("  ⚠️ **撤单未确认**（err=%v，终态=%v）——账户上可能还挂着一张单，请手工处理",
			cancelErr, gone)
	}

	dAvail := during["available"] - before["available"]
	dBal := during["balance"] - before["balance"]
	fm, fc := during["frozen_margin"], during["frozen_commission"]

	r.Logf("")
	r.Logf("  挂单造成的变化：available %+.4f   balance %+.4f", dAvail, dBal)
	r.Logf("  冻结项：frozen_margin=%.4f  frozen_commission=%.4f", fm, fc)
	r.Logf("")

	// —— 样本守卫 ——
	if fm == 0 && fc == 0 {
		// ⚠️ 阴性结果不能当阳性用。
		r.Logf("  ⚠️ **两个冻结项都是 0**：这个柜台对挂单不冻结，或没实现冻结。")
		r.Logf("     两种情形在数据上一样，这里分不开。")
		r.Logf("     ⚠️ 更要紧的是：**「冻结项要不要从 Available 里扣」仍然没测到**——")
		r.Logf("        减数为 0 时，扣与不扣同值。判据不成立。")
		return nil
	}

	// —— 判据一：冻结项进不进 Available ——
	want := -(fm + fc)
	if abs(dAvail-want) < 0.01 {
		r.Logf("  【判据一】available 减少了 %.4f，恰好等于两个冻结项之和 → ", -dAvail)
		r.Logf("            **冻结项确实从 Available 里扣**，本库的公式这一半成立。")
	} else {
		r.Logf("  ⚠️ 【判据一】available 变化 %+.4f，与冻结项之和 %.4f **对不上**。", dAvail, -want)
		r.Logf("     说明 Available 里还有本实验没考虑的成分。**本项不收敛。**")
	}

	// —— 判据二：balance 应当不变（冻结不是花钱）——
	r.Logf("")
	if abs(dBal) < 0.01 {
		r.Logf("  【判据二】balance 不变（%+.6f）→ 冻结**不进结存**，只占用可用。", dBal)
	} else {
		r.Logf("  ⚠️ 【判据二】balance 变了 %+.6f —— 与「冻结只占用不扣钱」矛盾，需人工看。", dBal)
	}

	// —— 判据四：撤单后冻结要归还 ——
	//
	// ⚠️ 这一条不是走过场。冻结只增不减的实现，在**单笔**样本上与正确实现同值：
	// 挂单时两者都扣，而「撤单后还回来」要下一次观测才看得出。
	// 一个只挂不撤的实验，测不出这一半。
	r.Logf("")
	// ⚠️ 判据用 available - balance，**不用 available 本身**。
	//
	// 账上别的持仓在这几秒里会浮动，balance 与 available 一起跟着动。
	// 拿 available 的绝对值比，会把「别的仓浮亏了 20 块」读成「有 20 块没还回来」——
	// 首跑就是这样：available 差 -20，而同期 balance 也正好差 -20。
	// 两者之差把浮盈亏消掉，剩下的才是占用。
	gapBefore := before["available"] - before["balance"]
	gapAfter := after["available"] - after["balance"]
	dGap := gapAfter - gapBefore
	switch {
	case after["frozen_margin"] != 0 || after["frozen_commission"] != 0:
		r.Logf("  ⚠️ 【判据四】撤单后冻结项**没有归零**（margin=%.4f commission=%.4f）——",
			after["frozen_margin"], after["frozen_commission"])
		r.Logf("     要么撤单没真撤掉，要么冻结不随撤单释放。两种都得人工看。")
	case abs(dGap) < 0.01:
		r.Logf("  【判据四】撤单后 (available - balance) 回到挂单前：%.4f → %.4f（差 %+.6f）",
			gapBefore, gapAfter, dGap)
		r.Logf("            → 冻结随撤单**完整释放**。")
		r.Logf("            （同期 balance 变了 %+.4f，是别的持仓在浮动；这个判据不受它影响）",
			after["balance"]-before["balance"])
	default:
		r.Logf("  ⚠️ 【判据四】冻结项已归零，但 (available - balance) 变了 %+.6f", dGap)
		r.Logf("     %.4f → %.4f。浮盈亏已被消掉，所以这是真的有东西没还回来。", gapBefore, gapAfter)
	}

	// —— 判据三：冻结保证金按哪个价算 ——
	r.Logf("")
	if fm > 0 {
		mult := 0.0
		if q.VolumeMultiple > 0 {
			mult = q.VolumeMultiple
		}
		r.Logf("  【判据三】冻结保证金 %.4f。委托价 %.2f，昨结算价 %.2f。", fm, far, q.PreSettlement)
		if mult <= 0 {
			// ⚠️ 免费行情网关不发静态字段，乘数取不到。说清楚，不要用一个猜来的数往下算。
			r.Logf("     ⚠️ 行情网关没给乘数（VolumeMultiple=0），**无法反解费率**。")
			r.Logf("        判别办法：拿同合约建一手仓量到的每手保证金来比——")
			r.Logf("        相等 → 冻结按昨结算价算；按比例小 → 按委托价算。")
		} else {
			r.Logf("     按委托价反解费率 = %.8f", fm/(far*mult))
			r.Logf("     按昨结算价反解费率 = %.8f", fm/(q.PreSettlement*mult))
			r.Logf("     ⚠️ 哪个是「干净」的数要对照持仓保证金的费率看，本实验不单独下结论。")
		}
	}
	return nil
}
