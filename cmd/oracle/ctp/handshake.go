package ctp

import (
	"fmt"
	"sync"
	"time"
)

const (
	handshakeFront   = "front"
	handshakeSetup   = "setup"
	handshakeAuth    = "authenticate"
	handshakeLogin   = "login"
	handshakeConfirm = "settlement-confirm"
)

// handshakeLoginResult 只复制会话业务字段，不保留DLL指针或登录原始结构。
type handshakeLoginResult struct {
	day, maxOrderRef   string
	frontID, sessionID int
}

// handshakeFailure 不包含柜台自由文本；超时仅说明缺少完整结果。
type handshakeFailure struct {
	stage, kind string
	id          int
	code        int32
	elapsed     time.Duration
}

// Error 返回固定诊断字段，不格式化原始登录负载或柜台自由文本。
func (e *handshakeFailure) Error() string {
	extra := ""
	if e.kind == "timeout" {
		extra = "，没有结论"
	}
	return fmt.Sprintf("握手 stage=%s request=%d event=%s code=%d elapsed=%s%s", e.stage, e.id, e.kind, e.code, e.elapsed, extra)
}

type handshakeDiagnostic struct {
	stage, event     string
	id               int
	code             int32
	elapsed          time.Duration
	receivedStage    string
	receivedID       int
	last, hasPayload bool
}

// ignoredHandshakeReason 只描述过滤条件，不携带回调业务负载。
func ignoredHandshakeReason(stopped, active, complete bool, actualStage, expectedStage string, actualID, expectedID int, withinDeadline bool) string {
	switch {
	case stopped:
		return "stopped"
	case !active:
		return "inactive"
	case complete:
		return "duplicate"
	case actualStage != expectedStage:
		return "stage"
	case actualID != expectedID:
		return "request"
	case !withinDeadline:
		return "late"
	default:
		return "unknown"
	}
}

// callbackDiagnostic 必须持有mu；仅复制白名单元数据，不格式化value。
func (h *handshakeCollector) callbackDiagnostic(event, stage string, id int, code int32, last, hasPayload bool) handshakeDiagnostic {
	d := h.diagnostic(event, code)
	d.receivedStage, d.receivedID, d.last, d.hasPayload = stage, id, last, hasPayload
	return d
}

// handshakeCollector 一次性、逐阶段收集器。回调只写结果，下一阶段由调用者发送。
type handshakeCollector struct {
	mu                        sync.Mutex
	started, deadline         time.Time
	stage                     string
	id                        int
	active, complete, stopped bool
	runStarted                bool
	done                      chan struct{}
	result                    handshakeLoginResult
	err, abortErr             error
	trace                     func(handshakeDiagnostic)
}

func newHandshake(timeout time.Duration, trace func(handshakeDiagnostic)) *handshakeCollector {
	start := time.Now()
	if trace == nil {
		trace = func(handshakeDiagnostic) {}
	}
	return &handshakeCollector{started: start, deadline: start.Add(timeout), stage: handshakeSetup, trace: trace}
}

func (h *handshakeCollector) failure(kind string, code int32) error {
	return &handshakeFailure{stage: h.stage, id: h.id, kind: kind, code: code, elapsed: time.Since(h.started)}
}

func (h *handshakeCollector) diagnostic(event string, code int32) handshakeDiagnostic {
	return handshakeDiagnostic{stage: h.stage, id: h.id, event: event, code: code, elapsed: time.Since(h.started)}
}

func (h *handshakeCollector) prepare(stage string, id int) error {
	h.mu.Lock()
	if h.stopped {
		err := h.abortErr
		if err == nil {
			err = h.failure("already-finished", 0)
		}
		h.mu.Unlock()
		return err
	}
	h.stage, h.id = stage, id
	if stage != handshakeFront && id <= 0 {
		err := h.failure("invalid-request-id", 0)
		h.mu.Unlock()
		return err
	}
	if !time.Now().Before(h.deadline) {
		err := h.failure("timeout", 0)
		d := h.diagnostic("timeout", 0)
		h.mu.Unlock()
		h.trace(d)
		return err
	}
	h.active, h.complete = true, false
	h.done, h.err, h.result = make(chan struct{}), nil, handshakeLoginResult{}
	d := h.diagnostic("begin", 0)
	h.mu.Unlock()
	h.trace(d)
	return nil
}

