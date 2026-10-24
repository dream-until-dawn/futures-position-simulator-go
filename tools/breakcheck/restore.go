package main

import "time"

// restoreOriginal 恢复原始字节；成功写回后调用方还须逐字节读回核验。
func restoreOriginal(files breakFileIO, path string, original []byte) error {
	return writePlannedBytes(files, path, original)
}

// writePlannedBytes 只对同一次计划写入的Windows共享/锁/映射错误重试。
// 最多6次、总等待1.55秒；不重跑测试、不换计划，调用方必须读回核验。
func writePlannedBytes(files breakFileIO, path string, planned []byte) error {
	for attempt := 0; ; attempt++ {
		err := files.write(path, planned, 0o644)
		if err == nil || attempt == 5 || !retryableRestoreError(err) {
			return err
		}
		time.Sleep((50 * time.Millisecond) << attempt)
	}
}
