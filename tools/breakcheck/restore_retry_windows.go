//go:build windows

package main

import (
	"errors"
	"syscall"
)

// retryableRestoreError 只认可 Windows 共享冲突、锁冲突和用户映射区占用。
func retryableRestoreError(err error) bool {
	return errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33)) || errors.Is(err, syscall.Errno(1224))
}
