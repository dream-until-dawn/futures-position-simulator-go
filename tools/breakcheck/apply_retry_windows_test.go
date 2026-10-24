//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestWindowsApplyRetryIsBounded 只重试同一次破坏的计划字节；测试体最多执行一次。
func TestWindowsApplyRetryIsBounded(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		transient, permission bool
		calls, bodies         int
	}{{"暂时映射占用", true, false, 3, 1}, {"持续映射占用", false, false, 6, 0}, {"权限不足不重试", false, true, 1, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source")
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			calls, bodies := 0, 0
			files := breakFileIO{read: os.ReadFile, write: func(p string, data []byte, mode os.FileMode) error {
				if string(data) == "broken" {
					calls++
					if tc.permission {
						return os.ErrPermission
					}
					if !tc.transient || calls <= 2 {
						return &os.PathError{Op: "write", Path: p, Err: syscall.Errno(1224)}
					}
				}
				return os.WriteFile(p, data, mode)
			}}
			verdict, _ := withBreakFiles(Break{File: path, Old: "original", New: "broken"}, func() (string, string) {
				bodies++
				data, _ := os.ReadFile(path)
				if string(data) != "broken" {
					t.Fatal("未施加破坏就执行测试体")
				}
				return "红对了", ""
			}, files)
			expected := "零层未成立"
			if tc.bodies == 1 {
				expected = "红对了"
			}
			original, err := os.ReadFile(path)
			if calls != tc.calls || bodies != tc.bodies || verdict != expected || err != nil || string(original) != "original" {
				t.Fatalf("施加重试边界错误：calls=%d bodies=%d verdict=%s restored=%q err=%v", calls, bodies, verdict, original, err)
			}
		})
	}
	if !retryableRestoreError(&os.PathError{Err: syscall.Errno(1224)}) || retryableRestoreError(errors.New("other")) {
		t.Fatal("Windows重试错误识别不准确")
	}
}
