package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

// runPythonBoundary 把真实 Python 行为纳入 Go 破坏执行器，不用 AST 文本替代运行。
func runPythonBoundary(t *testing.T, module string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("python", "-m", "unittest", module)
	cmd.Dir = filepath.Join(root, "scripts")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Python边界验证失败: %s %v\n%s", module, err, output)
	}
}
func TestPythonIntegrityBoundary(t *testing.T) { runPythonBoundary(t, "test_integrity") }
func TestPythonRuntimeBoundary(t *testing.T)   { runPythonBoundary(t, "test_runtime") }
func TestPythonEvidenceBoundary(t *testing.T)  { runPythonBoundary(t, "test_evidence") }
