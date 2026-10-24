package main

import (
	"bytes"
	"encoding/json"
	"github.com/dream-until-dawn/futures-position-simulator-go/replay"
	"os"
	"path/filepath"
	"testing"
)

// TestOfflineCLI 走真实文件闭环；没有许可、错误输入和覆盖现有输出都返回非零。
func TestOfflineCLI(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "bundle.json")
	report := filepath.Join(dir, "report.json")
	var stdout bytes.Buffer
	if code, err := run([]string{"demo", "-out", bundle}, &stdout); err != nil || code != 0 {
		t.Fatal(code, err)
	}
	denied := filepath.Join(dir, "denied.json")
	if code, err := run([]string{"run", "-bundle", bundle, "-out", denied}, &stdout); err == nil || code == 0 {
		t.Fatal("默认许可错误")
	}
	raw, _ := os.ReadFile(denied)
	var failed replay.Report
	if err := json.Unmarshal(raw, &failed); err != nil || failed.Status != "failed" || failed.InitialAccount != nil {
		t.Fatal("未知账户写成零")
	}
	if code, err := run([]string{"run", "-bundle", bundle, "-allow-placeholder", "-out", report}, &stdout); err != nil || code != 0 {
		t.Fatal(code, err)
	}
	original, _ := os.ReadFile(report)
	if code, err := run([]string{"run", "-bundle", bundle, "-allow-placeholder", "-out", report}, &stdout); err == nil || code == 0 {
		t.Fatal("CLI覆盖既有结果")
	}
	current, _ := os.ReadFile(report)
	if !bytes.Equal(original, current) {
		t.Fatal("旧结果被覆盖")
	}
	diffPath := filepath.Join(dir, "diff.json")
	if code, err := run([]string{"diff", "-left", report, "-right", report, "-out", diffPath}, &stdout); err != nil || code != 0 {
		t.Fatal(code, err)
	}
	// 合法但较短的回放，diff必须返回专用差异码2，不能把差异当成功相等。
	p, err := replay.Demo()
	if err != nil {
		t.Fatal(err)
	}
	p.Events = p.Events[:2]
	raw, err = replay.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	shorter := filepath.Join(dir, "shorter.json")
	if err = os.WriteFile(shorter, raw, 0600); err != nil {
		t.Fatal(err)
	}
	shorterReport := filepath.Join(dir, "short-report.json")
	if code, err := run([]string{"run", "-bundle", shorter, "-allow-placeholder", "-out", shorterReport}, &stdout); err != nil || code != 0 {
		t.Fatal(code, err)
	}
	if code, err := run([]string{"diff", "-left", report, "-right", shorterReport, "-out", filepath.Join(dir, "different.json")}, &stdout); err != nil || code != 2 {
		t.Fatal("CLI差异退出码错误", code, err)
	}
	for _, args := range [][]string{nil, {"unknown", "-out", filepath.Join(dir, "unknown")}, {"demo", "-out", filepath.Join(dir, "extra"), "-bundle", bundle}, {"diff", "-left", bundle, "-right", report, "-out", filepath.Join(dir, "bad-diff")}} {
		if code, err := run(args, &stdout); err == nil || code == 0 {
			t.Fatal("坏命令被接受", args)
		}
	}
}
