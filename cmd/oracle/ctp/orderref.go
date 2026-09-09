package ctp

import (
	"strconv"
	"strings"
)

// ⚠️ 本文件**不带 build tag**：报单引用的格式与平台无关，
// 而把它留在 `_windows.go` 里意味着它的守卫只在一种机器上跑。

// formatOrderRef 把序号变成柜台能读的报单引用。
//
// ⚠️ **必须是纯数字**。这不是风格问题：CTP 把 OrderRef 当数字看，
// 带字母前缀的引用它读不出来，于是**同一会话里每一笔都被当成同一个引用**，
// 第二笔起一律 `ErrorID=22 不允许重复报单`。
//
// 20260910 夜盘用过 `p%09d`。它「看起来」是对的 —— 单调、不重复、
// 第一笔单永远成功。⚠️ **错只在第二笔单上暴露**，而当时每个实验都只发一笔。
// 见 probes.md §6.8。
func formatOrderRef(n int64) string { return strconv.FormatInt(n, 10) }

// parseMaxOrderRef 读登录应答里的「最大报单引用」。
//
// 第二个返回值是**读懂了没有**，不是「是不是零」——⚠️ 两者要人做的事不同：
// 读不懂要打日志（柜台给了个没见过的形状），读懂了是 0 不必。
func parseMaxOrderRef(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
