//go:build windows && amd64

package ctp

import (
	def "gitee.com/haifengat/goctp/ctpdefine"
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// 完整生产注册组合后再调用真实固定SPI，不只验证孤立握手回调。
func TestComposedConfirmationSPI(t *testing.T) {
	run := buildSPIBridge(t)
	c := nativeSPIObject(t)
	h := newHandshake(10*time.Second, nil)
	if err := h.prepare(handshakeConfirm, 7); err != nil {
		t.Fatal(err)
	}
	c.registerHandshakeCallbacks(h)
	c.registerQueryCallbacks()
	c.registerOrderCallbacks()
	c.registerMarketDataCallback()
	method := spiMethod(t, c, 0x80)
	runtime.GC()
	thread, _, _ := run.Call(method, c.spi, 7, 0, 4)
	if thread == 0 {
		t.Fatal("组合SPI线程失败")
	}
	h.mu.Lock()
	complete, err := h.complete, h.err
	h.mu.Unlock()
	entry := h.entries.stages[3].Load()
	if !complete || err != nil || entry == nil || entry.count != 1 {
		t.Fatal("完整注册覆盖结算确认接线")
	}
	runtime.KeepAlive(c)
}

// 来自固定SDK头文件CThostFtdcSettlementInfoConfirmField的x64布局。
func TestConfirmationRequestABILayout(t *testing.T) {
	var f def.CThostFtdcSettlementInfoConfirmField
	if unsafe.Sizeof(f) != 68 || unsafe.Offsetof(f.SettlementID) != 44 || unsafe.Offsetof(f.AccountID) != 48 || unsafe.Offsetof(f.CurrencyID) != 61 {
		t.Fatal("结算确认请求不符合固定SDK布局")
	}
}
