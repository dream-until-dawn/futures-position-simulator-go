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

// TestPythonHandshakeEntryBoundary 验证入口快照缺失、残缺与零值的不同含义。
func TestPythonHandshakeEntryBoundary(t *testing.T) {
	runPythonBoundary(t, "test_handshake_entry")
}

// TestPythonHandshakeIntegrityBoundary 通过真实Python入口验证矛盾证据不能发布成功报告。
func TestPythonHandshakeIntegrityBoundary(t *testing.T) {
	runPythonBoundary(t, "test_handshake_integrity")
}

// TestPythonAdmissionBoundary 经真实本地子进程检查准入失败、留证和一次性边界。
func TestPythonAdmissionBoundary(t *testing.T) { runPythonBoundary(t, "test_admission") }

// TestPythonAdmissionEnvBoundary 仅使用合成文件验证固定目标与共享只读锁。
func TestPythonAdmissionEnvBoundary(t *testing.T) { runPythonBoundary(t, "test_admission_env") }

// TestPythonAdmissionChecksBoundary 验证冻结构建、源码、远端及完整原始证据均实际接线。
func TestPythonAdmissionChecksBoundary(t *testing.T) { runPythonBoundary(t, "test_admission_checks") }

// TestPythonAdmissionRegisteredBoundary 以本地假CLI贯穿新登记唯一启动、锁文件和诊断。
func TestPythonAdmissionRegisteredBoundary(t *testing.T) {
	runPythonBoundary(t, "test_admission_registered")
}
