package probe

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expFeeCloseHistory 量**平昨**这一档手续费 —— 至今零观测的那一档。
//
// # ⚠️ 为什么它单独成一个实验
//
// CTP 的费率表有六个字段，开仓 / 平仓 / 平今各两档（按手、按额）。
// 本口子上已量到：开仓档与**平今**档同额（kq_facts 18）。
// 而「平今 = 开仓」**不能外推到平昨** —— 真实交易所里平昨往往是单独一档。
//
// ⚠️ 更要紧的是记住零观测的形态：至今**一笔平昨都没有**（kq_facts 19）。
// 一个从没发生过的动作，它的费率在对拍时两边都用「开仓档」算，于是恒等 ——
// 而那个恒等什么都不说明。
//
// # 判据
//
// 平昨一手，读**那笔成交记录自带的** commission：
//
//	等于开仓档   平昨不单独收 —— 这一档在本口子上测不出来（与平今同命）
//	不等         平昨是**单独一档**，值就是这个数
//
// ⚠️ 用对手价成交，这是一笔**真实平仓**，会吃掉一手昨仓。
// 昨仓只能等结算，吃掉就没了 —— 所以前提要求**至少 2 手**。
func (r *Runner) expFeeCloseHistory(ctx context.Context) error {
	if len(r.Symbols) != 1 {
		return fmt.Errorf("本实验需要 -symbols 指定**恰好一个**合约，得到 %d 个",
			len(r.Symbols))
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
		return fmt.Errorf("%s 行情未就绪，定不出对手价", sym)
	}

	p := cli.PositionOf(sym)
	dir, side, err := historyLegWithSpare(p)
	if err != nil {
		return err
	}
	ex, inst := splitSymbol(sym)

	r.Logf("")
	r.Logf("== 平昨手续费：至今零观测的那一档 ==")
	r.Logf("  ⚠️ 这是一笔**真实平仓**，会吃掉一手昨仓；昨仓只能等结算，吃掉就没了。")
	r.Logf("  %s 的 %s 方向昨仓 %.0f 手，平 1 手还剩 %.0f 手",
		sym, side, kq.MustNum(p, "volume_"+side+"_his"),
		kq.MustNum(p, "volume_"+side+"_his")-1)

	// ⚠️ 从**成交记录**读手续费，不从行情的费率字段推。
	// 免费行情网关不下发静态合约字段（commission / volume_multiple 全为 null，
	// 见 Quote.Valid 的注释），照它算出来的「开仓档」会是 0 ——
	// 而 0 正好等于「平昨免收」这个结论。成交记录里的 commission 是直接观测。
	seen := map[string]bool{}
	for id := range cli.Trades() {
		seen[id] = true
	}
	openFee, openN := openFeeOf(cli.Trades(), inst)
	if openN == 0 {
		return fmt.Errorf("⚠️ 本交易日 %s 上没有**费额一致的开仓**成交 —— "+
			"没有开仓档可比，平昨额单独一个数说明不了它是不是单独一档", inst)
	}
	r.Logf("  本交易日 %s 的开仓成交 %d 笔，每手手续费 %.6f", inst, openN, openFee)

	orderDir := kq.Buy
	if dir == kq.Buy {
		orderDir = kq.Sell
	}
	id, err := cli.InsertOrder(r.guard(), kq.OrderReq{
		Exchange: ex, Instrument: inst,
		Direction: orderDir, Offset: kq.Close, Volume: 1,
		LimitPrice: q.AggressivePrice(orderDir),
	})
	if err != nil {
		return fmt.Errorf("平昨下单失败：%w", err)
	}
	st, done := cli.WaitOrderFinished(id, 30*time.Second)
	if !done {
		return fmt.Errorf("⚠️ 委托 %s 30 秒内未走到终态（status=%q）—— "+
			"**它可能还挂着**，去看一眼；本次没有结论", id, st.Status)
	}
	if st.VolumeLeft != 0 {
		return fmt.Errorf("⚠️ 委托终态但未全成（volume_left=%d last_msg=%q）—— "+
			"没成交就没有手续费，本次没有结论", st.VolumeLeft, st.LastMsg)
	}

	// ⚠️ 等**那一笔新成交**出现，而不是睡够就往下走：
	// 没等到就读会得到「没有新成交」，而它与「平昨免收」在结论上完全不同。
	var fresh map[string]any
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) && fresh == nil {
		for tid, v := range cli.Trades() {
			if seen[tid] {
				continue
			}
			m, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if s, _ := m["instrument_id"].(string); s == inst {
				fresh = m
				break
			}
		}
		if fresh == nil {
			cli.WaitTrade(500 * time.Millisecond)
		}
	}
	if fresh == nil {
		// ⚠️ 不给结论：「没推过来」与「免收」在这里分不开。
		r.Logf("  ⚠️ 委托已全成，但 20 秒内**没等到那笔成交记录**。")
		r.Logf("     「截面没推过来」与「平昨免收」在这里分不开 —— 本次没有结论。")
		return r.dump("fee-close-history", fmt.Sprintf(
			"⚠️ %s 平昨 1 手已全成，但没等到成交记录 —— 本次**没有结论**", sym))
	}

	fee, hasFee := kq.Num(fresh, "commission")
	gotOffset, _ := fresh["offset"].(string)
	price, _ := kq.Num(fresh, "price")
	if !hasFee {
		return fmt.Errorf("⚠️ 那笔成交记录里**没有 commission 字段** —— "+
			"读不到不当成零：0 正好等于「平昨免收」这个结论。记录：%v", fresh)
	}
	r.Logf("")
	r.Logf("  平昨成交：offset=%s 成交价=%.4f **手续费=%.6f**", gotOffset, price, fee)

	// ⚠️ 柜台把这笔记成什么开平，照**它说的**读，不照我们发的读。
	if gotOffset != string(kq.Close) {
		r.Logf("  ⚠️ 发出去的是 %s，柜台记成 **%s** —— "+
			"那么这笔量到的**不是平昨档**，本次对平昨没有结论", kq.Close, gotOffset)
		return r.dump("fee-close-history", fmt.Sprintf(
			"⚠️ %s 发 CLOSE，柜台记成 %s —— 本次对平昨档**没有结论**", sym, gotOffset))
	}

	if math.Abs(fee-openFee) < 1e-9 {
		r.Logf("  → 平昨额 == 开仓额 %.6f：**平昨不单独收**（与平今同命，kq_facts 18）——",
			openFee)
		r.Logf("     这一档在本口子上**测不出来**，不是「量到了等于开仓」")
	} else {
		r.Logf("  → 平昨额 %.6f ≠ 开仓额 %.6f，差 %.6f —— **平昨是单独一档**",
			fee, openFee, fee-openFee)
		r.Logf("     比值 %.10g", fee/openFee)
	}

	return r.dump("fee-close-history", fmt.Sprintf(
		"平昨手续费**首次观测**（kq_facts 19：此前一笔平昨都没有）："+
			"%s 的 %s 方向平昨 1 手，柜台记 offset=%s，手续费 %.6f；"+
			"同合约同交易日的开仓档 %.6f（%d 笔）",
		sym, side, gotOffset, fee, openFee, openN))
}

