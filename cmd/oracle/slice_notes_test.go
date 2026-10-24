package main

import (
	"strings"
	"testing"
)

// TestSliceStageNotes 阶段文字用于人读，必须与枚举标识符表示的前后顺序一致。
func TestSliceStageNotes(t *testing.T) {
	for _, c := range []struct {
		stage        sliceStage
		want, refuse string
	}{
		{stageAfterLeg1, "①", "②"},
		{stageBeforeClose, "平仓之**前**", "平仓之**后**"},
		{stageAfterClose, "平仓之**后**", "平仓之**前**"},
	} {
		text := c.stage.note()
		if !strings.Contains(text, c.want) || strings.Contains(text, c.refuse) {
			t.Fatalf("阶段注记语义错误：%v %s", c.stage, text)
		}
	}
	if !strings.Contains(sliceStage(255).note(), "认不得") {
		t.Fatal("未知阶段被写成有效阶段")
	}
}
