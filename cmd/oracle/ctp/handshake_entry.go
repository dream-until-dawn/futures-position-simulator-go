package ctp

import (
	"sync/atomic"
	"time"
)

// 入口日志独立于收集器锁；记录提交不等于SDK事件发生时刻。
var handshakeEntryStages = [...]string{"front", "authenticate", "login", "settlement-confirm", "general-error", "disconnected"}

type handshakeEntryState struct {
	count                uint64
	firstNS, lastNS      int64
	id                   int
	code                 int32
	last, wrappedPayload bool
}

type handshakeEntryJournal struct {
	stages [6]atomic.Pointer[handshakeEntryState]
}

// recordEntry 在生产回调第一步调用；只复制白名单元数据，不等待收集器锁。
func (h *handshakeCollector) recordEntry(stage string, id int, code int32, last, payload bool) {
	index := -1
	for i, name := range handshakeEntryStages {
		if name == stage {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	elapsed := time.Since(h.started).Nanoseconds()
	cell := &h.entries.stages[index]
	for {
		old := cell.Load()
		next := &handshakeEntryState{count: 1, firstNS: elapsed, lastNS: elapsed, id: id, code: code, last: last, wrappedPayload: payload}
		if old != nil {
			*next = *old
			if old.count < ^uint64(0) {
				next.count++
			}
			if elapsed < next.firstNS {
				next.firstNS = elapsed
			}
			if elapsed >= next.lastNS {
				next.lastNS, next.id, next.code, next.last, next.wrappedPayload = elapsed, id, code, last, payload
			}
		}
		if cell.CompareAndSwap(old, next) {
			return
		}
	}
}

// logEntries 每个阶段独立取样；零只表示取样前没有已提交Go入口记录。
func (h *handshakeCollector) logEntries(logf func(string, ...any)) {
	for i, stage := range handshakeEntryStages {
		state := h.entries.stages[i].Load()
		at := time.Since(h.started).Nanoseconds()
		if state == nil {
			state = &handshakeEntryState{}
		}
		logf("[ctp.handshake.entry] stage=%s count=%d first_ns=%d last_ns=%d at_ns=%d request=%d code=%d last=%t wrapped_payload=%t", stage, state.count, state.firstNS, state.lastNS, at, state.id, state.code, state.last, state.wrappedPayload)
	}
}
