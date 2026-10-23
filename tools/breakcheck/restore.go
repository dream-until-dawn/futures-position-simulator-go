package main

import "time"

// restoreOriginal 只对 Windows 临时共享/映射错误重试原始字节，总等待上限1.55秒。
// 不重试测试执行或新破坏；成功写回后调用方还须逐字节读回核验。
func restoreOriginal(files breakFileIO, path string, original []byte) error {
	for attempt := 0; ; attempt++ {
		err := files.write(path, original, 0o644)
		if err == nil || attempt == 5 || !retryableRestoreError(err) {
			return err
		}
		time.Sleep((50 * time.Millisecond) << attempt)
	}
}
