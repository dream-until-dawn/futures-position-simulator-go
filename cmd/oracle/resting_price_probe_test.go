package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

type fakeRestingProbe struct {
	mainSentAt        time.Time
	controlAutoCancel bool
	*preflightFake
	clock                                           *fakeProbeClock
	states                                          map[string]ctp.OrderState
	requests                                        map[string]ctp.OrderReq
	trades                                          []*def.CThostFtdcTradeField
	sent, canceled, held, maxHeld                   int
	mainRef                                         string
	fillMain, controlFill, cancelRace, failEvidence bool
	priceOffset                                     float64
}

func newFakeRestingProbe() *fakeRestingProbe {
	_, spec, now := restingQuoteFixture()
	spec.VolumeMultiple = 10
	f := &fakeRestingProbe{preflightFake: newPreflightFake(), clock: &fakeProbeClock{now: now}, states: map[string]ctp.OrderState{}, requests: map[string]ctp.OrderReq{}, fillMain: true}
	f.spec = spec
	return f
}
func (f *fakeRestingProbe) Positions(time.Duration) (map[string]*def.CThostFtdcInvestorPositionField, error) {
	if f.held == 0 {
		return nil, nil
	}
	p := &def.CThostFtdcInvestorPositionField{Position: def.TThostFtdcVolumeType(f.held), TodayPosition: def.TThostFtdcVolumeType(f.held), OpenVolume: def.TThostFtdcVolumeType(f.held), PosiDirection: def.THOST_FTDC_PD_Long, PositionDate: def.THOST_FTDC_PSD_Today, HedgeFlag: def.THOST_FTDC_HF_Speculation}
	copy(p.ExchangeID[:], "SHFE")
	copy(p.InstrumentID[:], "rb2701")
	return map[string]*def.CThostFtdcInvestorPositionField{"own": p}, nil
}

// TestFinishRefusesUnknownOrder 即使查询暂时为空，也不能洗掉本次未确认的开仓委托。
func TestFinishRefusesUnknownOrder(t *testing.T) {
	f := newFakeRestingProbe()
	r := &restingProbeReport{Symbol: "SHFE.rb2701", TradingDay: "20261023", Control: ownedOrderOutcome{Ref: "unknown"}}
	if err := finishRestingExposure(f, r, time.Second, f.clock, func() error { return nil }); err == nil || r.FinalClean {
		t.Fatal("未知开仓终态被宣布收尾完成")
	}
	r.Control.Terminal = true
	if err := finishRestingExposure(f, r, time.Second, f.clock, func() error { return nil }); err != nil || !r.FinalClean {
		t.Fatal("已确认无成交终态不能通过空仓检查")
	}
}

// TestFinishRefusesExtraExposure 出现额外同合约持仓，不能自行平一手后假称属于本次收尾。
func TestFinishRefusesExtraExposure(t *testing.T) {
	f := newFakeRestingProbe()
	f.held = 2
	r := &restingProbeReport{Symbol: "SHFE.rb2701", TradingDay: "20261023", Main: ownedOrderOutcome{Ref: "own", Filled: 1, Terminal: true}}
	if err := finishRestingExposure(f, r, time.Second, f.clock, func() error { return nil }); err == nil || f.sent != 0 || r.FinalClean {
		t.Fatal("额外持仓触发不受允许的平仓")
	}
}

func TestRestingCLIRequiresExplicitRun(t *testing.T) {
	dir := t.TempDir()
	if err := runCTPRestingPrice([]string{"oracle", "ctp-resting-price", "-plan", strings.Repeat("a", 40), "-out", filepath.Join(dir, "never.jsonl"), "-env", filepath.Join(dir, "missing.env")}); err == nil || !strings.Contains(err.Error(), "显式-execute") {
		t.Fatal("默认调用未明确拒绝执行")
	}
}

func TestRestingPreviewDoesNotOrder(t *testing.T) {
	f := newFakeRestingProbe()
	preview, err := collectRestingPricePreview(f, time.Second, f.clock)
	if err != nil || !preview.ReadOnly || preview.PassivePrice != "2997" || f.sent != 0 {
		t.Fatalf("只读预览错误或报单：%+v %v sent=%d", preview, err, f.sent)
	}
}

