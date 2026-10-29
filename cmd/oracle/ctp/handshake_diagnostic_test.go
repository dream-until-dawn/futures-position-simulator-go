package ctp

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestHandshakeIgnoredDiagnostic 区分无回调和回调被过滤；诊断不能放宽阶段、编号、期限或隐私边界。
func TestHandshakeIgnoredDiagnostic(t *testing.T) {
	for _, reason := range []string{"request", "stage", "partial", "duplicate", "stopped", "inactive", "late"} {
		t.Run(reason, func(t *testing.T) {
			var trace strings.Builder
			h := newHandshake(time.Second, func(d handshakeDiagnostic) { fmt.Fprintf(&trace, "%+v\n", d) })
			if err := h.prepare(handshakeAuth, 7); err != nil {
				t.Fatal(err)
			}
			stage, id, last := handshakeAuth, 7, true
			row := handshakeLoginResult{day: "LEAKME", maxOrderRef: "LEAKME"}
			switch reason {
			case "request":
				id = 8
			case "stage":
				stage = handshakeLogin
			case "partial":
				last = false
			case "duplicate":
				accepted := handshakeFixture()
				h.reply(stage, id, &accepted, 0, true)
			case "stopped":
				h.seal()
			case "inactive":
				h.active = false
			case "late":
				h.deadline = time.Now().Add(-time.Second)
			}
			h.reply(stage, id, &row, 0, last)
			lines := strings.Split(strings.TrimSpace(trace.String()), "\n")
			lastLine := lines[len(lines)-1]
			if !strings.Contains(lastLine, "event:ignored-"+reason) || !strings.Contains(lastLine, fmt.Sprintf("receivedID:%d", id)) || !strings.Contains(lastLine, "receivedStage:"+stage) || !strings.Contains(lastLine, fmt.Sprintf("last:%t", last)) || !strings.Contains(lastLine, "hasPayload:true") || strings.Contains(trace.String(), "LEAKME") {
				t.Fatalf("被过滤握手回报缺少安全诊断 reason=%s trace=%s", reason, trace.String())
			}
			if reason == "duplicate" {
				if h.result != handshakeFixture() {
					t.Fatal("重复握手回报改写首个完整结果")
				}
			} else if h.complete || h.result != (handshakeLoginResult{}) {
				t.Fatal("被过滤回报仍改变握手结果")
			}
		})
	}
}
