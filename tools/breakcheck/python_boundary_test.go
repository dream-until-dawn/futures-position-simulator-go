package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// pythonBoundaryCommand 统一Go与Python边界的启动配置。
func pythonBoundaryCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("python", args...)
	// Go按UTF-8读取诊断；覆盖父进程的本地编码及显式ASCII设置。
	cmd.Env = append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
	return cmd
}

// TestPythonBoundaryOutputUTF8 用真实子进程验证父环境不能污染中文诊断。
func TestPythonBoundaryOutputUTF8(t *testing.T) {
	t.Setenv("PYTHONUTF8", "0")
	t.Setenv("PYTHONIOENCODING", "ascii")
	cmd := pythonBoundaryCommand("-c", "import sys;sys.stdout.write('stdout:命令超时|');sys.stdout.flush();sys.stderr.write('stderr:命令超时')")
	output, err := cmd.CombinedOutput()
	if err != nil || string(output) != "stdout:命令超时|stderr:命令超时" {
		t.Fatalf("Python中文输出不是UTF-8: %v %q", err, output)
	}
}

// runPythonBoundary 把真实 Python 行为纳入 Go 破坏执行器，不用 AST 文本替代运行。
func runPythonBoundary(t *testing.T, module string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := pythonBoundaryCommand("-m", "unittest", module)
	cmd.Dir = filepath.Join(root, "scripts")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Python边界验证失败: %s %v\n%s", module, err, output)
	}
}
func TestPythonIntegrityBoundary(t *testing.T) { runPythonBoundary(t, "test_integrity") }
func TestPythonRuntimeBoundary(t *testing.T)   { runPythonBoundary(t, "test_runtime") }
func TestPythonEvidenceBoundary(t *testing.T)  { runPythonBoundary(t, "test_evidence") }
func TestPythonHandshakeDiagnosticsBoundary(t *testing.T) {
	runPythonBoundary(t, "test_handshake_diagnostics")
}