func TestRestingBuildMatchesPlan(t *testing.T) {
	plan := strings.Repeat("a", 40)
	for _, settings := range [][]debug.BuildSetting{
		nil,
		{{Key: "vcs.revision", Value: plan}, {Key: "vcs.modified", Value: "true"}},
		{{Key: "vcs.revision", Value: strings.Repeat("b", 40)}, {Key: "vcs.modified", Value: "false"}},
	} {
		if _, err := validateRestingBuild(plan, settings); err == nil {
			t.Fatal("未登记或脏构建被接受")
		}
	}
	if got, err := validateRestingBuild(plan, []debug.BuildSetting{{Key: "vcs.revision", Value: plan}, {Key: "vcs.modified", Value: "false"}}); err != nil || got != plan {
		t.Fatal("干净的登记构建被拒")
	}
}

func TestRestingTargetIsSimNowOnly(t *testing.T) {
	if err := validateRestingTarget("9999", "tcp://182.254.243.31:30001"); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"real-broker", "tcp://182.254.243.31:30001"}, {"9999", "tcp://other.example:30001"}, {"9999", "tcp://182.254.243.31:40001"}} {
		if err := validateRestingTarget(pair[0], pair[1]); err == nil {
			t.Fatal("错误柜台或非登记环境被放行")
		}
	}
}
func (f *fakeRestingProbe) UnfinishedOrders(time.Duration) ([]*def.CThostFtdcOrderField, error) {
	var out []*def.CThostFtdcOrderField
	for _, s := range f.states {
		if !ownedTerminal(s.Status) {
			out = append(out, &def.CThostFtdcOrderField{OrderStatus: def.TThostFtdcOrderStatusType(s.Status)})
		}
	}
	return out, nil
}
func (f *fakeRestingProbe) Trades(time.Duration) ([]*def.CThostFtdcTradeField, error) {
	if f.failEvidence && f.held == 1 && f.mainRef != "" {
		return nil, errors.New("成交查询失败")
	}
	return f.trades, nil
}
func (f *fakeRestingProbe) MarketData(string, time.Duration) (*def.CThostFtdcDepthMarketDataField, error) {
	md, _, _ := restingQuoteFixture()
	copy(md.UpdateTime[:], f.clock.Now().In(probeShanghai).Format("15:04:05"))
	md.UpdateMillisec = def.TThostFtdcMillisecType(f.clock.Now().Nanosecond() / int(time.Millisecond))
	return md, nil
}
func (f *fakeRestingProbe) fill(ref string) {
	s := f.states[ref]
	if s.VolumeTraded != 0 {
		return
	}
	r := f.requests[ref]
	s.VolumeTraded = 1
	s.VolumeTotal = 0
	s.Status = def.THOST_FTDC_OST_AllTraded
	f.states[ref] = s
	if r.Direction == def.THOST_FTDC_D_Buy {
		f.held++
	} else {
		f.held--
	}
	if f.held > f.maxHeld {
		f.maxHeld = f.held
	}
	tr := &def.CThostFtdcTradeField{Direction: r.Direction, OffsetFlag: r.Offset, HedgeFlag: def.THOST_FTDC_HF_Speculation, Volume: 1, Price: def.TThostFtdcPriceType(r.LimitPrice)}
	if ref == f.mainRef {
		tr.Price += def.TThostFtdcPriceType(f.priceOffset)
	}
	copy(tr.TradeID[:], "trade"+ref)
	copy(tr.OrderRef[:], ref)
	copy(tr.OrderSysID[:], s.OrderSysID)
	copy(tr.ExchangeID[:], "SHFE")
	copy(tr.InstrumentID[:], "rb2701")
	copy(tr.TradingDay[:], "20261023")
	copy(tr.TradeTime[:], f.clock.Now().In(probeShanghai).Format("15:04:05"))
	f.trades = append(f.trades, tr)
}
func (f *fakeRestingProbe) Insert(r ctp.OrderReq, _ time.Duration) (ctp.OrderState, error) {
	if r.Volume != 1 {
		panic("探针突破一手")
	}
	f.sent++
	ref := fmt.Sprint(f.sent)
	f.requests[ref] = r
	f.states[ref] = ctp.OrderState{OrderRef: ref, OrderSysID: "sys" + ref, Status: def.THOST_FTDC_OST_NoTradeQueueing, VolumeTotal: 1}
	if r.Direction == def.THOST_FTDC_D_Buy && r.LimitPrice != 2700 {
		f.mainRef = ref
		f.mainSentAt = f.clock.Now()
	}
	if r.Direction == def.THOST_FTDC_D_Sell || f.controlFill {
		f.fill(ref)
	}
	return f.states[ref], nil
}
func (f *fakeRestingProbe) Order(ref string) (ctp.OrderState, bool) {
	if f.controlAutoCancel && ref != f.mainRef {
		state := f.states[ref]
		state.Status = def.THOST_FTDC_OST_Canceled
		f.states[ref] = state
	}
	if ref == f.mainRef && f.fillMain && f.clock.Now().Sub(f.mainSentAt) >= 2*time.Second {
		f.fill(ref)
	}
	s, ok := f.states[ref]
	return s, ok
}
func (f *fakeRestingProbe) Cancel(ref string, r ctp.OrderReq) error {
	if _, ok := f.states[ref]; !ok {
		panic("撤了非本次引用")
	}
	f.canceled++
	if f.cancelRace && ref == f.mainRef {
		f.fill(ref)
	} else {
		s := f.states[ref]
		s.Status = def.THOST_FTDC_OST_Canceled
		f.states[ref] = s
	}
	return nil
}