// reply 同时核对阶段与编号。错误立即结束，成功必须有负载及结束标记。
func (h *handshakeCollector) reply(stage string, id int, value *handshakeLoginResult, code int32, last bool) {
	h.mu.Lock()
	if h.stopped || !h.active || h.complete || stage != h.stage || id != h.id || !time.Now().Before(h.deadline) {
		reason := ignoredHandshakeReason(h.stopped, h.active, h.complete, stage, h.stage, id, h.id, time.Now().Before(h.deadline))
		d := h.callbackDiagnostic("ignored-"+reason, stage, id, code, last, value != nil)
		h.mu.Unlock()
		h.trace(d)
		return
	}
	if code == 0 && !last {
		d := h.callbackDiagnostic("ignored-partial", stage, id, code, last, value != nil)
		h.mu.Unlock()
		h.trace(d)
		return
	}
	if code != 0 {
		h.err = h.failure("response-error", code)
	} else if value == nil {
		h.err = h.failure("missing-response", 0)
	} else {
		h.result = *value
	}
	h.complete = true
	close(h.done)
	d := h.callbackDiagnostic("response", stage, id, code, last, value != nil)
	h.mu.Unlock()
	h.trace(d)
}

// generalError 仅把同一有效请求的非零通用错误交给握手，不能污染其他请求。
func (h *handshakeCollector) generalError(id int, code int32) {
	h.mu.Lock()
	stage := h.stage
	h.mu.Unlock()
	if code != 0 {
		h.reply(stage, id, nil, code, true)
	}
}

func (h *handshakeCollector) wait() (handshakeLoginResult, error) {
	h.mu.Lock()
	done, remaining := h.done, time.Until(h.deadline)
	h.mu.Unlock()
	timer := time.NewTimer(max(remaining, 0))
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	h.mu.Lock()
	if h.abortErr != nil {
		h.active = false
		err := h.abortErr
		h.mu.Unlock()
		return handshakeLoginResult{}, err
	}
	event := "finished"
	if !h.complete || !time.Now().Before(h.deadline) {
		h.err = h.failure("timeout", 0)
		event = "timeout"
	}
	h.active = false
	result, err := h.result, h.err
	d := h.diagnostic(event, 0)
	h.mu.Unlock()
	h.trace(d)
	if err != nil {
		return handshakeLoginResult{}, err
	}
	return result, nil
}

// abort 可从Close或断开回调唤醒等待；迟到成功不能复活本次收集器。
func (h *handshakeCollector) abort(kind string, code int32) {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return
	}
	h.stopped = true
	h.abortErr = h.failure(kind, code)
	if h.active && !h.complete {
		h.complete = true
		close(h.done)
	}
	d := h.diagnostic(kind, code)
	h.mu.Unlock()
	h.trace(d)
}

// run 的发送器只接受握手阶段和编号，既没有报单参数也不暴露凭据。
func (h *handshakeCollector) run(start func(), nextID func() int, send func(string, int) int32) (handshakeLoginResult, error) {
	h.mu.Lock()
	if h.runStarted {
		err := h.failure("already-started", 0)
		h.mu.Unlock()
		return handshakeLoginResult{}, err
	}
	h.runStarted = true
	h.mu.Unlock()
	defer h.seal()
	if err := h.prepare(handshakeFront, 0); err != nil {
		return handshakeLoginResult{}, err
	}
	start()
	if _, err := h.wait(); err != nil {
		return handshakeLoginResult{}, err
	}
	var login handshakeLoginResult
	for _, stage := range []string{handshakeAuth, handshakeLogin, handshakeConfirm} {
		id := nextID()
		if err := h.prepare(stage, id); err != nil {
			return handshakeLoginResult{}, err
		}
		code := send(stage, id)
		h.mu.Lock()
		d := h.diagnostic("send-return", code)
		var sendErr error
		if code != 0 {
			sendErr = h.failure("send-rejected", code)
		}
		h.mu.Unlock()
		h.trace(d)
		// 即使发送器先同步回调成功，非零发送返回码也必须优先失败。
		if sendErr != nil {
			return handshakeLoginResult{}, sendErr
		}
		value, err := h.wait()
		if err != nil {
			return handshakeLoginResult{}, err
		}
		if stage == handshakeLogin {
			if _, err = time.Parse("20060102", value.day); err != nil {
				h.mu.Lock()
				err = h.failure("invalid-trading-day", 0)
				h.mu.Unlock()
				return handshakeLoginResult{}, err
			}
			if n, ok := parseMaxOrderRef(value.maxOrderRef); !ok || n < 0 || n == int64(1<<63-1) {
				h.mu.Lock()
				err = h.failure("invalid-order-ref", 0)
				h.mu.Unlock()
				return handshakeLoginResult{}, err
			}
			login = value
		}
	}
	return login, nil
}

// seal 结束本次收集，之后的回调全部失效。
func (h *handshakeCollector) seal() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped, h.active = true, false
}