// openFeeOf 取某合约本交易日**开仓**成交的每手手续费。
//
// ⚠️ 多笔时要求它们**彼此相等**，不相等就报 0 笔（调用方会因此拒绝出结论）。
// 取平均会把两档不同的费率抹成一个数，而那个数哪一档都不是。
func openFeeOf(trades map[string]any, inst string) (fee float64, n int) {
	for _, v := range trades {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if s, _ := m["instrument_id"].(string); s != inst {
			continue
		}
		if s, _ := m["offset"].(string); s != string(kq.Open) {
			continue
		}
		c, ok := kq.Num(m, "commission")
		if !ok {
			continue
		}
		vol, _ := kq.Num(m, "volume")
		if vol <= 0 {
			continue
		}
		per := c / vol
		if n > 0 && math.Abs(per-fee) > 1e-9 {
			return 0, 0
		}
		fee, n = per, n+1
	}
	return fee, n
}

// historyLegWithSpare 找一个**昨仓至少 2 手**的方向。
//
// ⚠️ 要求 2 手而不是 1 手：平完还得剩下。昨仓只能等结算，
// 吃光了别的需要昨仓的实验今晚就全做不成了。
func historyLegWithSpare(p map[string]any) (kq.Direction, string, error) {
	if p == nil {
		return kq.Buy, "", fmt.Errorf("读不到持仓截面")
	}
	for _, c := range []struct {
		dir  kq.Direction
		side string
	}{{kq.Buy, "long"}, {kq.Sell, "short"}} {
		his, ok := kq.Num(p, "volume_"+c.side+"_his")
		if !ok {
			continue // ⚠️ 读不到不当成零
		}
		if his >= 2 {
			return c.dir, c.side, nil
		}
	}
	return kq.Buy, "", fmt.Errorf(
		"⚠️ 没有哪个方向的昨仓**至少 2 手** —— 本实验会吃掉一手，" +
			"只剩 1 手时平完就没有昨仓了，今晚别的需要昨仓的实验会全部落空。" +
			"昨仓只能等结算，造不出来")
}
