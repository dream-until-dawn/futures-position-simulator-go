package main

import (
	"errors"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

type failedNativeOwnedOrder struct {
	fakeOwnedOrder
	submitErr, cancelErr error
	wrongAfterCancel     bool
}

func (f *failedNativeOwnedOrder) Insert(r ctp.OrderReq, timeout time.Duration) (ctp.OrderState, error) {
	state, _ := f.fakeOwnedOrder.Insert(r, timeout)
	return state, f.submitErr
}
func (f *failedNativeOwnedOrder) Cancel(ref string, r ctp.OrderReq) error {
	_ = f.fakeOwnedOrder.Cancel(ref, r)
	return f.cancelErr
}
func (f *failedNativeOwnedOrder) Order(ref string) (ctp.OrderState, bool) {
	state, ok := f.fakeOwnedOrder.Order(ref)
	if f.wrongAfterCancel && f.canceled > 0 {
		state.OrderRef = "other"
	}
	return state, ok
}

// TestOwnedOrderKeepsCancelFailure 终态、未知和非法回报都不能吞掉实际撤单/提交错误。
func TestOwnedOrderKeepsCancelFailure(t *testing.T) {
	for _, mode := range []string{"canceled", "filled", "unknown", "invalid"} {
		for _, failedSubmit := range []bool{false, true} {
			cancelErr := errors.New("synthetic-cancel-error")
			f := &failedNativeOwnedOrder{fakeOwnedOrder: fakeOwnedOrder{initial: ctp.OrderState{OrderRef: "own", Status: def.THOST_FTDC_OST_NoTradeQueueing}}, cancelErr: cancelErr}
			if mode == "filled" {
				f.cancelFill = true
			}
			if mode == "unknown" {
				f.unknown = true
			}
			if mode == "invalid" {
				f.wrongAfterCancel = true
			}
			if failedSubmit {
				f.submitErr = errors.New("synthetic-submit-error")
			}
			out, err := waitOwnedOrder(f, ctp.OrderReq{Volume: 1}, time.Second, time.Second, time.Second, &fakeProbeClock{})
			if !errors.Is(err, cancelErr) {
				t.Fatal("一手流程吞掉原生撤单错误", mode, err)
			}
			if failedSubmit && !errors.Is(err, f.submitErr) {
				t.Fatal("一手流程吞掉提交错误", mode, err)
			}
			if f.sent != 1 || f.canceled != 1 || out.Ref != "own" {
				t.Fatal("一手错误后重试或丢本次引用", f.sent, f.canceled, out)
			}
			if mode == "filled" && (out.Filled != 1 || !out.Terminal) {
				t.Fatal("撤单错误丢失已见一手成交", out)
			}
			if mode == "canceled" && (!out.Terminal || out.Filled != 0) {
				t.Fatal("撤单错误改写已见终态", out)
			}
			if (mode == "unknown" || mode == "invalid") && out.Terminal {
				t.Fatal("错误回报被升级为终态", mode, out)
			}
		}
	}
}