// TestRestingProbeWorkflow 用完整假柜台验证证据、撤单、平仓和单次最大敞口。
func TestRestingProbeWorkflow(t *testing.T) {
	for _, c := range []struct {
		name                string
		fill, control, race bool
		offset              float64
		verdict             string
		sent                int
	}{
		{"排队成交后平仓", true, false, false, 0, "supports-order-price", 3},
		{"价格反例保留", true, false, false, -1, "counterexample", 3},
		{"超时不成交撤单", false, false, false, 0, "no-verdict", 2},
		{"撤单途中成交仍平仓", false, false, true, 0, "supports-order-price", 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeRestingProbe()
			f.fillMain = c.fill
			f.controlFill = c.control
			f.cancelRace = c.race
			f.priceOffset = c.offset
			var stages []string
			report, err := executeRestingPrice(f, restingProbeConfig{PlanCommit: strings.Repeat("a", 40), Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(r *restingProbeReport) error { stages = append(stages, r.Stage); return nil })
			if err != nil || report.Verdict != c.verdict || !report.FinalClean || f.held != 0 || f.sent != c.sent || f.maxHeld > 1 {
				t.Fatalf("探针流程错误：%+v sent=%d held=%d max=%d err=%v", report, f.sent, f.held, f.maxHeld, err)
			}
			if len(stages) < 5 || stages[len(stages)-1] != "finished" {
				t.Fatal("缺失阶段证据")
			}
		})
	}
}

// TestRestingProbeFailuresStillClose 对照意外成交或判据查询/记录失败，仍须平本次一手。
func TestRestingProbeFailuresStillClose(t *testing.T) {
	for _, kind := range []string{"control-fill", "evidence-query", "save-after-fill"} {
		f := newFakeRestingProbe()
		f.controlFill = kind == "control-fill"
		f.failEvidence = kind == "evidence-query"
		report, err := executeRestingPrice(f, restingProbeConfig{Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(r *restingProbeReport) error {
			if kind == "save-after-fill" && r.Stage == "main-after-order" {
				return errors.New("写入失败")
			}
			return nil
		})
		if err == nil || f.held != 0 || !report.FinalClean || report.Close.Filled != 1 {
			t.Fatalf("失败后遗留本次敞口：%s %+v held=%d err=%v", kind, report, f.held, err)
		}
		if kind == "control-fill" && f.mainRef != "" {
			t.Fatal("对照意外成交仍进入主探针")
		}
	}
}
