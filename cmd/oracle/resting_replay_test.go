package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// replayOwnedClient 的脚本只供应当前快照，不包含等待器的终态判断或重试策略。
type replayOwnedClient struct {
	initial       ctp.OrderState
	frames        []ctp.OrderState
	seen, inserts int
	cancels       []string
	submitError   bool
}

func (r *replayOwnedClient) Insert(ctp.OrderReq, time.Duration) (ctp.OrderState, error) {
	r.inserts++
	if r.submitError {
		return r.initial, errors.New("submit unknown")
	}
	return r.initial, nil
}
func (r *replayOwnedClient) Order(ref string) (ctp.OrderState, bool) {
	if len(r.frames) == 0 {
		return ctp.OrderState{}, false
	}
	i := r.seen
	if i >= len(r.frames) {
		i = len(r.frames) - 1
	}
	r.seen++
	s := r.frames[i]
	return s, s.OrderRef != ""
}
func (r *replayOwnedClient) Cancel(ref string, _ ctp.OrderReq) error {
	r.cancels = append(r.cancels, ref)
	return nil
}

// TestOwnedOrderReplay 使用真实等待器回放重复/乱序/断连/重连/撤单竞争。
func TestOwnedOrderReplay(t *testing.T) {
	queued := ctp.OrderState{OrderRef: "own", OrderSysID: "sys", Status: def.THOST_FTDC_OST_NoTradeQueueing}
	fill := queued
	fill.Status = def.THOST_FTDC_OST_AllTraded
	fill.VolumeTraded = 1
	canceled := queued
	canceled.Status = def.THOST_FTDC_OST_Canceled
	unknown := ctp.OrderState{OrderRef: "own"}
	wrong := fill
	wrong.OrderRef = "foreign"
	changedID := fill
	changedID.OrderSysID = "foreign-sys"
	emptyFill := fill
	emptyFill.VolumeTraded = 0
	partial := queued
	partial.Status = def.THOST_FTDC_OST_PartTradedQueueing
	for _, tc := range []struct {
		name            string
		frames          []ctp.OrderState
		submit, fail    bool
		filled, cancels int
	}{
		{"duplicate-delayed", []ctp.OrderState{queued, queued, fill}, false, false, 1, 0},
		{"disconnect-reconnect", []ctp.OrderState{{}, {}, queued, fill}, false, false, 1, 0},
		{"out-of-order-unknown", []ctp.OrderState{unknown, queued, fill}, false, false, 1, 0},
		{"cancel-race-fill", []ctp.OrderState{queued, queued, queued, queued, fill}, false, false, 1, 1},
		{"cancel-confirmed", []ctp.OrderState{queued, queued, queued, queued, canceled}, false, false, 0, 1},
		{"disconnect-persists", nil, false, true, 0, 1},
		{"submit-unknown-reconciled", []ctp.OrderState{canceled}, true, true, 0, 1},
		{"foreign-ref", []ctp.OrderState{wrong}, false, true, 0, 1},
		{"identity-changed", []ctp.OrderState{changedID}, false, true, 0, 1},
		{"all-traded-with-zero", []ctp.OrderState{emptyFill}, false, true, 0, 1},
		{"one-lot-partial", []ctp.OrderState{partial}, false, true, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &replayOwnedClient{initial: queued, frames: tc.frames, submitError: tc.submit}
			clock := &fakeProbeClock{now: time.Unix(0, 0)}
			got, err := waitOwnedOrder(r, ctp.OrderReq{Volume: 1}, time.Second, time.Second, time.Second, clock)
			if (err != nil) != tc.fail || r.inserts != 1 || len(r.cancels) != tc.cancels || (!tc.fail && (!got.Terminal || got.Filled != tc.filled)) {
				t.Fatalf("回放生命周期不符 name=%s out=%+v err=%v inserts=%d cancels=%v", tc.name, got, err, r.inserts, r.cancels)
			}
			if tc.name == "one-lot-partial" && (err == nil || !strings.Contains(err.Error(), "不允许部分成交")) {
				t.Fatal("回放未拒绝一手部分成交矛盾")
			}
			for _, ref := range r.cancels {
				if ref != "own" {
					t.Fatal("回放撤销外部委托")
				}
			}
			if clock.Now().Sub(time.Unix(0, 0)) > 2*time.Second {
				t.Fatal("回放等待越界")
			}
		})
	}
}

// TestReplayRestartCannotClaimOwnership 重启丢失终态时，JSON 往返的旧报告不能授权平仓。
func TestReplayRestartCannotClaimOwnership(t *testing.T) {
	f := newFakeRestingProbe()
	f.held = 1
	r := &restingProbeReport{Symbol: "SHFE.rb2701", TradingDay: "20261028", Main: ownedOrderOutcome{Ref: "old", Filled: 1}}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, r); err != nil {
		t.Fatal(err)
	}
	if err := finishRestingExposure(f, r, time.Second, f.clock, func() error { return nil }); err == nil || f.sent != 0 || r.FinalClean {
		t.Fatal("重启缺失终态却触发平仓")
	}
	r.Main.Terminal = true
	if err := finishRestingExposure(f, r, time.Second, f.clock, func() error { return nil }); err != nil || !r.FinalClean || f.sent != 1 || f.held != 0 {
		t.Fatalf("恢复明确归属后未能收尾: %+v %v", r, err)
	}
}
