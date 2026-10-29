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
		{"20261029", "14:40:00", true}, {"20261029", "14:49:59", true},
		{"20261029", "14:39:59", false}, {"20261029", "14:50:00", false},
		{"20261029", "10:55:00", false}, {"20261030", "14:40:00", false},
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
		{"14:40:00", true}, {"14:54:59", true}, {"14:39:59", false}, {"14:55:00", false}, {"10:56:00", false},
	} {
		r := &def.CThostFtdcTradeField{Direction: def.THOST_FTDC_D_Buy, OffsetFlag: def.THOST_FTDC_OF_Open, HedgeFlag: def.THOST_FTDC_HF_Speculation, Volume: 1, Price: 2997}
		copy(r.TradeID[:], "t")
		copy(r.OrderRef[:], "own")
		copy(r.OrderSysID[:], "sys")
		copy(r.ExchangeID[:], "SHFE")
		copy(r.InstrumentID[:], "rb2701")
		copy(r.TradingDay[:], "20261029")
		copy(r.TradeTime[:], tc.at)
		out := ownedOrderOutcome{Ref: "own", SysID: "sys", QueueObserved: true, LocalQueueMillis: 2000}
		verdict, _, err := restingPriceVerdict([]*def.CThostFtdcTradeField{r}, out, decimal.NewFromInt(2997), "20261029")
		if tc.valid {
			if err != nil || verdict != "supports-order-price" {
				t.Fatal("成交观察窗口合法样本被拒绝", tc.at, err)
			}
		} else if err == nil || verdict != "no-verdict" {
			t.Fatal("成交观察窗口越界样本被接受", tc.at, err, verdict)
		}
	}
}
