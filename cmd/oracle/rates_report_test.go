package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

type fakeRateReader struct {
	rows   []*def.CThostFtdcInstrumentCommissionRateField
	errors []error
	calls  int
}

func (f *fakeRateReader) CommissionRate(string, time.Duration) (*def.CThostFtdcInstrumentCommissionRateField, error) {
	i := f.calls
	f.calls++
	return f.rows[i], f.errors[i]
}

func validRateFixture() *def.CThostFtdcInstrumentCommissionRateField {
	r := &def.CThostFtdcInstrumentCommissionRateField{InvestorRange: def.THOST_FTDC_IR_All, OpenRatioByMoney: 0.0001, CloseRatioByMoney: 0.0001, CloseTodayRatioByMoney: 0.0001}
	copy(r.InstrumentID[:], "rb2701")
	return r
}

// TestRatesFailureStopsCollection 用假只读接口证明错误返回且不查询后续合约，不接触网络或env。
func TestRatesFailureStopsCollection(t *testing.T) {
	f := &fakeRateReader{rows: []*def.CThostFtdcInstrumentCommissionRateField{nil, validRateFixture()}, errors: []error{errors.New("synthetic-query-failure"), nil}}
	var output strings.Builder
	err := reportCTPRates(f, "SHFE.rb2701,SHFE.rb2701", time.Second, func(format string, args ...any) { fmt.Fprintf(&output, format+"\n", args...) })
	if err == nil || f.calls != 1 {
		t.Fatalf("费率查询失败未传播或继续查询 err=%v calls=%d", err, f.calls)
	}
	if strings.Contains(output.String(), "这一批里") {
		t.Fatal("部分失败却给出整批无细费率结论")
	}
}

// TestRatesPartialFailureNoConclusion 先有一条成功也不能掩盖后续失败或继续查询。
func TestRatesPartialFailureNoConclusion(t *testing.T) {
	failure := errors.New("synthetic-query-failure")
	f := &fakeRateReader{rows: []*def.CThostFtdcInstrumentCommissionRateField{validRateFixture(), nil, validRateFixture()}, errors: []error{nil, failure, nil}}
	var output strings.Builder
	err := reportCTPRates(f, "SHFE.rb2701,SHFE.rb2701,SHFE.rb2701", time.Second, func(format string, args ...any) { fmt.Fprintf(&output, format+"\n", args...) })
	if !errors.Is(err, failure) || f.calls != 2 || strings.Contains(output.String(), "⇒") {
		t.Fatalf("部分费率失败仍给整批结论 err=%v calls=%d output=%s", err, f.calls, output.String())
	}
}

// TestRatesValidZeroAndRanges 合法零值和三种明确适用范围必须成功，投资者代码不得打印。
func TestRatesValidZeroAndRanges(t *testing.T) {
	for _, scope := range []byte{'1', '2', '3'} {
		row := validRateFixture()
		row.OpenRatioByMoney = 0
		row.CloseRatioByMoney = 0
		row.CloseTodayRatioByMoney = 0
		row.InvestorRange = def.TThostFtdcInvestorRangeType(scope)
		if scope == '3' {
			copy(row.InvestorID[:], "LEAKME")
		}
		f := &fakeRateReader{rows: []*def.CThostFtdcInstrumentCommissionRateField{row}, errors: []error{nil}}
		var output strings.Builder
		err := reportCTPRates(f, "SHFE.rb2701", time.Second, func(format string, args ...any) { fmt.Fprintf(&output, format+"\n", args...) })
		if err != nil || f.calls != 1 || !strings.Contains(output.String(), "SHFE.rb2701") || strings.Contains(output.String(), "LEAKME") {
			t.Fatalf("有效零费率或适用范围正例失败 scope=%c err=%v", scope, err)
		}
	}
}

// TestRatesInvalidRowsStopAtFirst 对六个位置逐个注入NaN/Inf/负值，不能只覆盖开仓按额一格。
func TestRatesInvalidRowsStopAtFirst(t *testing.T) {
	var rows []*def.CThostFtdcInstrumentCommissionRateField
	rows = append(rows, nil)
	for _, change := range []func(*def.CThostFtdcInstrumentCommissionRateField){
		func(r *def.CThostFtdcInstrumentCommissionRateField) { copy(r.InstrumentID[:], "other") },
		func(r *def.CThostFtdcInstrumentCommissionRateField) { copy(r.ExchangeID[:], "DCE") },
		func(r *def.CThostFtdcInstrumentCommissionRateField) { r.InvestorRange = '9' },
		func(r *def.CThostFtdcInstrumentCommissionRateField) { r.InvestorRange = '3' },
	} {
		row := validRateFixture()
		change(row)
		rows = append(rows, row)
	}
	for slot := 0; slot < 6; slot++ {
		for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.001} {
			row := validRateFixture()
			fields := []*def.TThostFtdcRatioType{&row.OpenRatioByMoney, &row.OpenRatioByVolume, &row.CloseRatioByMoney, &row.CloseRatioByVolume, &row.CloseTodayRatioByMoney, &row.CloseTodayRatioByVolume}
			*fields[slot] = def.TThostFtdcRatioType(value)
			rows = append(rows, row)
		}
	}
	for i, row := range rows {
		f := &fakeRateReader{rows: []*def.CThostFtdcInstrumentCommissionRateField{row, validRateFixture()}, errors: []error{nil, nil}}
		err := reportCTPRates(f, "SHFE.rb2701,SHFE.rb2701", time.Second, func(string, ...any) {})
		if err == nil || f.calls != 1 {
			t.Fatalf("无效费率记录未拒绝 row=%d err=%v calls=%d", i, err, f.calls)
		}
	}
}

func TestRatesEmptyRequestDoesNotQuery(t *testing.T) {
	f := &fakeRateReader{}
	for _, tc := range []struct {
		symbols string
		timeout time.Duration
	}{{"", time.Second}, {" ", time.Second}, {",,", time.Second}, {" , \t, ", time.Second}, {"SHFE.rb2701", 0}} {
		if err := reportCTPRates(f, tc.symbols, tc.timeout, func(string, ...any) {}); err == nil || f.calls != 0 {
			t.Fatal("无效费率请求未拒绝或触及查询", err, f.calls)
		}
	}
}
