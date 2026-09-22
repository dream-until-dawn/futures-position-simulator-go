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

// insertOrCancel 发一笔要求立刻成交的委托；没成交（或报错 / 超时）就先按 ref 撤，再扫一遍本合约的活委托，确认撤干净再返回错误。
func insertOrCancel(c *ctp.Client, req ctp.OrderReq, timeout time.Duration, logf func(string, ...any)) (ctp.OrderState, error) {
	st, err := c.Insert(req, timeout)
	if !needsCancel(st, req.Volume, err) {
		return st, nil
	}
	failed := fmt.Errorf("没成交：status=%q %s err=%v", string(st.Status), st.StatusMsg, err)
	if st.OrderRef != "" {
		if cerr := c.Cancel(st.OrderRef, req); cerr != nil {
			logf("[cancel] ⚠️ 按 ref 撤单失败：%v", cerr)
		}
	}
	if serr := sweepLive(c, req.Instrument, timeout, logf); serr != nil {
		return st, errors.Join(failed, serr)
	}
	logf("[cancel] %s 没成交的那笔已撤、本合约没有活委托", req.Instrument)
	return st, failed
}
