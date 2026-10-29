package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/shopspring/decimal"
)

// TestRestingRegistrationCLIWindow 用无效构建/不存在env隔离网络，明确核验CLI先拒绝越界而不是下游凑巧失败。
func TestRestingRegistrationCLIWindow(t *testing.T) {
	for _, tc := range []struct {
		day, at string
		valid   bool
	}{
		{"20261029", "21:15:00", true}, {"20261029", "21:34:59", true},
		{"20261029", "21:14:59", false}, {"20261029", "21:35:00", false},
		{"20261029", "10:55:00", false}, {"20261030", "21:15:00", false},
	} {
		dir := t.TempDir()
		out := filepath.Join(dir, "never.jsonl")
		now, err := time.ParseInLocation("20060102 15:04:05", tc.day+" "+tc.at, probeShanghai)
		if err != nil {
			t.Fatal(err)
		}
		err = runCTPRestingPriceAt([]string{"oracle", "ctp-resting-price", "-execute", "-plan", strings.Repeat("a", 40), "-out", out, "-env", filepath.Join(dir, "missing.env")}, now)
		if err == nil {
			t.Fatal("CLI启动窗口未在读取凭据前拒绝")
		}
		if tc.valid {
			if !strings.Contains(err.Error(), "构建") {
				t.Fatal("CLI合法窗口未到达构建守卫", err)
			}
		} else if !strings.Contains(err.Error(), "登记启动窗口") {
			t.Fatal("CLI启动窗口未在读取凭据前拒绝", err)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatal("CLI边界检查提前创建报告")
		}
	}
}

// TestRestingRegistrationTradeWindow 成交观察必须独立核时刻，不能被行情/启动门槛掩盖。
func TestRestingRegistrationTradeWindow(t *testing.T) {
	for _, tc := range []struct {
		at    string
		valid bool
	}{
		{"21:15:00", true}, {"21:39:59", true}, {"21:14:59", false}, {"21:40:00", false}, {"10:56:00", false},
	} {
		r := &def.CThostFtdcTradeField{Direction: def.THOST_FTDC_D_Buy, OffsetFlag: def.THOST_FTDC_OF_Open, HedgeFlag: def.THOST_FTDC_HF_Speculation, Volume: 1, Price: 2997}
		copy(r.TradeID[:], "t")
		copy(r.OrderRef[:], "own")
		copy(r.OrderSysID[:], "sys")
		copy(r.ExchangeID[:], "SHFE")
		copy(r.InstrumentID[:], "rb2701")
		copy(r.TradingDay[:], "20261030")
		copy(r.TradeTime[:], tc.at)
		out := ownedOrderOutcome{Ref: "own", SysID: "sys", QueueObserved: true, LocalQueueMillis: 2000}
		verdict, _, err := restingPriceVerdict([]*def.CThostFtdcTradeField{r}, out, decimal.NewFromInt(2997), "20261030")
		if tc.valid {
			if err != nil || verdict != "supports-order-price" {
				t.Fatal("成交观察窗口合法样本被拒绝", tc.at, err)
			}
		} else if err == nil || verdict != "no-verdict" {
			t.Fatal("成交观察窗口越界样本被接受", tc.at, err, verdict)
		}
	}
}

// TestRestingNightReadWindow 用不存在的env证明越界在读取凭据之前被拒绝，合法端点才触及文件。
func TestRestingNightReadWindow(t *testing.T) {
	for _, tc := range []struct {
		date, at string
		valid    bool
	}{
		{"20261029", "21:00:00", true}, {"20261029", "21:44:59", true},
		{"20261029", "20:59:59", false}, {"20261029", "21:45:00", false},
		{"20261029", "14:40:00", false}, {"20261030", "10:55:00", false},
		{"20261030", "21:10:00", false}, {"20261028", "21:10:00", false},
	} {
		dir := t.TempDir()
		now, err := time.ParseInLocation("20060102 15:04:05", tc.date+" "+tc.at, probeShanghai)
		if err != nil {
			t.Fatal(err)
		}
		err = runCTPRestingPriceAt([]string{"oracle", "ctp-resting-price", "-check", "-env", filepath.Join(dir, "missing.env")}, now)
		if tc.valid {
			if err == nil || !strings.Contains(err.Error(), "missing.env") {
				t.Fatal("只读合法窗口未到达凭据文件守卫", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "登记只读窗口") {
			t.Fatal("只读越界未在读取凭据前拒绝", err)
		}
	}
}

// TestRestingNightQuoteClock 行情日期应是今晚自然日；交易日、午夜及24小时错位均不能代替它。
func TestRestingNightQuoteClock(t *testing.T) {
	for _, tc := range []struct {
		delta time.Duration
		valid bool
	}{
		{0, true}, {5 * time.Second, true}, {5*time.Second + time.Millisecond, false}, {-time.Millisecond, false},
	} {
		md, spec, now := restingQuoteFixture()
		quoted := now.Add(-tc.delta)
		copy(md.UpdateTime[:], quoted.Format("15:04:05"))
		md.UpdateMillisec = def.TThostFtdcMillisecType(quoted.Nanosecond() / int(time.Millisecond))
		if err := validateRestingQuote(md, spec, "20261030", now); (err == nil) != tc.valid {
			t.Fatalf("夜盘行情日期或新鲜度错误 delta=%s err=%v", tc.delta, err)
		}
	}
}

// TestRestingNightCloseDeadline 落盘跨过硬截止或使行情过期后，不允许再发归属平仓单。
func TestRestingNightCloseDeadline(t *testing.T) {
	for _, tc := range []struct {
		at    string
		delay time.Duration
		valid bool
	}{
		{"21:44:57", time.Second, true},
		{"21:44:59", time.Second, false},
		{"21:30:00", 6 * time.Second, false},
	} {
		f := newFakeRestingProbe()
		now, err := time.ParseInLocation("20060102 15:04:05", "20261029 "+tc.at, probeShanghai)
		if err != nil {
			t.Fatal(err)
		}
		f.clock.now = now
		f.held = 1
		r := &restingProbeReport{Symbol: "SHFE.rb2701", TradingDay: "20261030", Main: ownedOrderOutcome{Ref: "own", Filled: 1, Terminal: true}}
		err = finishRestingExposure(f, r, time.Second, f.clock, func() error { f.clock.now = f.clock.now.Add(tc.delay); return nil })
		if tc.valid {
			if err != nil || f.sent != 1 || f.held != 0 {
				t.Fatalf("截止前归属收尾正例失败 err=%v sent=%d held=%d", err, f.sent, f.held)
			}
		} else if err == nil || f.sent != 0 || f.held != 1 {
			t.Fatalf("落盘后越界或陈旧仍发收尾单 err=%v sent=%d held=%d", err, f.sent, f.held)
		}
	}
}
