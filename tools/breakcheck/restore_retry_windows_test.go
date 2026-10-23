//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestWindowsRestoreRetryIsBounded 模拟本次实测的1224映射错误；只重试恢复，不能无限重试。
func TestWindowsRestoreRetryIsBounded(t *testing.T) {
	for _, transient := range []bool{true, false} {
		calls := 0
		path := filepath.Join(t.TempDir(), "original")
		files := breakFileIO{write: func(path string, data []byte, mode os.FileMode) error {
			calls++
			if !transient || calls <= 2 {
				return &os.PathError{Op: "write", Path: path, Err: syscall.Errno(1224)}
			}
			return os.WriteFile(path, data, mode)
		}}
		err := restoreOriginal(files, path, []byte("original"))
		if transient {
			data, readErr := os.ReadFile(path)
			if err != nil || readErr != nil || calls != 3 || string(data) != "original" {
				t.Fatalf("临时映射错误未恢复原始字节：calls=%d err=%v read=%v", calls, err, readErr)
			}
		} else if !errors.Is(err, syscall.Errno(1224)) || calls != 6 {
			t.Fatalf("持续映射错误未有界失败：calls=%d err=%v", calls, err)
		}
	}
	calls := 0
	err := restoreOriginal(breakFileIO{write: func(string, []byte, os.FileMode) error { calls++; return os.ErrPermission }}, "unused", nil)
	if !errors.Is(err, os.ErrPermission) || calls != 1 {
		t.Fatalf("不可重试错误被反复写入：calls=%d err=%v", calls, err)
	}
}
