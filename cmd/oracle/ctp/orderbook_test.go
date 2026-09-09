package ctp

import "testing"

// TestOrderBookResetClearsTerminalState 钉住「发单前必须清簿」。
//
// ⚠️ 它防的不是一个整洁问题，是一次**假结果**：20260910 夜盘 `ctp-dup`
// 第 4 格重用了第 1 格的 ref，而第 1 格已经撤单落在终态上 ——
// 于是 `Insert` 的等待条件（簿上出现终态）**在发单那一刻就已经成立**，
// 它秒回「已撤单」，报告了一个根本没发生过的结果，那一格白测了。
//
// **「上一笔的结局」被当成了「这一笔的结局」。**
func TestOrderBookResetClearsTerminalState(t *testing.T) {
	var b orderBook
	b.reset("7", 1)
	b.put("7", func(s *OrderState) { s.Status, s.StatusMsg, s.VolumeTraded = '5', "已撤单", 0 })
	if s, _ := b.get("7"); !settledForTest(s.Status) {
		t.Fatalf("前置条件不成立：'5' 应当是终态，否则本用例什么都没验")
	}

	b.reset("7", 2) // 同一个 ref 再发一笔
	s, ok := b.get("7")
	if !ok {
		t.Fatal("清簿之后这一格应当还在（只是内容清空），否则 Insert 会等到超时")
	}
	if settledForTest(s.Status) || s.StatusMsg != "" || s.VolumeTraded != 0 {
		t.Fatalf("清簿之后仍带着上一笔的结局 status=%q msg=%q 已成交=%d —— "+
			"⚠️ **「上一笔的结局」会被当成「这一笔的结局」**，"+
			"Insert 会在发单那一刻就返回一个没发生过的结果",
			string(s.Status), s.StatusMsg, s.VolumeTraded)
	}
	if s.VolumeTotal != 2 {
		t.Fatalf("清簿之后委托量该是新的 2，拿到 %d", s.VolumeTotal)
	}
}

// settledForTest 只判「是不是终态」，⚠️ 刻意不复用 settled():
// 那个函数是 windows 专属，而本用例要在两个平台都跑。
// 这里只需要「'5' 是终态、零值不是」这一点。
func settledForTest(st byte) bool { return st != 0 }
