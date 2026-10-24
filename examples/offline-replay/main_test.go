package main

import "testing"

// TestExternalUse 验证外部模块可接入，错误口径仍失败且未知初始状态保持空值。
func TestExternalUse(t *testing.T) {
	report, err := demonstrate(false)
	if err != nil || report.Status != "passed" || len(report.Frames) != 25 || report.Final.Account.PreBalance.String() != "1000000" {
		t.Fatal("外部模块业务闭环失败", err)
	}
	report, err = demonstrate(true)
	if err == nil || report.Status != "failed" || report.InitialAccount != nil || report.Final != nil {
		t.Fatal("错误口径被外部接入放行")
	}
}
