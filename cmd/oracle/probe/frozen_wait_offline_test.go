package probe

import (
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
	"testing"
	"time"
)

// frozenTimeline 用合成截面和逻辑时钟驱动真实等待判据，不创建网络客户端。
type frozenTimeline struct {
	at      time.Time
	waits   int
	status  string
	changed bool
}

func (f *frozenTimeline) PositionOf(string) map[string]any {
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
func (f *frozenTimeline) WaitTrade(d time.Duration) bool { f.waits++; f.at = f.at.Add(d); return false }

func TestOfflineFrozenWaitReasons(t *testing.T) {
	for _, tc := range []struct {
		name, status string
		changed      bool
		want         waitReason
		waits        int
	}{
		{"拒单不是超时", "finished", false, orderFinished, 0},
		{"挂着但未变化", "ALIVE", false, timedOut, 2},
		{"还未收到委托", "", false, timedOut, 2},
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
