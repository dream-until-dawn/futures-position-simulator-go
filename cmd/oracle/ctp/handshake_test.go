package ctp

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func handshakeFixture() handshakeLoginResult {
	return handshakeLoginResult{day: "20261027", maxOrderRef: "41", frontID: 1, sessionID: 2}
}

func runFakeHandshake(h *handshakeCollector, send func(string, int) int32) (handshakeLoginResult, error) {
	id := 0
	return h.run(func() { h.reply(handshakeFront, 0, &handshakeLoginResult{}, 0, true) }, func() int { id++; return id }, send)
}

// TestHandshakeSuccessAndCopy 三步有序，登记早于同步回调，最终返回复制后的登录业务结果。
func TestHandshakeSuccessAndCopy(t *testing.T) {
	h := newHandshake(time.Second, nil)
	var sent []string
	result, err := runFakeHandshake(h, func(stage string, id int) int32 {
		sent = append(sent, stage)
		row := handshakeFixture()
		h.generalError(id, 0) // 零错误码不能被误判成空成功或失败。
		h.reply(stage, id, &row, 0, true)
		row.day = "19990101"
		// 重复回报和上一阶段回报不能覆盖首个完整结果。
		h.reply(stage, id, &row, 0, true)
		h.reply(handshakeAuth, id-1, nil, 99, true)
		return 0
	})
	if err != nil || result != handshakeFixture() || !reflect.DeepEqual(sent, []string{handshakeAuth, handshakeLogin, handshakeConfirm}) {
		t.Fatalf("握手顺序或复制结果错误: %+v %v %v", result, err, sent)
	}
}

func TestHandshakeDiagnosticRedaction(t *testing.T) {
	var text strings.Builder
	h := newHandshake(time.Second, func(d handshakeDiagnostic) { fmt.Fprintf(&text, "%+v\n", d) })
	_, err := runFakeHandshake(h, func(stage string, id int) int32 {
		row := handshakeFixture()
		row.maxOrderRef = "LEAKME"
		h.reply(stage, id, &row, 0, true)
		return 0
	})
	if err == nil || strings.Contains(text.String()+err.Error(), "LEAKME") || !strings.Contains(text.String(), "stage:login") {
		t.Fatal("握手诊断泄漏原始业务字段或缺失阶段", err)
	}
}

// TestHandshakeFailureMatrix 每一步都独立检验错误传播及实际发送数量，不用err非空代替停止判据。
func TestHandshakeFailureMatrix(t *testing.T) {
	stages := []string{handshakeAuth, handshakeLogin, handshakeConfirm}
	for index, target := range stages {
		for _, mode := range []string{"send", "sync-success-send-error", "response", "generic", "empty", "partial", "wrong-id", "wrong-stage"} {
			t.Run(target+"/"+mode, func(t *testing.T) {
				h := newHandshake(100*time.Millisecond, nil)
				var sent []string
				result, err := runFakeHandshake(h, func(stage string, id int) int32 {
					sent = append(sent, stage)
					row := handshakeFixture()
					if stage != target {
						h.reply(stage, id, &row, 0, true)
						return 0
					}
					switch mode {
					case "send":
						return -2
					case "sync-success-send-error":
						h.reply(stage, id, &row, 0, true)
						return -3
					case "response":
						h.reply(stage, id, nil, 17, false)
						h.reply(stage, id, &row, 0, true)
					case "generic":
						h.generalError(id+100, 88)
						h.generalError(id, 19)
					case "empty":
						h.reply(stage, id, nil, 0, true)
					case "partial":
						h.reply(stage, id, &row, 0, false)
					case "wrong-id":
						h.reply(stage, id+100, &row, 0, true)
					case "wrong-stage":
						other := handshakeAuth
						if stage == other {
							other = handshakeLogin
						}
						h.reply(other, id, &row, 0, true)
					}
					return 0
				})
				if err == nil || result != (handshakeLoginResult{}) || !reflect.DeepEqual(sent, stages[:index+1]) {
					t.Fatalf("握手阶段失败后仍推进: mode=%s sent=%v result=%+v err=%v", mode, sent, result, err)
				}
				var failure *handshakeFailure
				if !errors.As(err, &failure) || failure.stage != target || failure.id != index+1 {
					t.Fatalf("握手失败缺少阶段或编号: %v", err)
				}
				codes := map[string]int32{"send": -2, "sync-success-send-error": -3, "response": 17, "generic": 19}
				if code, ok := codes[mode]; ok && failure.code != code {
					t.Fatalf("握手错误码丢失: %+v", failure)
				}
			})
		}
	}
}

