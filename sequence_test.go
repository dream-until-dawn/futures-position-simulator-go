package futsim

import (
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"reflect"
	"testing"
)

// TestSequenceRoundTrip 验证恢复不依赖存档数组顺序，且空簿保留高水位。
func TestSequenceRoundTrip(t *testing.T) {
	s := newSim(t)
	r := req(t, "DCE.m2701", types.Buy, types.Open, "2800", 1)
	for _, id := range []string{"z", "a"} {
		if _, err := s.PlaceAccepted(s.acc.Day, id, r); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	st.Orders[0], st.Orders[1] = st.Orders[1], st.Orders[0]
	// 以原配置恢复，避免从 ID 推断接收先后。
	cfg := Config{Day: s.acc.Day, Rules: s.rules, Choices: s.choices, Calendar: s.calendar, TickRounding: s.tickRounding, PositionLimits: s.positionLimits}
	restored, err := Restore(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.book.OrderedIDs(), []string{"z", "a"}) {
		t.Fatal("恢复改变接收顺序")
	}
	for _, id := range restored.Live() {
		if err := restored.Cancel(st.Day, id); err != nil {
			t.Fatal(err)
		}
	}
	empty, _ := restored.State()
	restored, err = Restore(cfg, empty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = restored.PlaceAccepted(st.Day, "next", r); err != nil {
		t.Fatal(err)
	}
	if n, _ := restored.book.Sequence("next"); n != 3 {
		t.Fatal("空簿恢复丢高水位")
	}
	st.Format = 4
	if _, err = Restore(cfg, st); err == nil {
		t.Fatal("接受旧格式")
	}
}
