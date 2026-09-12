package main

import "testing"

// TestHoldVerdictRefusesBeforeItConcludes 钉住 `ctp-hold` 判定的**优先级**。
//
// ⚠️ 它是 20260911 夜盘那次假结论的正面补丁：当时账上有 ag2701 多与 ag2702 空
// 两条腿，而这段判据默认「只有这一条腿、只有价格在动」，于是照常打印
// 「占用保证金变了 ⇒ 基准是某个动态价，开仓价被否」。
//
//	⚠️ **判据没写错，错在前提被违反** —— 而它不会说「我的前提不成立了」。
//
// 这里逐一钉住四种「不该下结论」的情形各自被认了出来，
// **并且钉住它们排在真结论之前**：把顺序倒过来，
// 前三种情形会各自得到一个看起来完全正常的结论。
func TestHoldVerdictRefusesBeforeItConcludes(t *testing.T) {
	cases := []struct {
		name                         string
		legs                         int
		first, last, firstPx, lastPx float64
		want                         holdKind
	}{
		// ⚠️ 头一条正是那次假结论的现场：数值上「变了」，而账上有两条腿。
		// 旧判据在这组输入上会给 holdDynamic。
		{"两条腿时不下判断（那次假结论的现场）", 2, 45357.75, 45372.00, 8800, 8800, holdNoPremise},
		{"两条腿 + 行情也动了，仍然不下判断", 2, 100, 200, 3100, 3105, holdNoPremise},
		{"一轮都没读到今仓", 1, 0, 0, 3100, 3105, holdNoData},
		{"行情全程没动 —— 两个候选同值", 1, 5000, 5000, 3100, 3100, holdNoMove},
		{"连行情都没读到", 1, 5000, 5000, 0, 0, holdNoMove},
		{"行情动了而保证金不变 ⇒ 开仓价", 1, 5000, 5000, 3100, 3105, holdStatic},
		{"行情动了保证金也变 ⇒ 动态价", 1, 5000, 5010, 3100, 3105, holdDynamic},
	}
	for _, c := range cases {
		got := holdVerdict(c.legs, c.first, c.last, c.firstPx, c.lastPx)
		if got != c.want {
			t.Errorf("%s：得到 %d，要的是 %d", c.name, got, c.want)
		}
	}

	// ⚠️ 反空转：五种取值**每一种都要被上面这组用例覆盖到**。
	// 一张只覆盖三种的表，与一张覆盖全的表，在全绿时长得一模一样。
	seen := map[holdKind]bool{}
	for _, c := range cases {
		seen[c.want] = true
	}
	for k, name := range map[holdKind]string{
		holdNoPremise: "holdNoPremise", holdNoData: "holdNoData",
		holdNoMove: "holdNoMove", holdStatic: "holdStatic", holdDynamic: "holdDynamic",
	} {
		if !seen[k] {
			t.Errorf("⚠️ %s 一条用例都没有 —— 那一支从没被走到过", name)
		}
	}
}
