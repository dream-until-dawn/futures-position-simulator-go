package ctp

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHandshakeEntryJournalIndependentAndConcurrent(t *testing.T) {
	h := newHandshake(time.Second, nil)
	h.mu.Lock()
	done := make(chan struct{})
	go func() { h.recordEntry(handshakeConfirm, 7, -3, false, true); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		h.mu.Unlock()
		t.Fatal("入口日志等待收集器锁")
	}
	h.mu.Unlock()
	var workers sync.WaitGroup
	for i := 0; i < 64; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); h.recordEntry(handshakeConfirm, 7, -3, false, true) }()
	}
	workers.Wait()
	state := h.entries.stages[3].Load()
	if state == nil || state.count != 65 || state.firstNS < 0 || state.lastNS < state.firstNS || state.id != 7 || state.code != -3 || state.last || !state.wrappedPayload {
		t.Fatal("入口日志丢失计数或元数据", state)
	}
	var lines []string
	h.logEntries(func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) })
	if len(lines) != 6 || !strings.Contains(lines[3], "count=65") || !strings.Contains(lines[0], "count=0 first_ns=0 last_ns=0") {
		t.Fatal("入口快照缺项或伪造零值", lines)
	}
	before := state.count
	h.recordEntry("unregistered", 1, 1, true, true)
	if h.entries.stages[3].Load().count != before {
		t.Fatal("未知入口阶段污染日志")
	}
}

func TestHandshakeEntryCounterSaturates(t *testing.T) {
	h := newHandshake(time.Second, nil)
	h.entries.stages[3].Store(&handshakeEntryState{count: ^uint64(0), firstNS: 0})
	h.recordEntry(handshakeConfirm, 3, 0, true, true)
	if h.entries.stages[3].Load().count != ^uint64(0) {
		t.Fatal("入口计数溢出归零")
	}
}
