//go:build windows

package ctp

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// fakeHandshakeClient 只注入握手，不加载DLL、不连接网络，也不提供订单传输。
func fakeHandshakeClient(send func(*handshakeCollector, string, int) int32) *Client {
	c := New(Credentials{UserID: "LEAKME", Password: "LEAKME", AuthCode: "LEAKME"}, nil)
	c.handshakeStart = func(h *handshakeCollector) { h.reply(handshakeFront, 0, &handshakeLoginResult{}, 0, true) }
	c.handshakeSend = send
	return c
}

func TestHandshakeClientFailureDoesNotPublish(t *testing.T) {
	for _, kind := range []string{"send", "response", "generic", "sync-success-send-error"} {
		count := 0
		c := fakeHandshakeClient(func(h *handshakeCollector, stage string, id int) int32 {
			count++
			row := handshakeFixture()
			if stage != handshakeConfirm {
				h.reply(stage, id, &row, 0, true)
				return 0
			}
			switch kind {
			case "send":
				return -2
			case "response":
				h.reply(stage, id, nil, 9, true)
			case "generic":
				h.generalError(id, 9)
			case "sync-success-send-error":
				h.reply(stage, id, &row, 0, true)
				return -3
			}
			return 0
		})
		var log strings.Builder
		c.logf = func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) }
		err := c.Connect(time.Second)
		if err == nil || count != 3 {
			t.Fatalf("Connect失败被当成成功: kind=%s count=%d err=%v", kind, count, err)
		}
		if c.TradingDay() != "" || c.frontID != 0 || c.sessionID != 0 || atomic.LoadInt64(&c.orderSeq) != 0 {
			t.Fatal("握手未完成却发布会话结果")
		}
		if strings.Contains(log.String()+err.Error(), "LEAKME") {
			t.Fatal("Windows握手诊断泄漏凭据")
		}
		if err = c.Connect(time.Second); err == nil || count != 3 {
			t.Fatal("同一Client自动重复握手")
		}
		c.Close()
	}
}

func TestHandshakeClientPublishesAfterConfirmation(t *testing.T) {
	type pending struct {
		h  *handshakeCollector
		id int
	}
	confirm := make(chan pending, 1)
	c := fakeHandshakeClient(func(h *handshakeCollector, stage string, id int) int32 {
		if stage == handshakeConfirm {
			confirm <- pending{h, id}
			return 0
		}
		row := handshakeFixture()
		h.reply(stage, id, &row, 0, true)
		return 0
	})
	done := make(chan error, 1)
	go func() { done <- c.Connect(time.Second) }()
	p := <-confirm
	if c.TradingDay() != "" || atomic.LoadInt64(&c.orderSeq) != 0 {
		t.Fatal("结算确认之前发布会话")
	}
	p.h.reply(handshakeConfirm, p.id, &handshakeLoginResult{}, 0, true)
	if err := <-done; err != nil || c.TradingDay() != "20261027" || atomic.LoadInt64(&c.orderSeq) != 41 || c.frontID != 1 || c.sessionID != 2 {
		t.Fatal("确认成功会话未完整发布", err)
	}
	c.Close()
}

func TestHandshakeClientCloseAndInvalidTimeout(t *testing.T) {
	entered := make(chan struct{})
	c := fakeHandshakeClient(func(*handshakeCollector, string, int) int32 { close(entered); return 0 })
	if err := c.Connect(0); err == nil {
		t.Fatal("非法超时没有拒绝")
	}
	done := make(chan error, 1)
	go func() { done <- c.Connect(time.Second) }()
	<-entered
	c.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "closed") || c.TradingDay() != "" {
			t.Fatal("Close未传播或发布了会话", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close未唤醒Connect")
	}
	info := &def.CThostFtdcRspInfoField{ErrorID: 7}
	copy(info.ErrorMsg[:], "LEAKME")
	if handshakeErrorID(info) != 7 || handshakeErrorID(nil) != 0 {
		t.Fatal("握手业务数值错误码丢失")
	}
}
