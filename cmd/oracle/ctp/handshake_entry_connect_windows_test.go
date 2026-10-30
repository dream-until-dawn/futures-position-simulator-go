//go:build windows

package ctp

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestHandshakeEntryConnectSnapshot 验证成功和失败都在Connect结束时输出一次完整快照。
func TestHandshakeEntryConnectSnapshot(t *testing.T) {
	for _, fail := range []bool{false, true} {
		var lines []string
		c := fakeHandshakeClient(func(h *handshakeCollector, stage string, id int) int32 {
			h.recordEntry(stage, id, 0, true, true)
			row := handshakeFixture()
			h.reply(stage, id, &row, 0, true)
			if fail && stage == handshakeConfirm {
				return -3
			}
			return 0
		})
		c.handshakeStart = func(h *handshakeCollector) {
			h.recordEntry(handshakeFront, 0, 0, true, true)
			h.reply(handshakeFront, 0, &handshakeLoginResult{}, 0, true)
		}
		c.logf = func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }
		err := c.Connect(time.Second)
		if (err != nil) != fail {
			t.Fatal("入口快照改变握手结果", err)
		}
		count := 0
		for _, line := range lines {
			if strings.Contains(line, "[ctp.handshake.entry]") {
				count++
				if strings.Contains(line, "stage=settlement-confirm") && !strings.Contains(line, "count=1") {
					t.Fatal("Connect入口快照早于握手结束", line)
				}
			}
			if strings.Contains(line, "LEAKME") {
				t.Fatal("入口快照泄漏原始负载")
			}
		}
		if count != 6 {
			t.Fatal("Connect返回未输出六阶段入口快照", count)
		}
		if c.Connect(time.Second) == nil {
			t.Fatal("诊断允许重复连接")
		}
		after := 0
		for _, line := range lines {
			if strings.Contains(line, "[ctp.handshake.entry]") {
				after++
			}
		}
		if after != count {
			t.Fatal("拒绝重复连接时伪造新快照")
		}
	}
}
