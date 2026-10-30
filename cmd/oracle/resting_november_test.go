package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

func novemberTime(t *testing.T, text string) time.Time {
	t.Helper()
	at, err := time.ParseInLocation("20060102 15:04:05.999999999", text, probeShanghai)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// novemberQuote 使用独立字面夹具，不能从登记常量反推测试预言。
func novemberQuote(at time.Time) (*def.CThostFtdcDepthMarketDataField, *def.CThostFtdcInstrumentField) {
	md := &def.CThostFtdcDepthMarketDataField{LastPrice: 3000, BidPrice1: 2999, AskPrice1: 3001, BidVolume1: 10, AskVolume1: 10, LowerLimitPrice: 2700, UpperLimitPrice: 3300}
	copy(md.InstrumentID[:], "rb2701")
	copy(md.TradingDay[:], "20261102")
	copy(md.UpdateTime[:], at.In(probeShanghai).Format("15:04:05"))
	spec := &def.CThostFtdcInstrumentField{IsTrading: 1, PriceTick: 1, VolumeMultiple: 10}
	copy(spec.InstrumentID[:], "rb2701")
	copy(spec.ExchangeID[:], "SHFE")
	return md, spec
}

func TestNovemberQuoteRegistrationLiteral(t *testing.T) {
	for _, tc := range []struct {
		when  string
		valid bool
	}{
		{"20261102 10:30:00", true}, {"20261102 10:55:00", true}, {"20261102 11:24:59", true},
		{"20261102 10:29:59", false}, {"20261102 11:25:00", false}, {"20261101 10:55:00", false}, {"20261029 21:15:00", false},
	} {
		at := novemberTime(t, tc.when)
		md, spec := novemberQuote(at)
		price, err := passiveProbePrice(md, spec, "20261102", at)
		if tc.valid && (err != nil || price.String() != "2997") {
			t.Fatal("新登记合法日盘行情被拒或改变定价", tc.when, price, err)
		}
		if !tc.valid && err == nil {
			t.Fatal("新登记行情越界仍放行", tc.when)
		}
	}
}

func TestNovemberReadOnlyWindowLiteral(t *testing.T) {
	for _, tc := range []struct {
		when  string
		valid bool
	}{
		{"20261102 10:50:00", true}, {"20261102 10:54:59", true},
		{"20261102 10:49:59", false}, {"20261102 10:55:00", false}, {"20261102 11:00:00", false}, {"20261029 21:10:00", false},
	} {
		missing := filepath.Join(t.TempDir(), "missing.env")
		err := runRestingPriceCheckAt(missing, time.Second, novemberTime(t, tc.when))
		if tc.valid && (err == nil || !strings.Contains(err.Error(), missing)) {
			t.Fatal("新登记只读合法窗口未到达缺失文件守卫", tc.when, err)
		}
		if !tc.valid && (err == nil || !strings.Contains(err.Error(), "登记只读窗口")) {
			t.Fatal("新登记只读越界触及凭据阶段", tc.when, err)
		}
	}
}

func TestNovemberExecutionWindowLiteral(t *testing.T) {
	for _, tc := range []struct {
		when  string
		valid bool
	}{
		{"20261102 10:55:00", true}, {"20261102 11:14:59", true},
		{"20261102 10:54:59", false}, {"20261102 11:15:00", false}, {"20261029 21:15:00", false},
	} {
		f := newFakeRestingProbe()
		f.clock.now = novemberTime(t, tc.when)
		stop := errors.New("synthetic-stop-before-query")
		_, err := executeRestingPrice(f, restingProbeConfig{Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(r *restingProbeReport) error {
			if r.Stage == "checking-empty-account" {
				return stop
			}
			return nil
		})
		if errors.Is(err, stop) != tc.valid {
			t.Fatal("新登记启动窗口未独立绑定", tc.when, err)
		}
		if f.sent != 0 {
			t.Fatal("窗口测试意外发单", f.sent)
		}
	}
}

type novemberProbe struct{ *fakeRestingProbe }

func (*novemberProbe) TradingDay() string { return "20261102" }
func (f *novemberProbe) MarketData(string, time.Duration) (*def.CThostFtdcDepthMarketDataField, error) {
	md, _ := novemberQuote(f.clock.Now())
	return md, nil
}

// TestNovemberOpeningDeadlineAfterSave 行情不足两秒，不能靠陈旧行情代替开仓截止。
func TestNovemberOpeningDeadlineAfterSave(t *testing.T) {
	for _, stage := range []string{"control-before-insert", "main-before-insert"} {
		f := &novemberProbe{newFakeRestingProbe()}
		f.fillMain = false
		f.clock.now = novemberTime(t, "20261102 11:14:59.5")
		visited := false
		report, err := executeRestingPrice(f, restingProbeConfig{Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(r *restingProbeReport) error {
			if r.Stage == stage {
				visited = true
				f.clock.now = f.clock.now.Add(time.Second)
			}
			return nil
		})
		if !visited {
			t.Fatal("开仓截止反例未到达独立落盘点", stage, err)
		}
		want := 0
		if stage == "main-before-insert" {
			want = 1
		}
		if err == nil || f.sent != want || !report.Failed || strings.Contains(err.Error(), "行情陈旧") {
			t.Fatal("启动窗口已过但新鲜行情仍触发新开仓", stage, f.sent, err)
		}
	}
}
