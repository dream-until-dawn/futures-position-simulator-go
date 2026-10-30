package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRestingRegistrationQuoteBoundary 独立固定自然日与交易日，拒绝两者混淆和旧轮复用。
func TestRestingRegistrationQuoteBoundary(t *testing.T) {
	for _, tc := range []struct {
		date, day, at string
		valid         bool
	}{
		{"20261102", "20261102", "10:30:00", true},
		{"20261102", "20261102", "10:55:00", true},
		{"20261102", "20261102", "11:24:59", true},
		{"20261102", "20261102", "10:29:59", false},
		{"20261102", "20261102", "11:25:00", false},
		{"20261102", "20261102", "14:40:00", false},
		{"20261102", "20261101", "10:55:00", false},
		{"20261102", "20261103", "10:55:00", false},
		{"20261101", "20261102", "10:55:00", false},
		{"20261030", "20261102", "21:15:00", false},
		{"20261029", "20261030", "21:15:00", false},
	} {
		md, spec, _ := restingQuoteFixture()
		now, err := time.ParseInLocation("20060102 15:04:05", tc.date+" "+tc.at, probeShanghai)
		if err != nil {
			t.Fatal(err)
		}
		copy(md.TradingDay[:], tc.day)
		copy(md.UpdateTime[:], tc.at)
		err = validateRestingQuote(md, spec, tc.day, now)
		if (err == nil) != tc.valid {
			t.Fatalf("登记行情边界错误 date=%s day=%s at=%s valid=%v err=%v", tc.date, tc.day, tc.at, tc.valid, err)
		}
	}
}

// TestRestingRegistrationStartBoundary 隔离初始窗口，不让执行途中跨过新开仓截止掩盖初始判定。
func TestRestingRegistrationStartBoundary(t *testing.T) {
	for _, tc := range []struct {
		date, at string
		valid    bool
	}{
		{"20261102", "10:55:00", true}, {"20261102", "11:14:59", true},
		{"20261102", "10:54:59", false}, {"20261102", "11:15:00", false},
		{"20261102", "14:40:00", false}, {"20261103", "10:55:00", false},
		{"20261029", "21:15:00", false}, {"20261101", "10:55:00", false},
	} {
		f := newFakeRestingProbe()
		now, err := time.ParseInLocation("20060102 15:04:05", tc.date+" "+tc.at, probeShanghai)
		if err != nil {
			t.Fatal(err)
		}
		f.clock.now = now
		stop := errors.New("synthetic-initial-window-stop")
		_, err = executeRestingPrice(f, restingProbeConfig{Wait: 3 * time.Second, Timeout: time.Second}, f.clock, func(r *restingProbeReport) error {
			if r.Stage == "checking-empty-account" {
				return stop
			}
			return nil
		})
		if tc.valid {
			if !errors.Is(err, stop) || f.sent != 0 {
				t.Fatalf("登记启动正例失败 at=%s err=%v sent=%d", tc.at, err, f.sent)
			}
		} else if err == nil || !strings.Contains(err.Error(), "本次探针日期") || f.sent != 0 {
			t.Fatalf("越界启动未在报单前拒绝 date=%s at=%s err=%v sent=%d", tc.date, tc.at, err, f.sent)
		}
	}
}
