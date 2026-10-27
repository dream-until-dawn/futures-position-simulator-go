package main

import (
	"strings"
	"testing"
)

// TestLifecycleExternal 验证独立模块的真实导出接入，而非同包访问内部字段。
func TestLifecycleExternal(t *testing.T) {
	r, err := demonstrateLifecycle(false)
	if err != nil || r.Status != "passed" || len(r.Frames) != 4 || r.Final.RulesVersion != 3 || r.Final.Account.PreBalance.String() != "1099" || r.Frames[3].Risk.RequiredDeposit.String() != "166" {
		t.Fatalf("独立生命周期/风控接入失败 err=%v report=%+v", err, r)
	}
	failed, err := demonstrateLifecycle(true)
	if err == nil || !strings.Contains(err.Error(), "SettleWithRules") || failed.Status != "failed" || len(failed.Frames) != 2 || failed.Final.RulesVersion != 2 {
		t.Fatalf("独立跨日负例未阻断 err=%v", err)
	}
}

// TestFrozenExternal 钉住完整累计可平量、费用、序号和超平时无半簿。
func TestFrozenExternal(t *testing.T) {
	book, known, err := demonstrateFrozen(false)
	if err != nil || !known || book == nil || len(book.Live()) != 2 || book.LastSequence() != 19 {
		t.Fatalf("独立冻结接入失败 known=%v err=%v", known, err)
	}
	for _, id := range []string{"first", "second"} {
		_, f, ok := book.Get(id)
		if !ok || f.VolumeToday != 1 || f.VolumeHistory != 0 || f.Commission.String() != "3" || !f.Margin.IsZero() {
			t.Fatalf("独立冻结手算不符 id=%s freeze=%+v", id, f)
		}
	}
	book, known, err = demonstrateFrozen(true)
	if err == nil || book != nil || known {
		t.Fatal("独立冻结超平返回半本簿")
	}
}
