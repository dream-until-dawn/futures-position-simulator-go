package replay

import (
	"fmt"
	"os"
	"path/filepath"
)

type durableFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
	Name() string
}
type fileOps struct {
	create func(string) (durableFile, error)
	link   func(string, string) error
	remove func(string) error
}

// WriteFile 同目录写完、同步、关闭后才原子发布；不覆盖任何已存在文件。
// 只保证文件内容同步和无覆盖发布，不声称目录项已在断电后持久化。
func WriteFile(path string, data []byte) error {
	return publish(path, data, fileOps{create: func(dir string) (durableFile, error) { return os.CreateTemp(dir, ".replay-*.tmp") }, link: os.Link, remove: os.Remove})
}
func publish(path string, data []byte, ops fileOps) (err error) {
	if len(data) > MaxReportBytes {
		return fmt.Errorf("输出超过报告上限")
	}
	f, err := ops.create(filepath.Dir(path))
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		cleanup := ops.remove(f.Name())
		if err == nil && cleanup != nil {
			err = fmt.Errorf("已发布但临时文件清理失败: %w", cleanup)
		}
	}()
	n, err := f.Write(data)
	if err != nil {
		return err
	}
	if n != len(data) {
		return fmt.Errorf("写入不完整")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	closed = true
	if err = f.Close(); err != nil {
		return err
	}
	if err = ops.link(f.Name(), path); err != nil {
		return fmt.Errorf("原子发布失败（目标已存在时不覆盖）: %w", err)
	}
	return nil
}
