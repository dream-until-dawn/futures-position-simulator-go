//go:build windows && amd64

package ctp

import (
	"reflect"
	"runtime"
	"syscall"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// TestNativeOrderRequestSequence 即时失败也消费唯一请求号和委托引用，不能重用旧身份。
func TestNativeOrderRequestSequence(t *testing.T) {
	c, f, _, _ := nativeOrderFixture(t, 0, false)
	var ids []uintptr
	var refs []string
	insert := func(_ uintptr, request, id uintptr) uintptr {
		row, err := copyNativeOrderFixture[def.CThostFtdcInputOrderField](request)
		if err != nil {
			t.Error(err)
			return 0
		}
		ids = append(ids, id)
		refs = append(refs, text(row.OrderRef[:]))
		code := int32(-3)
		return uintptr(uint32(code))
	}
	cancel := func(_, _, id uintptr) uintptr { ids = append(ids, id); return 0 }
	f.keep = append(f.keep, insert, cancel)
	f.table[0xb8/8], f.table[0xd0/8] = syscall.NewCallback(insert), syscall.NewCallback(cancel)
	for i := 0; i < 2; i++ {
		state, err := c.Insert(nativeOrderRequest(), time.Millisecond)
		if err == nil {
			t.Fatal("序列测试未保留即时失败")
		}
		if err := c.Cancel(state.OrderRef, nativeOrderRequest()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(ids, []uintptr{1, 2, 3, 4}) || !reflect.DeepEqual(refs, []string{"42", "43"}) {
		t.Fatal("原生订单请求号或引用重用", ids, refs)
	}
	runtime.KeepAlive(f)
}
