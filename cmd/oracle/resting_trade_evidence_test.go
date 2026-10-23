package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// TestRestingEvidenceWhitelist 保留可重算的原始业务字段，排除身份字段与可变指针引用。
func TestRestingEvidenceWhitelist(t *testing.T) {
	r := &def.CThostFtdcTradeField{Price: 2997.25, Volume: 1}
	copy(r.TradeID[:], "trade")
	copy(r.OrderRef[:], "own")
	copy(r.OrderSysID[:], "sys")
	copy(r.ExchangeID[:], "SHFE")
	copy(r.InstrumentID[:], "rb2701")
	copy(r.TradingDay[:], "20261023")
	copy(r.TradeTime[:], "13:36:00")
	copy(r.InvestorID[:], "LEAKME")
	copy(r.BrokerID[:], "LEAKME")
	got := copyOwnedTradeEvidence([]*def.CThostFtdcTradeField{r}, ownedOrderOutcome{Ref: "own"})
	if len(got) != 1 || got[0].Price != "2997.25" || got[0].TradeID != "trade" || got[0].TradeTime != "13:36:00" {
		t.Fatal("原始成交证据缺失或价格被改写")
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "LEAKME") || strings.Contains(string(raw), "InvestorID") || strings.Contains(string(raw), "BrokerID") {
		t.Fatal("成交证据泄漏身份字段")
	}
	r.Price = 1
	copy(r.TradeID[:], "other")
	if got[0].Price != "2997.25" || got[0].TradeID != "trade" {
		t.Fatal("证据引用了可变原始记录")
	}
}

func TestRestingEvidenceContainsNoIdentityValues(t *testing.T) {
	r := &def.CThostFtdcTradeField{Price: 1, Volume: 1}
	copy(r.OrderRef[:], "own")
	copy(r.ExchangeID[:], "SHFE")
	copy(r.InstrumentID[:], "rb2701")
	copy(r.TradeID[:], "safe")
	copy(r.InvestorID[:], "LEAKME")
	copy(r.BrokerID[:], "LEAKME")
	rows := copyOwnedTradeEvidence([]*def.CThostFtdcTradeField{r}, ownedOrderOutcome{Ref: "own"})
	if len(rows) != 1 {
		t.Fatal("没有产生白名单证据")
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "LEAKME") {
		t.Fatal("成交证据泄漏身份字段")
	}
}

func recordedRestingFixture(t *testing.T) (restingProbeReport, []byte) {
	t.Helper()
	f := newFakeRestingProbe()
	var raw bytes.Buffer
	encoder := json.NewEncoder(&raw)
	plan := strings.Repeat("a", 40)
	report, err := executeRestingPrice(f, restingProbeConfig{PlanCommit: plan, BuildCommit: plan, Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(r *restingProbeReport) error { return encoder.Encode(r) })
	if err != nil {
		t.Fatal(err)
	}
	return *report, raw.Bytes()
}

// TestRestingOfflineRecomputes 从完整阶段报告重算；篡改原始价、归属、构建或收尾不能保留支持结论。
func TestRestingOfflineRecomputes(t *testing.T) {
	report, raw := recordedRestingFixture(t)
	checked, err := readRestingReport(bytes.NewReader(raw))
	if err != nil || checked.Verdict != "supports-order-price" {
		t.Fatalf("正对照不能离线重算：%v", err)
	}
	for _, change := range []func(*restingProbeReport){
		func(r *restingProbeReport) { r.MainTrades[0].Price = "2996" },
		func(r *restingProbeReport) { r.MainTrades[0].OrderRef = "old" },
		func(r *restingProbeReport) { r.MainTrades = nil },
		func(r *restingProbeReport) { r.FinalClean = false },
		func(r *restingProbeReport) { r.BuildCommit = strings.Repeat("b", 40) },
		func(r *restingProbeReport) { r.Control.CancelAttempted = false },
	} {
		bad := report
		bad.MainTrades = append([]restingTradeEvidence(nil), report.MainTrades...)
		change(&bad)
		if _, err = verifyRestingReport(bad); err == nil {
			t.Fatal("篡改报告仍离线通过")
		}
	}
	// 单独最终一行不构成事前记录，不能通过同一核验入口。
	final, _ := json.Marshal(report)
	if _, err = readRestingReport(bytes.NewReader(final)); err == nil {
		t.Fatal("缺少事前阶段仍通过")
	}
}

// TestRestingOfflineRequiresAllPhases 不能用拼接的前后两行绕过真实的中间过程。
func TestRestingOfflineRequiresAllPhases(t *testing.T) {
	_, raw := recordedRestingFixture(t)
	var rows []restingProbeReport
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for decoder.More() {
		var row restingProbeReport
		if err := decoder.Decode(&row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	for _, missing := range []string{"checking-empty-account", "control-before-insert", "control-after-order", "main-after-order", "main-trades-observed", "closing-owned-long"} {
		var altered bytes.Buffer
		encoder := json.NewEncoder(&altered)
		for _, row := range rows {
			if row.Stage != missing {
				if err := encoder.Encode(row); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := readRestingReport(&altered); err == nil {
			t.Fatalf("缺少必要阶段仍通过：%s", missing)
		}
	}
}

func TestRestingControlMustBeOwnCancel(t *testing.T) {
	f := newFakeRestingProbe()
	f.controlAutoCancel = true
	report, err := executeRestingPrice(f, restingProbeConfig{Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(*restingProbeReport) error { return nil })
	if err == nil || f.mainRef != "" || f.sent != 1 || !report.FinalClean {
		t.Fatal("没有本次撤单仍进入主探针")
	}
}

func TestRestingDeclaredSpecMustMatch(t *testing.T) {
	for _, change := range []func(*def.CThostFtdcInstrumentField){func(s *def.CThostFtdcInstrumentField) { s.PriceTick = 2 }, func(s *def.CThostFtdcInstrumentField) { s.VolumeMultiple = 11 }} {
		md, spec, now := restingQuoteFixture()
		change(spec)
		if _, err := passiveProbePrice(md, spec, "20261023", now); err == nil {
			t.Fatal("声明改变未停止")
		}
	}
}

// TestRestingPersistDelayStopsBeforeSend 落盘或调度延迟后不能继续用过期行情报单。
func TestRestingPersistDelayStopsBeforeSend(t *testing.T) {
	for _, stage := range []string{"control-before-insert", "main-before-insert"} {
		f := newFakeRestingProbe()
		report, err := executeRestingPrice(f, restingProbeConfig{Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(r *restingProbeReport) error {
			if r.Stage == stage {
				f.clock.Sleep(6 * time.Second)
			}
			return nil
		})
		wantSent := 0
		if stage == "main-before-insert" {
			wantSent = 1
		}
		if err == nil || f.sent != wantSent || f.mainRef != "" || f.held != 0 {
			t.Fatalf("落盘后陈旧行情仍报单：%s sent=%d report=%+v err=%v", stage, f.sent, report, err)
		}
	}
}
