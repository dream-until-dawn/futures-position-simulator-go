package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

type failedCancelResting struct {
	*fakeRestingProbe
	failMain, fillOnCancel bool
	cancelFailure          error
}

func (f *failedCancelResting) Cancel(ref string, request ctp.OrderReq) error {
	if err := f.fakeRestingProbe.Cancel(ref, request); err != nil {
		return err
	}
	if (ref == f.mainRef) == f.failMain {
		if f.fillOnCancel {
			f.fill(ref)
		}
		return f.cancelFailure
	}
	return nil
}

// TestRestingWorkflowKeepsNativeCancelError 核真实执行器，而不止核等待辅助函数。
func TestRestingWorkflowKeepsNativeCancelError(t *testing.T) {
	for _, main := range []bool{false, true} {
		for _, filled := range []bool{false, true} {
			sentinel := &ctp.NativeOrderError{Operation: "cancel", Code: -3}
			f := &failedCancelResting{fakeRestingProbe: newFakeRestingProbe(), failMain: main, fillOnCancel: filled, cancelFailure: sentinel}
			f.fillMain = false
			report, err := executeRestingPrice(f, restingProbeConfig{PlanCommit: strings.Repeat("a", 40), Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(*restingProbeReport) error { return nil })
			if !errors.Is(err, sentinel) || !report.Failed {
				t.Fatal("完整执行器吞掉原生撤单错误", main, filled, err)
			}
			want := 1
			if main {
				want++
			}
			if filled {
				want++
			}
			if f.sent != want || f.held != 0 || f.maxHeld > 1 || !report.FinalClean || report.Verdict != "no-verdict" {
				t.Fatal("撤单错误后额外开仓或丢失收尾", main, filled, f.sent, f.held, report)
			}
			if !main && f.mainRef != "" {
				t.Fatal("对照撤单失败仍发主单")
			}
		}
	}
}
