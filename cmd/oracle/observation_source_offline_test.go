package main

import (
	"encoding/json"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"os"
	"testing"
)

// TestOfflineObservationSourceRoundTrip 仅向临时目录写合成回报，不生成仓库的真实观测文件。
func TestOfflineObservationSourceRoundTrip(t *testing.T) {
	o := observe("20261024", "10:00:00", "SHFE", "rb2701", rejectCase{Name: "synthetic"}, ctp.OrderState{ErrorID: 50}, "rejected", tickUsed{Value: 1, Source: tickFromFlag})
	if o.Source != "probe" {
		t.Fatalf("观测来源标签错误：%q", o.Source)
	}
	path, err := writeRejectCorpus(t.TempDir(), []rejectObservation{o}, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []rejectObservation
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Source != "probe" || rows[0].ErrorID == nil || *rows[0].ErrorID != 50 {
		t.Fatalf("观测来源落盘错误：%s", raw)
	}
	if _, err = writeRejectCorpus(t.TempDir(), nil, func(string, ...any) {}); err == nil {
		t.Fatal("空观测也落盘")
	}
}
