package main

import (
	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"testing"
	"time"
)

type fakeProbeClock struct{ now time.Time }

func (c *fakeProbeClock) Now() time.Time        { return c.now }
func (c *fakeProbeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

type fakeOwnedOrder struct {
	initial, current          ctp.OrderState
	fill, cancelFill, unknown bool
	sent, canceled            int
}

func (f *fakeOwnedOrder) Insert(ctp.OrderReq, time.Duration) (ctp.OrderState, error) {
	f.sent++
	f.current = f.initial
	return f.initial, nil
}
func (f *fakeOwnedOrder) Order(ref string) (ctp.OrderState, bool) {
	if f.fill {
		f.current.Status = def.THOST_FTDC_OST_AllTraded
		f.current.VolumeTraded = 1
	}
	return f.current, true
}
func (f *fakeOwnedOrder) Cancel(ref string, r ctp.OrderReq) error {
	f.canceled++
	if ref != "own" {
		panic("不许撤其他引用")
	}
	if !f.unknown {
		f.current.Status = def.THOST_FTDC_OST_Canceled
	}
	if f.cancelFill {
		f.current.Status = def.THOST_FTDC_OST_AllTraded
		f.current.VolumeTraded = 1
	}
	return nil
}

// TestOwnedOrderBoundedWait 覆盖排队成交、即时成交、超时撤单和撤单途中成交。
func TestOwnedOrderBoundedWait(t *testing.T) {
	for _, c := range []struct {
		name                 string
		initial              byte
		fill, cancelFill     bool
		wantFill, wantCancel int
		queued               bool
	}{
		{"排队后成交", def.THOST_FTDC_OST_NoTradeQueueing, true, false, 1, 0, true},
		{"即时成交", def.THOST_FTDC_OST_AllTraded, false, false, 1, 0, false},
		{"超时撤单", def.THOST_FTDC_OST_NoTradeQueueing, false, false, 0, 1, true},
		{"撤单时成交", def.THOST_FTDC_OST_NoTradeQueueing, false, true, 1, 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeOwnedOrder{initial: ctp.OrderState{OrderRef: "own", Status: c.initial}, fill: c.fill, cancelFill: c.cancelFill}
			if c.initial == def.THOST_FTDC_OST_AllTraded {
				f.initial.VolumeTraded = 1
			}
			clock := &fakeProbeClock{now: time.Unix(0, 0)}
			got, err := waitOwnedOrder(f, ctp.OrderReq{Volume: 1}, time.Second, time.Second, time.Second, clock)
			if err != nil || got.Filled != c.wantFill || f.canceled != c.wantCancel || got.QueueObserved != c.queued || !got.Terminal {
				t.Fatalf("挂单生命周期错误：%+v cancel=%d err=%v", got, f.canceled, err)
			}
			if clock.Now().Sub(time.Unix(0, 0)) > 2*time.Second {
				t.Fatal("等待超过上限")
			}
		})
	}
}

// TestOwnedOrderRefusesUnknown 未确认终态或超过一手必须失败，不扩大撤单范围。
func TestOwnedOrderRefusesUnknown(t *testing.T) {
	f := &fakeOwnedOrder{initial: ctp.OrderState{OrderRef: "own", Status: 'a'}, unknown: true}
	got, err := waitOwnedOrder(f, ctp.OrderReq{Volume: 1}, time.Second, time.Second, time.Second, &fakeProbeClock{})
	if err == nil || got.Terminal || f.canceled != 1 {
		t.Fatal("未知终态被当成撤单成功")
	}
	f = &fakeOwnedOrder{}
	if _, err = waitOwnedOrder(f, ctp.OrderReq{Volume: 2}, time.Second, time.Second, time.Second, &fakeProbeClock{}); err == nil || f.sent != 0 {
		t.Fatal("两手请求越过探针上限")
	}
}
