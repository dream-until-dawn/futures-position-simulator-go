//go:build !windows

package main

// retryableRestoreError 其他平台没有本次实测的 Windows 映射错误，不猜测重试条件。
func retryableRestoreError(error) bool { return false }
