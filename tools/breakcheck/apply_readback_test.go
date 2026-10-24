package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyReadbackPreventsUnappliedBody 写入成功但字节不符时，不能拿原状的测试结果作破坏证据。
func TestApplyReadbackPreventsUnappliedBody(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "source")
		if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		files := breakFileIO{read: os.ReadFile, write: func(p string, data []byte, mode os.FileMode) error {
			if corrupt && string(data) == "broken" {
				data = []byte("not-the-plan")
			}
			return os.WriteFile(p, data, mode)
		}}
		bodies := 0
		verdict, detail := withBreakFiles(Break{File: path, Old: "original", New: "broken"}, func() (string, string) { bodies++; return "红对了", "" }, files)
		expectedBodies := 1
		if corrupt {
			expectedBodies = 0
		}
		if bodies != expectedBodies || (corrupt && (verdict != "零层未成立" || !strings.Contains(detail, "破坏写入后字节不符"))) {
			t.Fatalf("未确认破坏字节就执行测试：bodies=%d verdict=%s detail=%s", bodies, verdict, detail)
		}
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "original" {
			t.Fatalf("施加异常后未还原：%q %v", data, err)
		}
	}
}
