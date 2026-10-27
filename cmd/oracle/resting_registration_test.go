package main

import (
	"strings"
	"testing"
	"time"
)

// TestRestingRegistrationQuoteBoundary 用独立字面日期验证新登记，旧日期不能随行情重新变成有效。
func TestRestingRegistrationQuoteBoundary(t *testing.T) {
	for _, tc := range []struct {
		day, at string
		valid   bool
	}{
		{"20261028", "10:30:00", true},
		{"20261028", "10:55:00", true},
		{"20261028", "11:24:59", true},
		{"20261028", "10:29:59", false},
		{"20261028", "11:25:00", false},
		{"20261028", "13:35:00", false},
		{"20261027", "10:55:00", false},
		{"20261029", "10:55:00", false},
	} {
		md, spec, _ := restingQuoteFixture()
		now, err := time.ParseInLocation("20060102 15:04:05", tc.day+" "+tc.at, probeShanghai)
		if err != nil {
			t.Fatal(err)
		}
		copy(md.TradingDay[:], tc.day)
		copy(md.UpdateTime[:], tc.at)
		err = validateRestingQuote(md, spec, tc.day, now)
		if (err == nil) != tc.valid {
			t.Fatalf("登记行情边界错误 day=%s at=%s valid=%v err=%v", tc.day, tc.at, tc.valid, err)
		}
	}
}

// TestRestingRegistrationStartBoundary 两个合法端点间才可执行；越界必须在任何报单之前明确拒绝。
func TestRestingRegistrationStartBoundary(t *testing.T) {
	for _, tc := range []struct {
		day, at string
		valid   bool
	}{
		{"20261028", "10:55:00", true},
		{"20261028", "11:14:59", true},
		{"20261028", "10:54:59", false},
		{"20261028", "11:15:00", false},
		{"20261028", "13:35:00", false},
		{"20261027", "10:55:00", false},
		{"20261029", "10:55:00", false},
	} {
		f := newFakeRestingProbe()
		now, err := time.ParseInLocation("20060102 15:04:05", tc.day+" "+tc.at, probeShanghai)
		if err != nil {
			t.Fatal(err)
		}
		f.clock.now = now
		_, err = executeRestingPrice(f, restingProbeConfig{Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(*restingProbeReport) error { return nil })
		if tc.valid {
			if err != nil || f.sent == 0 {
				t.Fatalf("登记启动正例失败 at=%s err=%v sent=%d", tc.at, err, f.sent)
			}
		} else if err == nil || !strings.Contains(err.Error(), "本次探针日期") || f.sent != 0 {
			t.Fatalf("越界启动未在报单前拒绝 day=%s at=%s err=%v sent=%d", tc.day, tc.at, err, f.sent)
		}
	}
}
