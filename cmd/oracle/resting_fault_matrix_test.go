package main

import (
	"errors"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// faultRestingClient 只在假柜台注入故障，绝不连接真实客户端。
type faultRestingClient struct {
	*fakeRestingProbe
	kind string
}

func (c *faultRestingClient) Insert(r ctp.OrderReq, timeout time.Duration) (ctp.OrderState, error) {
	state, err := c.fakeRestingProbe.Insert(r, timeout)
	if c.kind == "reject-control" && c.sent == 1 {
		state.Status = def.THOST_FTDC_OST_Canceled
		state.ErrorID = 42
		c.states[state.OrderRef] = state
	}
	if (c.kind == "timeout-main" || c.kind == "timeout-with-fill") && c.sent == 2 {
		return state, errors.New("injected submit timeout")
	}
	return state, err
}

func (c *faultRestingClient) Cancel(ref string, r ctp.OrderReq) error {
	if c.kind == "cancel-unknown" {
		return errors.New("injected cancel failure")
	}
	return c.fakeRestingProbe.Cancel(ref, r)
}

func (c *faultRestingClient) Positions(timeout time.Duration) (map[string]*def.CThostFtdcInvestorPositionField, error) {
	if c.kind == "positions-before" || (c.kind == "positions-after-fill" && c.held == 1) {
		return nil, errors.New("injected position failure")
	}
	return c.fakeRestingProbe.Positions(timeout)
}

// TestRestingFaultMatrix 验证拒单/超时/查询未知时不继续实验；只允许确认归属的一手收尾。
func TestRestingFaultMatrix(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		sent, held int
		clean      bool
	}{
		{"reject-control", 1, 0, true},
		{"timeout-main", 2, 0, true},
		{"timeout-with-fill", 3, 0, true},
		{"cancel-unknown", 1, 0, false},
		{"positions-before", 0, 0, false},
		{"positions-after-fill", 2, 1, false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			client := &faultRestingClient{fakeRestingProbe: newFakeRestingProbe(), kind: tc.kind}
			client.cancelRace = tc.kind == "timeout-with-fill"
			report, err := executeRestingPrice(client, restingProbeConfig{Wait: 3 * time.Second, Timeout: time.Second}, client.clock, func(*restingProbeReport) error { return nil })
			if err == nil || !report.Failed || client.sent != tc.sent || client.held != tc.held || report.FinalClean != tc.clean || client.maxHeld > 1 {
				t.Fatalf("故障矩阵未阻断或错误收尾：%s report=%+v err=%v sent=%d held=%d", tc.kind, report, err, client.sent, client.held)
			}
		})
	}
}

// TestRestingSaveFailureAtEveryStage 记录失败不能放行后续实验，也不能丢失最终失败标志。
func TestRestingSaveFailureAtEveryStage(t *testing.T) {
	for _, tc := range []struct {
		stage string
		sent  int
	}{
		{"checking-empty-account", 0}, {"control-before-insert", 0}, {"control-after-order", 1},
		{"main-before-insert", 1}, {"main-after-order", 3}, {"main-trades-observed", 3},
		{"closing-owned-long", 3}, {"finished", 3},
	} {
		t.Run(tc.stage, func(t *testing.T) {
			client := newFakeRestingProbe()
			hit := false
			report, err := executeRestingPrice(client, restingProbeConfig{Wait: 3 * time.Second, Timeout: time.Second}, client.clock, func(r *restingProbeReport) error {
				if r.Stage == tc.stage {
					hit = true
					return errors.New("injected persist failure")
				}
				return nil
			})
			if !hit || err == nil || !report.Failed || client.sent != tc.sent || client.held != 0 || client.maxHeld > 1 {
				t.Fatalf("阶段落盘失败未标记或实验继续：%s hit=%v report=%+v err=%v sent=%d held=%d", tc.stage, hit, report, err, client.sent, client.held)
			}
		})
	}
}
