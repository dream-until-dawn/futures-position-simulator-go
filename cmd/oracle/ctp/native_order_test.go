package ctp

import (
	"strings"
	"testing"
)

func TestNativeOrderSlots(t *testing.T) {
	for kind, want := range map[nativeOrderKind]uintptr{nativeOrderInsert: 0xb8, nativeOrderCancel: 0xd0} {
		if got, err := nativeOrderSlot(kind); err != nil || got != want {
			t.Fatal("原生订单偏移不符", kind, got, want, err)
		}
	}
	for _, kind := range []nativeOrderKind{0, 3, 255} {
		if _, err := nativeOrderSlot(kind); err == nil {
			t.Fatal("未知原生订单方法被放行", kind)
		}
	}
	for _, e := range []*NativeOrderError{{Operation: "insert", Code: -3}, {Operation: "cancel", Code: -10001, Local: true}} {
		if !strings.Contains(e.Error(), "code=") || !strings.Contains(e.Error(), "不能据此改写委托终态") {
			t.Fatal("原生订单错误缺少来源边界", e)
		}
	}
}
