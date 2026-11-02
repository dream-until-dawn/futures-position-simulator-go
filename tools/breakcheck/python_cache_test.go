package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPythonBoundaryIgnoresStaleBytecode 构造同尺寸同mtime的真实旧pyc，生产启动器必须执行新源码。
func TestPythonBoundaryIgnoresStaleBytecode(t *testing.T) {
	t.Setenv("PYTHONPYCACHEPREFIX", "")
	t.Setenv("PYTHONDONTWRITEBYTECODE", "1")
	dir := t.TempDir()
	path := filepath.Join(dir, "cached_fixture.py")
	stamp := time.Unix(1700000000, 0)
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	old, current := "VALUE = 'old'\n", "VALUE = 'new'\n"
	if len(old) != len(current) {
		t.Fatal("缓存反例必须同尺寸")
	}
	write(old)
	code := "import sys;sys.path.insert(0," + strconv.Quote(filepath.ToSlash(dir)) + ");import cached_fixture;print(cached_fixture.VALUE)"
	seed := exec.Command("python", "-c", code)
	for _, value := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(value, "=", 2)[0])
		if key != "PYTHONPYCACHEPREFIX" && key != "PYTHONDONTWRITEBYTECODE" {
			seed.Env = append(seed.Env, value)
		}
	}
	output, err := seed.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "old" {
		t.Fatalf("旧pyc准备未成立: %v %q", err, output)
	}
	files, err := filepath.Glob(filepath.Join(dir, "__pycache__", "cached_fixture.*.pyc"))
	if err != nil || len(files) != 1 {
		t.Fatalf("未生成真实旧pyc: %v %v", err, files)
	}
	write(current)
	// 在旧生产启动器上，该断言稳定打印old；不能靠偶然跨过一秒钟避免缓存。
	command := pythonBoundaryCommand(t, "-c", code)
	output, err = command.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "new" {
		t.Fatalf("Python边界读取旧字节码: %v %q", err, output)
	}
}

// TestPythonBoundaryCachePerInvocation 用真实解释器核继承污染不能替换隔离前缀，每次启动必须独立。
func TestPythonBoundaryCachePerInvocation(t *testing.T) {
	poison := t.TempDir()
	t.Setenv("PYTHONPYCACHEPREFIX", poison)
	t.Setenv("PYTHONDONTWRITEBYTECODE", "")
	previous := ""
	for i := 0; i < 2; i++ {
		cmd := pythonBoundaryCommand(t, "-c", "import json,sys;print(json.dumps({'prefix':sys.pycache_prefix,'no_write':sys.dont_write_bytecode}))")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("缓存配置解释器失败: %v %s", err, output)
		}
		var value struct {
			Prefix  string `json:"prefix"`
			NoWrite bool   `json:"no_write"`
		}
		if err := json.Unmarshal(output, &value); err != nil {
			t.Fatal(err)
		}
		if value.Prefix == "" || value.Prefix == poison || value.Prefix == previous {
			t.Fatal("Python边界缓存前缀未逐次隔离", value.Prefix, previous)
		}
		if !value.NoWrite {
			t.Fatal("Python边界缓存写入未禁用")
		}
		previous = value.Prefix
	}
}
