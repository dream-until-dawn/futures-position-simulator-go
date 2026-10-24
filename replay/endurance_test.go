package replay

import (
	"bytes"
	"encoding/json"
	"github.com/shopspring/decimal"
	"runtime"
	"testing"
)

// TestLongCycleIndependentLedger 200个合成交易日，期末只携一手；按手算公式核对每一天，不借用生产费用函数。
func TestLongCycleIndependentLedger(t *testing.T) {
	p, err := demoDays(200)
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 3; round++ {
		r := execute(t, p)
		dayIndex := 0
		largest := 0
		for _, f := range r.Frames {
			if f.Statement == nil {
				continue
			}
			fee := int64(11)
			if dayIndex == 0 {
				fee = 9
			}
			balance := int64(1000001 - dayIndex)
			if !f.Statement.Commission.Equal(decimal.NewFromInt(fee)) || !f.Statement.Balance.Equal(decimal.NewFromInt(balance)) || !f.Statement.PositionProfit.Equal(decimal.NewFromInt(10)) || f.State.LastOrderSequence != uint64(4*(dayIndex+1)) || len(f.State.Orders) != 0 || len(f.State.Quotas) != 0 || f.State.Account.CommissionTrades != 0 || !f.State.Account.Commission.IsZero() {
				t.Fatalf("长周期独立账本不符 day=%d statement=%+v", dayIndex, f.Statement)
			}
			raw, _ := json.Marshal(f.State)
			if len(raw) > largest {
				largest = len(raw)
			}
			// 每日存档体积必须有确定上界；序号不能带着旧挂单/旧额度无限增长。
			if len(raw) > 4000 {
				t.Fatal("长周期存档持续膨胀")
			}
			dayIndex++
		}
		if dayIndex != 200 || len(r.Frames) != 2599 || r.Final.LastOrderSequence != 800 {
			t.Fatal("长周期事件未完整执行")
		}
		raw, _ := json.Marshal(r)
		if len(raw) >= MaxReportBytes {
			t.Fatal("长周期报告未受控")
		}
		if _, err = ReadReport(bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
		// 只记录多轮GC后的堆观测，避免把机器噪声写成永远绿或偶发红的阈值。
		r = Report{}
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		t.Logf("round=%d largest_state=%d report_bytes=%d heap_after_gc=%d", round, largest, len(raw), stats.HeapAlloc)
	}
}

func TestReportBudgetRollback(t *testing.T) {
	p := demoPayload(t)
	prefix := copyPayload(t, p)
	prefix.Events = nil
	initial := execute(t, prefix)
	raw, _ := json.MarshalIndent(initial, "", "  ")
	r, err := runBounded(p, Options{AllowPlaceholder: true}, len(raw)+4096+14000)
	if err == nil || r.Status != "failed" || r.FailedIndex == nil || len(r.Frames) == 0 || len(r.Frames) >= len(p.Events) {
		t.Fatal("报告预算未阻断", err, len(r.Frames))
	}
	prefix.Events = p.Events[:len(r.Frames)]
	expected := execute(t, prefix)
	if !same(r.Final, expected.Final) {
		t.Fatal("报告超限仍提交事件")
	}
	raw, _ = json.MarshalIndent(r, "", "  ")
	if _, err = ReadReport(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	r, err = runBounded(p, Options{AllowPlaceholder: true}, 10)
	if err == nil || r.Initial != nil || r.Final != nil {
		t.Fatal("初始报告超限却保留伪成功")
	}
}
