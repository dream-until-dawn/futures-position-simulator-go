//go:build windows

package ctp

import (
	"fmt"
	"sync/atomic"
	"time"
	"unsafe"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// ⚠️ **本文件是全包唯一能发出委托的地方，而它先过安全阀。**
//
// # 为什么是「结构上不可绕过」，不是「记得调用 Check」
//
// 这个模块栽过一模一样的跟头：`probe.decideFallback` 抽成纯函数、
// 配了 7 条用例 2 条穷举不变式 3 次破坏验证 —— **然后忘了接线**。
// ⚠️ **那 7 条用例当时全绿**，而全绿恰恰是问题的一部分：
// `unused` 不报（测试用了它）、编译不报、覆盖率还好看。
// 是评审把内联守卫换成 `if false`、全库仍然全绿才逼出来的。
//
//	验证   防的是「**这一次**忘了接线」  —— 要有人想到去写那条断言
//	结构   防的是「**以后任何一次**忘」  —— 不给第二条到达路径
//
// 所以这里的做法是：`ReqOrderInsert` 这个 proc 名在全包**只出现一次**，
// 就在 `send` 里，且它的第一件事是过阀。守卫 `TestSendIsTheOnlyPathToInsert`
// 读本包源码核这两件事 —— ⚠️ 它查的是**结构**，不是行为，所以它在
// 「有人新写了第二条路径」的那一刻就红，而不必等那条路径被用到。

// send 是**全包唯一**到达柜台报单接口的路径。
//
// ⚠️ 阀检查是它的第一件事，且**没有参数可以关掉它** ——
// 不提供 `sendUnchecked`、不提供 `force` 布尔。
// 一个「紧急情况下绕过」的开关，会在紧急情况下**正好**被用到，
// 而紧急情况正是判断力最差的时候。
func (c *Client) send(r OrderReq) (string, error) {
	if err := c.Check(r); err != nil {
		return "", err
	}
	// ⚠️ **OrderRef 必须是纯数字，并且从登录应答的 `MaxOrderRef` 往后续。**
	//
	// 这一行改过两次，前两版都是**照着一个错的理由**改的：
	//
	//	v1  UnixNano()%1e9        跨 1e9 回绕 ⇒ 变小
	//	v2  p%09d 会话内计数器     ⇒ 单调了，**可第二笔照样被拒**
	//	v3  %d 且从 MaxOrderRef 续 ⇒ 见下
	//
	// ⚠️ v2 的理由是「ref 必须单调递增」。它**看起来**被证实了（那次 ref 确实变小、
	// 确实被拒），于是我停在这里 —— 而真相是 `p` 前缀让柜台根本读不出数字，
	// 于是同一会话里**每一笔都被当成同一个引用**，第二笔起一律
	// `ErrorID=22 不允许重复报单`。20260910 夜盘 `ctp-dup` 五格全新递增 ref、
	// 五个不同价、中途不撤，第 2–5 格照样全拒 —— 那才把 v2 的理由否掉。
	//
	// ⚠️ 教训不是「要单调」，是：**一个只在第二笔单上暴露的错，
	// 用只发一笔单的实验永远看不见。**
	ref := formatOrderRef(atomic.AddInt64(&c.orderSeq, 1))
	f := def.CThostFtdcInputOrderField{
		OrderPriceType:      def.THOST_FTDC_OPT_LimitPrice,
		Direction:           r.Direction,
		LimitPrice:          def.TThostFtdcPriceType(r.LimitPrice),
		VolumeTotalOriginal: def.TThostFtdcVolumeType(r.Volume),
		TimeCondition:       def.THOST_FTDC_TC_GFD,
		VolumeCondition:     def.THOST_FTDC_VC_AV,
		ContingentCondition: def.THOST_FTDC_CC_Immediately,
		ForceCloseReason:    def.THOST_FTDC_FCC_NotForceClose,
	}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	copy(f.UserID[:], c.cred.UserID)
	copy(f.OrderRef[:], ref)
	copy(f.ExchangeID[:], r.Exchange)
	copy(f.InstrumentID[:], r.Instrument)
	f.CombOffsetFlag[0] = byte(r.Offset)
	// ⚠️ 本库仅投机（fidelity.md 覆盖范围）。写死而不是留参数：
	// 留参数意味着有人可以传别的，而别的取值本库一行都没建模。
	f.CombHedgeFlag[0] = byte(def.THOST_FTDC_HF_Speculation)

	// ⚠️ **发单前必须把这个 ref 的旧状态清掉。**
	// 20260910 `ctp-dup` 第 4 格撞出来的：那一格故意重用了第 1 格的 ref，
	// 而第 1 格已经撤单落在终态 "5" 上 —— 于是 `Insert` **秒回「已撤单」**，
	// 报告了一个**根本没发生过**的结果，那一格白测了。
	//
	// ⚠️ 它比看上去严重：`Insert` 的等待条件是「簿上出现终态」，
	// 而一个残留的终态让这个条件**在发单的那一刻就已经成立**。
	// 「上一笔的结局」被当成了「这一笔的结局」。
	//
	// ⚠️ 清了之后仍有一层残余歧义：旧单的迟到回报会落到新单的格子上。
	// 这**没法在按 ref 索引的簿里根治**。⚠️ 于是本包**不提供**任何拨动序号的口子：
	// 20260910 曾为判别实验加过一个 `SeedOrderSeq`，评审指出它与
	// `TestNoBypassKnob` 禁掉的那种开关**是同一个形状** —— 一个绕过安全阀、
	// 一个绕过 ref 唯一性，而后者只靠「正常路径不重用」这条纪律守着。
	// ⚠️ 已删除。守卫 `TestNoOrderRefSeedKnob`。
	c.book.reset(ref, r.Volume)
	c.logf("[ctp] 报单 %s  ref=%s", r, ref)
	c.req("ReqOrderInsert", unsafe.Pointer(&f))
	return ref, nil
}

// Insert 发一笔限价委托并等它到达一个**可判断**的状态。
//
// ⚠️ 返回的是**状态**不是「成功」：一笔挂着的单与一笔成交的单都不是错误，
// 而调用方要做的事不同。把两者都折成 error==nil 会让调用方无从分辨。
func (c *Client) Insert(r OrderReq, timeout time.Duration) (OrderState, error) {
	ref, err := c.send(r)
	if err != nil {
		return OrderState{}, err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		// ⚠️ **不要在过渡态上返回。** status "a"（已提交）只说明单送到了柜台，
		// 它既没挂上也没成交 —— 20260910 夜盘平仓时我在 "a" 上就返回了，
		// 于是报「没平掉，仓还在」，**而那笔单几秒后成交了**。
		//
		// **「还没有结果」被当成了「结果是失败」**，而两者要人做的事完全相反：
		// 前者该等，后者该去收拾。
		if s, ok := c.book.get(ref); ok && settled(s.Status) {
			return s, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	s, _ := c.book.get(ref)
	// ⚠️ 超时是**没有结论**，不是「被拒」——两者要人做的事完全不同：
	// 前者要去查这笔单还在不在，后者不必。
	return s, fmt.Errorf("%v 内没有等到 %s 的任何状态回报 —— "+
		"⚠️ **没有结论**，不是「被拒」：这笔单可能还挂在柜台上，去查再撤", timeout, ref)
}

// Order 取本次运行发出的某笔委托的当前状态。
//
// ⚠️ 存在的理由：撤单前要先知道它还挂不挂着 —— 对一笔已经终态的单发撤单，
// 柜台会回一条与「撤不掉」长得一样的错，而那会淹掉真正的撤单失败。
func (c *Client) Order(ref string) (OrderState, bool) { return c.book.get(ref) }

// Cancel 撤一笔还挂着的委托。
func (c *Client) Cancel(ref string, r OrderReq) error {
	f := def.CThostFtdcInputOrderActionField{ActionFlag: def.THOST_FTDC_AF_Delete}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	copy(f.UserID[:], c.cred.UserID)
	copy(f.OrderRef[:], ref)
	copy(f.ExchangeID[:], r.Exchange)
	copy(f.InstrumentID[:], r.Instrument)
	f.FrontID = def.TThostFtdcFrontIDType(c.frontID)
	f.SessionID = def.TThostFtdcSessionIDType(c.sessionID)
	c.logf("[ctp] 撤单 ref=%s", ref)
	c.req("ReqOrderAction", unsafe.Pointer(&f))
	return nil
}

// registerOrderCallbacks 注册委托回报。由 Connect 调用。
func (c *Client) registerOrderCallbacks() {
	c.on("SetOnRtnOrder", func(o *def.CThostFtdcOrderField) uintptr {
		if o == nil {
			return 0
		}
		ref := text(o.OrderRef[:])
		st, msg := o.OrderStatus, text(o.StatusMsg[:])
		traded := int(o.VolumeTraded)
		c.book.put(ref, func(s *OrderState) {
			s.Status, s.StatusMsg, s.VolumeTraded = byte(st), msg, traded
		})
		c.logf("[ctp] 回报 ref=%s status=%q 已成交=%d  %s", ref, string(st), traded, msg)
		return 0
	})
	c.on("SetOnRspOrderInsert", func(o *def.CThostFtdcInputOrderField,
		info *def.CThostFtdcRspInfoField, _ int, _ bool) uintptr {
		// ⚠️ 这条只在**柜台当场拒单**时来（进不了簿）。它与 OnRtnOrder 的
		// 「已撤单」不是一回事：前者从未进簿，后者进过。
		if o == nil {
			return 0
		}
		ref := text(o.OrderRef[:])
		msg := ""
		if err := errOf(info); err != nil {
			msg = err.Error()
		}
		c.book.put(ref, func(s *OrderState) {
			s.Status, s.StatusMsg = def.THOST_FTDC_OST_Canceled, msg
		})
		c.logf("[ctp] ⚠️ 报单被柜台拒绝 ref=%s  %s", ref, msg)
		return 0
	})

	// ⚠️ 下面三条**全是失败通道**，而漏掉它们的表现是**沉默** ——
	// 20260910 夜盘第一次真发单时就撞上了：单发出去、回调一个都不来、进程等到超时，
	// 而账户上**没有冻结**（说明单根本没挂上）。
	// 「被拒了」与「回报丢了」在那一刻完全分不开，**因为两者都没有声音**。
	//
	// ⚠️ 交易所级的拒单走 ErrRtn，不走 Rsp —— 我只注册了后者。
	c.on("SetOnErrRtnOrderInsert", func(o *def.CThostFtdcInputOrderField,
		info *def.CThostFtdcRspInfoField) uintptr {
		ref := ""
		if o != nil {
			ref = text(o.OrderRef[:])
		}
		msg := ""
		if err := errOf(info); err != nil {
			msg = err.Error()
		}
		c.book.put(ref, func(s *OrderState) {
			s.Status, s.StatusMsg = def.THOST_FTDC_OST_Canceled, "交易所拒单："+msg
		})
		c.logf("[ctp] ⚠️ **交易所**拒单 ref=%s  %s", ref, msg)
		return 0
	})
	c.on("SetOnRspOrderAction", func(_ *def.CThostFtdcInputOrderActionField,
		info *def.CThostFtdcRspInfoField, _ int, _ bool) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ 撤单被柜台拒绝：%v", err)
		}
		return 0
	})
	c.on("SetOnErrRtnOrderAction", func(_ *def.CThostFtdcOrderActionField,
		info *def.CThostFtdcRspInfoField) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ **交易所**拒绝撤单：%v", err)
		}
		return 0
	})
}

// settled 判一个状态是不是**可以据以行动**的。
//
// ⚠️ 判据写成白名单而不是「非零即可」：CTP 的状态字将来可能多出取值，
// 而黑名单式的「不是 a 就算数」会把一个新的过渡态当成终态。
func settled(st byte) bool {
	switch st {
	case def.THOST_FTDC_OST_AllTraded, // 全部成交
		def.THOST_FTDC_OST_PartTradedQueueing,  // 部分成交还在队列
		def.THOST_FTDC_OST_PartTradedNotQueueing, // 部分成交不在队列（已撤余量）
		def.THOST_FTDC_OST_NoTradeQueueing,     // 未成交还在队列 —— 挂上了
		def.THOST_FTDC_OST_NoTradeNotQueueing,  // 未成交不在队列
		def.THOST_FTDC_OST_Canceled:            // 已撤单（含被拒）
		return true
	}
	// ⚠️ 其余（"a" 未知/已提交 等）一律当成**还没有结果**。
	return false
}
