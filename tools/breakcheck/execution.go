package main

import (
	"os"
	"strings"
)

type breakFileIO struct {
	read  func(string) ([]byte, error)
	write func(string, []byte, os.FileMode) error
}

// visitBreaks 统一正常验证和只编译模式的执行顺序，记录由调用方负责。
func visitBreaks(breaks []Break, only string, visit func(Break) string) (bool, int) {
	var selected []Break
	for _, b := range breaks {
		if only == "" || strings.Contains(b.Name, only) {
			selected = append(selected, b)
		}
	}
	for i, b := range selected {
		if visit(b) == "⚠️ 还原失败" {
			return true, len(selected) - i - 1
		}
	}
	return false, 0
}
