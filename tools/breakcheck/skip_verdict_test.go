package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSkippedCannotValidateBreak 跳过不能证明预期红，也不能证明预期绿。
func TestSkippedCannotValidateBreak(t *testing.T) {
	for _, expect := range []string{"red", "green"} {
		b := Break{Test: "TestProbe", Pkg: ".", Expect: expect, Want: "目标断言", Why: "盲区"}
		for _, output := range []string{"--- SKIP: TestProbe (0.00s)\nPASS", "    --- SKIP: TestProbe/child (0.01s)\n--- PASS: TestProbe (0.01s)"} {
			if v, _ := classify(b, output, true); v != "零层未成立" {
				t.Fatalf("跳过却被接受：%s %q", expect, v)
			}
		}
		plain := "    probe_test.go:4: 说明里提及 --- SKIP: TestProbe (0.00s)\n--- PASS: TestProbe (0.00s)"
		v, _ := classify(b, plain, true)
		if v == "零层未成立" {
			t.Fatal("普通日志误判跳过")
		}
	}
}

// TestSkippedBreakEndToEnd 真正运行 t.Skip，避免只验证手写输出。
func TestSkippedBreakEndToEnd(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "executions")
	probe := "package skipprobe\nimport (\"testing\";\"os\")\nfunc TestProbe(t *testing.T){f,e:=os.OpenFile(" + strconv.Quote(marker) + ",os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if e!=nil{t.Fatal(e)};if _,e=f.WriteString(\"x\");e!=nil{t.Fatal(e)};if e=f.Close();e!=nil{t.Fatal(e)};t.Skip(\"前置条件缺失\")}\n"
	for name, body := range map[string]string{"go.mod": "module skipprobe\n\ngo 1.21\n", "x.go": "package skipprobe\nconst V=1\n", "x_test.go": probe} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, expect := range []string{"red", "green"} {
		b := Break{File: filepath.Join(dir, "x.go"), Dir: dir, Pkg: ".", Test: "TestProbe", Old: "V=1", New: "V=2", Expect: expect, Want: "目标断言", Why: "盲区"}
		if v, d := run(b); v != "零层未成立" || !strings.Contains(d, "跳过") {
			t.Fatalf("真实跳过被接受：%q %s", v, d)
		}
	}
	if calls, err := os.ReadFile(marker); err != nil || string(calls) != "xx" {
		t.Fatalf("未真实执行两次，可能复用缓存：%q %v", calls, err)
	}
}
