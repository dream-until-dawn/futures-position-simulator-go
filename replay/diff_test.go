package replay

import (
	"bytes"
	"encoding/json"
	"github.com/shopspring/decimal"
	"strings"
	"testing"
)

// TestFirstDifferenceAndReportIntegrity 分开验证结果不同、输入不同但结果相同、前缀缺失及坏报告。
func TestFirstDifferenceAndReportIntegrity(t *testing.T) {
	p := demoPayload(t)
	a := execute(t, p)
	b := execute(t, p)
	diff, err := Compare(a, b)
	if err != nil || !diff.Equal {
		t.Fatal(diff, err)
	}
	changed := copyPayload(t, p)
	changed.Events[1].Order.Request.Price = decimal.NewFromInt(2999)
	b = execute(t, changed)
	diff, err = Compare(a, b)
	if err != nil || diff.Equal || diff.Step != 1 || !strings.Contains(diff.Path, "/account/") || diff.LeftChanges == nil || !diff.Left.Present || !diff.Right.Present {
		t.Fatalf("首个差异定位失效: %+v %v", diff, err)
	}
	prefix := copyPayload(t, p)
	prefix.Events = prefix.Events[:2]
	b = execute(t, prefix)
	diff, err = Compare(a, b)
	if err != nil || diff.Equal || diff.Step != 2 || !diff.Left.Present || diff.Right.Present || diff.Left.Value == nil {
		t.Fatal("缺失事件变成已知零", diff, err)
	}
	changed = copyPayload(t, p)
	changed.Settings.PositionLimits[0].Volume++
	b = execute(t, changed)
	diff, err = Compare(a, b)
	if err != nil || diff.Equal || diff.Path != "/payload_sha256" {
		t.Fatal("输入不同却报告完全相同", diff, err)
	}
	// 金额比较按规范JSON，而不依赖decimal系数/指数的内存表示。
	amount := decimal.RequireFromString("0.5")
	p.Events = append(p.Events[:0], Event{ID: "deposit-1", Day: p.Settings.Day, Kind: "deposit", Amount: &amount}, Event{ID: "deposit-2", Day: p.Settings.Day, Kind: "deposit", Amount: &amount})
	fractional := execute(t, p)
	raw, _ := json.Marshal(fractional)
	if _, err = ReadReport(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Report){
		func(r *Report) { r.Frames[0].Index = 9 },
		func(r *Report) { r.Frames[0].Account.Balance = decimal.Zero },
		func(r *Report) { r.Frames[0].Changes.Margin = decimal.NewFromInt(1) },
		func(r *Report) { r.Final.LastOrderSequence++ },
		func(r *Report) { r.Frames[len(r.Frames)-1].Statement.Commission = decimal.Zero },
		func(r *Report) { r.Status = "failed" },
		func(r *Report) { r.Frames[0].State.Day++ },
	} {
		raw, _ := json.Marshal(a)
		var bad Report
		_ = json.Unmarshal(raw, &bad)
		change(&bad)
		raw, _ = json.Marshal(bad)
		if _, err = ReadReport(bytes.NewReader(raw)); err == nil {
			t.Fatal("不一致报告被接受")
		}
	}
	unknown, err := Run(demoPayload(t), Options{})
	if err == nil {
		t.Fatal("缺少占位许可却成功")
	}
	if _, err = Compare(unknown, a); err == nil {
		t.Fatal("未知初始状态被当成已知零比较")
	}
	path, left, right, different := firstDifference(map[string]any{}, map[string]any{"risk": 0}, "")
	if !different || path != "/risk" || left.Present || !right.Present {
		t.Fatal("缺失字段和零合并")
	}
}
