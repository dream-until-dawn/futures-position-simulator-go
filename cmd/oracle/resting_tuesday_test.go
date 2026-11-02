package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestTuesdayQuoteDateBinding 预言固定11月3日，不能从生产日期常量生成夹具。
func TestTuesdayQuoteDateBinding(t *testing.T) {
	for _, tc := range []struct {
		date, day string
		valid     bool
	}{
		{"20261103", "20261103", true},
		{"20261102", "20261103", false}, {"20261104", "20261103", false},
		{"20261103", "20261102", false}, {"20261103", "20261104", false},
	} {
		now, err := time.ParseInLocation("20060102 15:04:05", tc.date+" 10:55:00", probeShanghai)
		if err != nil {
			t.Fatal(err)
		}
		md, spec, _ := restingQuoteFixture()
		copy(md.TradingDay[:], tc.day)
		copy(md.UpdateTime[:], "10:55:00")
		err = validateRestingQuote(md, spec, tc.day, now)
		if (err == nil) != tc.valid {
			t.Fatalf("11月3日行情双日期绑定错误 date=%s day=%s valid=%v err=%v", tc.date, tc.day, tc.valid, err)
		}
	}
}

// TestTuesdayExecutionDateBinding 隔离启动日期，旧轮不能借后继账户守卫凑巧被拒。
func TestTuesdayExecutionDateBinding(t *testing.T) {
	for _, tc := range []struct {
		stamp string
		valid bool
	}{
		{"20261103 10:55:00", true}, {"20261103 11:14:59", true},
		{"20261102 10:55:00", false}, {"20261104 10:55:00", false},
		{"20261103 10:54:59", false}, {"20261103 11:15:00", false},
		{"20261102 22:55:00", false},
	} {
		f := newFakeRestingProbe()
		f.clock.now = novemberTime(t, tc.stamp)
		stop := errors.New("synthetic-tuesday-before-account")
		_, err := executeRestingPrice(f, restingProbeConfig{Wait: time.Second, Timeout: time.Second}, f.clock, func(r *restingProbeReport) error {
			if r.Stage == "checking-empty-account" {
				return stop
			}
			return nil
		})
		if errors.Is(err, stop) != tc.valid || f.sent != 0 {
			t.Fatalf("11月3日启动日期绑定错误 at=%s sent=%d err=%v", tc.stamp, f.sent, err)
		}
		if !tc.valid && (err == nil || !strings.Contains(err.Error(), "本次探针日期")) {
			t.Fatal("旧轮启动未由日期先拒绝", tc.stamp, err)
		}
	}
}
