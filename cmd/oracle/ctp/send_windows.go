//go:build windows

package ctp

import (
	"fmt"
	"sync"
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

// OrderState 是一笔委托的当前状态。
type OrderState struct {
	// OrderRef 是本地委托引用，撤单要用。
	OrderRef string
	// Status 是柜台报的状态字（`def.THOST_FTDC_OST_*`）。
	Status byte
	// StatusMsg 是柜台的原话。⚠️ 它是**自由文本**，与 kq 那侧同一条纪律：
	// 可以进本地日志，**不进入库的夹具**。
	StatusMsg string
	// VolumeTraded / VolumeTotal 是已成交与剩余。
	VolumeTraded int
	VolumeTotal  int
}

// Alive 报告这笔委托是不是还挂着。
func (s OrderState) Alive() bool {
	return s.Status == def.THOST_FTDC_OST_NoTradeQueueing ||
		s.Status == def.THOST_FTDC_OST_PartTradedQueueing
}

// orderBook 记本次运行发出去的委托。
type orderBook struct {
	mu sync.Mutex
	m  map[string]*OrderState
}

func (b *orderBook) put(ref string, f func(*OrderState)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]*OrderState{}
	}
	s, ok := b.m[ref]
	if !ok {
		s = &OrderState{OrderRef: ref}
		b.m[ref] = s
	}
	f(s)
}

func (b *orderBook) get(ref string) (OrderState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.m[ref]
	if !ok {
		return OrderState{}, false
	}
	return *s, true
}

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
	ref := fmt.Sprintf("p%d", time.Now().UnixNano()%1e9)
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

	c.book.put(ref, func(s *OrderState) { s.VolumeTotal = r.Volume })
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
		if s, ok := c.book.get(ref); ok && s.Status != 0 {
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
}
