package fixture

import (
	"strings"
	"testing"
)

func TestOfflineFrozenReadErrorBeforeMissingSpec(t *testing.T) {
	for _, status := range []bool{false, true} {
		o := withLeft(ord("exchange_id", "DCE", "instrument_id", "x2701", "direction", "BUY", "offset", "OPEN"), "1")
		if status {
			o["status"] = Value{Text: "ALIVE", IsText: true}
		}
		f := &Fixture{HasOrders: true, Orders: map[string]map[string]Value{"x": o}}
		_, has, skipped, err := frozenBookOf(t, f)
		if !status {
			if err == nil || !strings.Contains(err.Error(), "status") || skipped {
				t.Fatalf("委托读取错误被规格跳过吞掉：has=%v skipped=%v err=%v", has, skipped, err)
			}
		} else if err != nil || !skipped {
			t.Fatalf("仅缺规格的对照错误：%v %v", skipped, err)
		}
	}
}
