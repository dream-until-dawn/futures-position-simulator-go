package main

import (
	"errors"
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// needsCancel 判一笔要求「立刻成交」的委托发出之后要不要撤：报错（含超时拿不到结论）或成交手数少于下单手数 ⇒ 要撤。
// （多手单部分成交时，余量还挂在队列里 —— 与一手没成交同一种残留。）
//
// ⚠️ ctp.Client.Insert 在「未成交还在队列」（OST_NoTradeQueueing）时也返回 —— 那张 GFD 单还挂在柜台上。
// 不撤就返回，工具退出后它随时可能成交，账上多一手没人照看的仓（评审 20260922）。
func needsCancel(st ctp.OrderState, want int, err error) bool {
	return err != nil || st.VolumeTraded < want
}

// afterCancel 判撤单、扫单之后，本地簿里这一笔的状态说明了什么。
//
//	clean = true   已终态且一手没成交（已撤 / 未成交不在队列）⇒ 干净，照原错误报
//	clean = false  why 说明：超时之后其实成交了（账上有仓）/ 还停在过渡态或本地簿里没有（没有结论）/ 还挂着
//
// ⚠️ LiveOrders 只收「还在队列」的委托：停在 'a'（已提交）上的那一笔在两次扫单里都是 0，会被误判为干净 ——
// 所以扫完还要看本地簿（评审 20260922）。
func afterCancel(st ctp.OrderState, known bool) (clean bool, why string) {
	if !known {
		return false, "本地簿里没有这一笔的任何回报 —— **没有结论**，它可能还在路上，去看账户"
	}
	if st.VolumeTraded > 0 {
		return false, fmt.Sprintf("撤单之前已经成交了 %d 手 —— **账上有这笔仓**，去看账户", st.VolumeTraded)
	}
	switch st.Status {
	case def.THOST_FTDC_OST_Canceled, def.THOST_FTDC_OST_NoTradeNotQueueing:
		return true, ""
	case def.THOST_FTDC_OST_NoTradeQueueing, def.THOST_FTDC_OST_PartTradedQueueing:
		return false, "扫单之后本地簿里它**还挂着** —— 去看账户"
	}
	return false, fmt.Sprintf("本地簿里它还停在过渡态 %q —— **没有结论**，去看账户", string(st.Status))
}

// liveOn 从一次委托查询里挑出某合约还活着的委托。
func liveOn(orders []*def.CThostFtdcOrderField, inst string) []*def.CThostFtdcOrderField {
	var out []*def.CThostFtdcOrderField
	for _, o := range orders {
		if o != nil && ctp.Text(o.InstrumentID[:]) == inst {
			out = append(out, o)
		}
	}
	return out
}

// sweepLive 撤掉某合约的全部活委托并核对为 0；撤不掉就报错（调用方要大声报「去看账户」）。
//
// 按交易所编号撤（CancelByOrder），与会话无关 —— 超时拿不到 ref 的那一笔也撤得到。
// ⚠️ LiveOrders 只收「还在队列」的委托：刚发出、柜台还没接受（状态 'a'）的那一笔一时查不到 ⇒ 要**连续两次**（隔 1 秒）都是 0 才算干净。
func sweepLive(c *ctp.Client, inst string, timeout time.Duration, logf func(string, ...any)) error {
	empty := 0
	for i := 0; i < 6; i++ {
		orders, err := c.LiveOrders(timeout)
		if err != nil {
			return fmt.Errorf("⚠️ 查不到活委托（没有结论，不是没有挂单）：%w", err)
		}
		live := liveOn(orders, inst)
		if len(live) == 0 {
			empty++
			if empty >= 2 {
				return nil
			}
			time.Sleep(time.Second)
			continue
		}
		empty = 0
		for _, o := range live {
			logf("[cancel] %s 还有活委托（OrderSysID=%s），撤", inst, ctp.Text(o.OrderSysID[:]))
			if err := c.CancelByOrder(o); err != nil {
				logf("[cancel] ⚠️ %v", err)
			}
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("⚠️⚠️ %s 撤了 6 轮还没连续两次查到 0 个活委托 —— **去看账户**", inst)
}

// insertOrCancel 发一笔要求立刻成交的委托；没成交（或报错 / 超时）就撤、确认撤干净（cancelAndConfirm）再返回错误。
func insertOrCancel(c *ctp.Client, req ctp.OrderReq, timeout time.Duration, logf func(string, ...any)) (ctp.OrderState, error) {
	st, err := c.Insert(req, timeout)
	if !needsCancel(st, req.Volume, err) {
		return st, nil
	}
	failed := fmt.Errorf("没成交：status=%q %s err=%v", string(st.Status), st.StatusMsg, err)
	if cerr := cancelAndConfirm(c, st.OrderRef, req, timeout, logf); cerr != nil {
		return st, errors.Join(failed, cerr)
	}
	return st, failed
}

// cancelAndConfirm 撤一笔委托并确认撤干净：按 ref 撤（有 ref 时）→ 扫本合约活委托 → 看本地簿落到终态（afterCancel）。
// 返回 nil = 干净、一手没成交；否则说明为什么不干净（撤不掉 / 撤之前已成交 / 没有结论）。
//
// ⚠️ 要求立刻成交的路径（insertOrCancel）与有意挂单、自己负责撤的路径（openOneLotResting 等）共用这一段（评审 20260922）。
func cancelAndConfirm(c *ctp.Client, ref string, req ctp.OrderReq, timeout time.Duration, logf func(string, ...any)) error {
	if ref != "" {
		if cerr := c.Cancel(ref, req); cerr != nil {
			logf("[cancel] ⚠️ 按 ref 撤单失败：%v", cerr)
		}
	}
	if serr := sweepLive(c, req.Instrument, timeout, logf); serr != nil {
		return serr
	}
	if ref != "" {
		// 撤单回报要一点时间进本地簿：最多等 3 秒看它落到终态。
		var clean bool
		var why string
		for i := 0; i < 6; i++ {
			cur, known := c.Order(ref)
			if clean, why = afterCancel(cur, known); clean || (known && cur.VolumeTraded > 0) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !clean {
			return fmt.Errorf("⚠️⚠️ %s ref=%s：%s", req.Instrument, ref, why)
		}
	}
	logf("[cancel] %s 那笔已撤、本合约没有活委托", req.Instrument)
	return nil
}
