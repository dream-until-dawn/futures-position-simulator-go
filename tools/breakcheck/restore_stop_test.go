package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRestoreFailureStopsFollowingCases 注入真实文件恢复失败，已失败条目须先记录，下一条绝不能执行。
func TestRestoreFailureStopsFollowingCases(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		dir := t.TempDir()
		first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
		for _, path := range []string{first, second} {
			if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		files := breakFileIO{read: os.ReadFile, write: func(path string, data []byte, mode os.FileMode) error {
			if path == first && string(data) == "original" {
				if corrupt {
					return os.WriteFile(path, []byte("corrupt"), mode)
				}
				return errors.New("injected restore failure")
			}
			return os.WriteFile(path, data, mode)
		}}
		rows := []Break{{Name: "first", File: first, Old: "original", New: "broken"}, {Name: "second", File: second, Old: "original", New: "broken"}}
		var recorded []string
		bodies := 0
		stopped, unrun := visitBreaks(rows, "", func(b Break) string {
			verdict, _ := withBreakFiles(b, func() (string, string) { bodies++; return "红对了", "" }, files)
			recorded = append(recorded, verdict)
			return verdict
		})
		if !stopped || unrun != 1 || bodies != 1 || len(recorded) != 1 || recorded[0] != "⚠️ 还原失败" {
			t.Fatalf("还原失败后仍继续执行或失败记录缺失：stop=%v unrun=%d bodies=%d records=%v", stopped, unrun, bodies, recorded)
		}
		data, err := os.ReadFile(second)
		if err != nil || string(data) != "original" {
			t.Fatal("后续文件被触碰")
		}
	}
}

// TestRestoreAttemptsEveryFile 首文件还原失败，也须尽力恢复本条其余文件并保留所有失败路径。
func TestRestoreAttemptsEveryFile(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "first"), filepath.Join(dir, "second"), filepath.Join(dir, "third")}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files := breakFileIO{read: os.ReadFile, write: func(path string, data []byte, mode os.FileMode) error {
		if string(data) == "original" && path != paths[2] {
			return errors.New("injected " + path)
		}
		return os.WriteFile(path, data, mode)
	}}
	b := Break{File: paths[0], Old: "original", New: "broken", Also: []AlsoEdit{{File: paths[1], Old: "original", New: "broken"}, {File: paths[2], Old: "original", New: "broken"}}}
	verdict, detail := withBreakFiles(b, func() (string, string) { return "红对了", "" }, files)
	data, _ := os.ReadFile(paths[2])
	if verdict != "⚠️ 还原失败" || !strings.Contains(detail, paths[0]) || !strings.Contains(detail, paths[1]) || string(data) != "original" {
		t.Fatalf("多文件恢复提前退出或丢失错误：%s %s third=%q", verdict, detail, data)
	}
}

func TestSequenceContinuesAfterOrdinaryVerdicts(t *testing.T) {
	rows := []Break{{Name: "selected-a"}, {Name: "ignored"}, {Name: "selected-b"}, {Name: "selected-c"}}
	var names []string
	stopped, unrun := visitBreaks(rows, "selected", func(b Break) string {
		names = append(names, b.Name)
		if len(names) == 1 {
			return "红错了理由"
		}
		return "如预期仍然绿"
	})
	if stopped || unrun != 0 || strings.Join(names, ",") != "selected-a,selected-b,selected-c" {
		t.Fatalf("普通判定或筛选改变执行顺序：%v %v %d", names, stopped, unrun)
	}
}

func TestLastRestoreFailureStillStops(t *testing.T) {
	stopped, unrun := visitBreaks([]Break{{Name: "last"}}, "", func(Break) string { return "⚠️ 还原失败" })
	if !stopped || unrun != 0 {
		t.Fatal("最后一条恢复失败被隐藏")
	}
}

// TestPartialApplyFailureRestoresEarlierFiles 施加第二文件失败，第一文件仍须恢复且测试体不能运行。
func TestPartialApplyFailureRestoresEarlierFiles(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	called := false
	files := breakFileIO{read: os.ReadFile, write: func(path string, data []byte, mode os.FileMode) error {
		if path == second && string(data) == "broken" {
			return os.ErrPermission
		}
		return os.WriteFile(path, data, mode)
	}}
	b := Break{File: first, Old: "original", New: "broken", Also: []AlsoEdit{{File: second, Old: "original", New: "broken"}}}
	verdict, _ := withBreakFiles(b, func() (string, string) { called = true; return "红对了", "" }, files)
	if verdict != "零层未成立" || called {
		t.Fatalf("施加失败仍跑测试体：%s %v", verdict, called)
	}
	for _, path := range []string{first, second} {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "original" {
			t.Fatalf("部分施加未恢复：%s %v", path, err)
		}
	}
}
