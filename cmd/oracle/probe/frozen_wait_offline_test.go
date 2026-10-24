package probe

import (
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// frozenTimeline 用合成截面和逻辑时钟驱动真实等待判据，不创建网络客户端。
type frozenTimeline struct {
	at      time.Time
	waits   int
	status  string
	changed bool
	missing bool
	onWait  func(*frozenTimeline)
}

func (f *frozenTimeline) PositionOf(string) map[string]any {
	if f.missing {
		return nil
	}
	m := map[string]any{}
	for _, k := range frozenFields {
		m[k] = float64(0)
	}
	if f.changed {
		m[frozenFields[0]] = float64(1)
	}
	return m
}
func (f *frozenTimeline) OrderOf(string) (kq.OrderState, bool) {
	return kq.OrderState{Status: f.status, VolumeLeft: 1}, f.status != ""
}
func (f *frozenTimeline) WaitTrade(d time.Duration) bool {
	f.waits++
	f.at = f.at.Add(d)
	if f.onWait != nil {
		f.onWait(f)
	}
	return false
}

func TestOfflineFrozenWaitReasons(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		changed      bool
		want         waitReason
		waits        int
	}{
		{"拒单不是超时", "finished", false, orderFinished, 0},
		{"挂着但未变化", "ALIVE", false, timedOut, 2},
		{"还未收到委托", "", false, stateUnknown, 2},
		{"冻结已变化", "ALIVE", true, frozenChanged, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &frozenTimeline{at: time.Unix(0, 0), status: tc.status}
			base := frozenSnapshot(f, "s")
			f.changed = tc.changed
			got, why := waitFrozenClock(f, "s", "o", base, time.Second, func() time.Time { return f.at })
			if why != tc.want || f.waits != tc.waits || (why == frozenChanged) == sameFrozen(got, base) {
				t.Fatalf("冻结等待三态错误 why=%v waits=%d", why, f.waits)
			}
		})
	}
}

// TestOfflineFrozenUnknown 不允许缺失状态或持仓字段成为否定观测。
func TestOfflineFrozenUnknown(t *testing.T) {
	for _, missing := range []bool{false, true} {
		f := &frozenTimeline{at: time.Unix(0, 0), status: "ALIVE"}
		base := frozenSnapshot(f, "s")
		if missing {
			f.missing = true
		} else {
			f.status = ""
		}
		_, why := waitFrozenClock(f, "s", "o", base, time.Second, func() time.Time { return f.at })
		if why != stateUnknown {
			t.Fatalf("未知冻结状态被当成可下结论：missing=%v why=%v", missing, why)
		}
	}
}

func TestOfflineFrozenMissingFields(t *testing.T) {
	f := &frozenTimeline{}
	base := frozenSnapshot(f, "s")
	if !knownFrozen(base) {
		t.Fatal("已知零值误拒")
	}
	for _, bad := range []string{"(无截面)", "(缺字段)", "-", "NaN", "+Inf", "-1"} {
		copy := map[string]string{}
		for k, v := range base {
			copy[k] = v
		}
		copy[frozenFields[0]] = bad
		if knownFrozen(copy) {
			t.Fatalf("无效冻结字段被当成已知：%q", bad)
		}
	}
}

// TestOfflineFrozenDeadlineState 验证等待最后一次唤醒时的终态和消失，防止使用旧ALIVE。
func TestOfflineFrozenDeadlineState(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   waitReason
	}{{"FINISHED", orderFinished}, {"", stateUnknown}} {
		f := &frozenTimeline{at: time.Unix(0, 0), status: "ALIVE"}
		base := frozenSnapshot(f, "s")
		f.onWait = func(f *frozenTimeline) {
			if f.waits == 2 {
				f.status = tc.status
			}
		}
		_, why := waitFrozenClock(f, "s", "o", base, time.Second, func() time.Time { return f.at })
		if why != tc.want || f.waits != 2 {
			t.Fatalf("截止时仍使用陈旧委托状态：%q why=%v waits=%d", tc.status, why, f.waits)
		}
	}
}