// TestHandshakeSharedDeadline 前一步已成功也不能为下一步重新分配完整预算。
func TestHandshakeSharedDeadline(t *testing.T) {
	prepare := newHandshake(time.Second, nil)
	if err := prepare.prepare(handshakeFront, 0); err != nil {
		t.Fatal(err)
	}
	prepare.mu.Lock()
	prepare.deadline = time.Now().Add(-time.Millisecond)
	prepare.mu.Unlock()
	if err := prepare.prepare(handshakeAuth, 1); err == nil {
		t.Fatal("共享截止未阻止下一阶段")
	}
	h := newHandshake(time.Second, nil)
	count := 0
	_, err := runFakeHandshake(h, func(stage string, id int) int32 {
		count++
		row := handshakeFixture()
		h.reply(stage, id, &row, 0, true)
		if stage == handshakeAuth {
			h.mu.Lock()
			h.deadline = time.Now().Add(-time.Millisecond)
			h.mu.Unlock()
		}
		return 0
	})
	if err == nil || count != 1 || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("共享截止未阻止下一阶段: count=%d err=%v", count, err)
	}
}

// TestHandshakeAbortAndLateReply 关闭/断开会唤醒等待，迟到成功、再次准备或再次运行不能复活旧握手。
func TestHandshakeAbortAndLateReply(t *testing.T) {
	for _, kind := range []string{"closed", "disconnected"} {
		beforeRun := newHandshake(time.Second, nil)
		if err := beforeRun.prepare(handshakeFront, 0); err != nil {
			t.Fatal(err)
		}
		beforeRun.abort(kind, 7)
		if err := beforeRun.prepare(handshakeAuth, 1); err == nil {
			t.Fatal("终止后重新准备了握手")
		}
		h := newHandshake(time.Second, nil)
		entered := make(chan struct{})
		finished := make(chan error, 1)
		go func() {
			_, err := runFakeHandshake(h, func(string, int) int32 { close(entered); return 0 })
			finished <- err
		}()
		<-entered
		h.abort(kind, 7)
		h.reply(handshakeAuth, 1, &handshakeLoginResult{}, 0, true)
		select {
		case err := <-finished:
			if err == nil || !strings.Contains(err.Error(), kind) {
				t.Fatal("终止握手被迟到成功复活", err)
			}
		case <-time.After(time.Second):
			t.Fatal("终止未唤醒握手")
		}
		if err := h.prepare(handshakeAuth, 2); err == nil {
			t.Fatal("终止后重新准备了握手")
		}
		called := false
		_, err := h.run(func() { called = true }, func() int { return 3 }, func(string, int) int32 { called = true; return 0 })
		if err == nil || called {
			t.Fatal("旧握手被再次运行")
		}
	}
}

// TestHandshakeLateDeadline 在截止之后到达的成功回报不能掩盖超时。
func TestHandshakeLateDeadline(t *testing.T) {
	h := newHandshake(time.Second, nil)
	if err := h.prepare(handshakeAuth, 1); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.deadline = time.Now().Add(-time.Millisecond)
	h.mu.Unlock()
	row := handshakeFixture()
	h.reply(handshakeAuth, 1, &row, 0, true)
	h.mu.Lock()
	accepted := h.complete || h.result != (handshakeLoginResult{})
	h.mu.Unlock()
	if accepted {
		t.Fatal("截止后的回调进入收集状态")
	}
	if result, err := h.wait(); err == nil || result != (handshakeLoginResult{}) {
		t.Fatal("截止后的回调变成成功", result, err)
	}
}

