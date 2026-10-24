package replay

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type faultyFile struct {
	durableFile
	stage string
}

func (f faultyFile) Write(b []byte) (int, error) {
	if f.stage == "write" {
		return 0, errors.New("disk")
	}
	if f.stage == "short" {
		return 0, nil
	}
	return f.durableFile.Write(b)
}
func (f faultyFile) Sync() error {
	if f.stage == "sync" {
		return errors.New("sync")
	}
	return f.durableFile.Sync()
}
func (f faultyFile) Close() error {
	err := f.durableFile.Close()
	if f.stage == "close" {
		return errors.New("close")
	}
	return err
}

// TestAtomicNoOverwrite 真实文件正例与写/同步/关闭/发布反例都不能破坏既有报告。
func TestAtomicNoOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := WriteFile(path, []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("overwrite")); err == nil {
		t.Fatal("覆盖了已有证据")
	}
	raw, _ := os.ReadFile(path)
	if !bytes.Equal(raw, []byte("original")) {
		t.Fatal("已有文件被改变")
	}
	for _, stage := range []string{"create", "write", "short", "sync", "close", "publish"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "never.json")
			ops := fileOps{create: func(dir string) (durableFile, error) {
				if stage == "create" {
					return nil, errors.New("create")
				}
				f, err := os.CreateTemp(dir, "temporary")
				if err != nil {
					return nil, err
				}
				return faultyFile{f, stage}, nil
			}, remove: os.Remove, link: func(a, b string) error {
				if stage == "publish" {
					return errors.New("publish")
				}
				return os.Link(a, b)
			}}
			if err := publish(path, []byte("complete"), ops); err == nil {
				t.Fatal("故障落盘却返回成功")
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatal("落盘失败残留文件", files, err)
			}
		})
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatal("成功或拒绝覆盖后遗留临时文件")
	}
	// 发布已成功但清理失败：返回明确错误，最终文件保持完整，不能谎称未发布后自动重试。
	postDir := t.TempDir()
	postPath := filepath.Join(postDir, "published")
	err := publish(postPath, []byte("complete"), fileOps{create: func(dir string) (durableFile, error) { return os.CreateTemp(dir, "pending") }, link: os.Link, remove: func(string) error { return errors.New("cleanup") }})
	if err == nil {
		t.Fatal("发布后清理失败未报告")
	}
	postRaw, readErr := os.ReadFile(postPath)
	if readErr != nil || string(postRaw) != "complete" {
		t.Fatal("清理失败破坏完整发布", readErr)
	}
	if err := WriteFile(filepath.Join(dir, "oversize"), make([]byte, MaxReportBytes+1)); err == nil {
		t.Fatal("未限制输出大小")
	}
}