// TestHandshakeLateSendReturn 最后一步同步成功后发送调用迟返，也不能突破整体截止。
func TestHandshakeLateSendReturn(t *testing.T) {
	h := newHandshake(time.Second, nil)
	count := 0
	result, err := runFakeHandshake(h, func(stage string, id int) int32 {
		count++
		row := handshakeFixture()
		h.reply(stage, id, &row, 0, true)
		if stage == handshakeConfirm {
			h.mu.Lock()
			h.deadline = time.Now().Add(-time.Millisecond)
			h.mu.Unlock()
		}
		return 0
	})
	if err == nil || result != (handshakeLoginResult{}) || count != 3 || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("最后发送迟返被当成及时成功: %+v %v count=%d", result, err, count)
	}
}

func TestHandshakeFrontAndInvalidLogin(t *testing.T) {
	badID := newHandshake(time.Second, nil)
	called := false
	if _, err := badID.run(func() { badID.reply(handshakeFront, 0, &handshakeLoginResult{}, 0, true) }, func() int { return 0 }, func(string, int) int32 { called = true; return 0 }); err == nil || called {
		t.Fatal("非法请求编号仍发送握手")
	}
	for _, timeout := range []time.Duration{0, -time.Second} {
		h := newHandshake(timeout, nil)
		called := false
		if _, err := h.run(func() { called = true }, func() int { return 1 }, func(string, int) int32 { called = true; return 0 }); err == nil || called {
			t.Fatal("非法握手超时仍启动传输")
		}
	}
	h := newHandshake(time.Millisecond, nil)
	sent := false
	if _, err := h.run(func() {}, func() int { return 1 }, func(string, int) int32 { sent = true; return 0 }); err == nil || sent {
		t.Fatal("未连接前置仍发送认证")
	}
	h = newHandshake(time.Second, nil)
	count := 0
	_, err := runFakeHandshake(h, func(stage string, id int) int32 {
		count++
		row := handshakeFixture()
		row.day = "LEAKME"
		h.reply(stage, id, &row, 0, true)
		return 0
	})
	if err == nil || count != 2 || strings.Contains(err.Error(), "LEAKME") {
		t.Fatal("无效登录结果推进或泄漏原文", count, err)
	}
}

func TestHandshakeDiagnosticsAndConcurrentReplies(t *testing.T) {
	var mu sync.Mutex
	var diagnostics []string
	h := newHandshake(time.Second, func(d handshakeDiagnostic) {
		mu.Lock()
		defer mu.Unlock()
		diagnostics = append(diagnostics, fmt.Sprintf("%+v", d))
	})
	_, err := runFakeHandshake(h, func(stage string, id int) int32 {
		row := handshakeFixture()
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); h.reply(stage, id, &row, 0, true); h.generalError(id+20, 99) }()
		}
		wg.Wait()
		return 0
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	text := strings.Join(diagnostics, "\n")
	if strings.Count(text, "event:send-return") != 3 || strings.Contains(text, "LEAKME") || !strings.Contains(text, "settlement-confirm") {
		t.Fatalf("握手诊断缺失或泄漏原文: %s", text)
	}
}

// TestHandshakeInvalidOrderRef 未知引用不能当作已知0；真实0正常，最大整型因无法递增而拒绝。
func TestHandshakeInvalidOrderRef(t *testing.T) {
	for _, reference := range []string{"", "LEAKME", "-1", "9223372036854775807", "0"} {
		h := newHandshake(time.Second, nil)
		count := 0
		result, err := runFakeHandshake(h, func(stage string, id int) int32 {
			count++
			row := handshakeFixture()
			row.maxOrderRef = reference
			h.reply(stage, id, &row, 0, true)
			return 0
		})
		if reference == "0" {
			if err != nil || count != 3 || result.maxOrderRef != "0" {
				t.Fatal("有效零引用被拒绝", err, count)
			}
		} else if err == nil || count != 2 || result != (handshakeLoginResult{}) || strings.Contains(err.Error(), "LEAKME") {
			t.Fatal("未知最大引用被当成可用会话", reference, err, count)
		}
	}
}
