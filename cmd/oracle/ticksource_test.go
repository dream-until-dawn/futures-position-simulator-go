package main

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// TestTickVerdict 钉住「柜台报的 tick」与「人手填的 tick」之间的四档判定。
//
// ⚠️ 判别力在**不等那一档**上：那正是 20260914 夜盘真的发生过的事
// （`bc` 的 tick 是 10，命令里填了 1），而当时没有任何东西喊 ——
// 命令照样跑完四条、照样拿到四个码，事后靠人重读命令行才发现。
func TestTickVerdict(t *testing.T) {
	for _, c := range []struct {
		name            string
		counter, flag   float64
		wantValue       float64
		wantSource      string
		wantErrContains string
	}{
		{"柜台报了、人没填", 10, 0, 10, tickFromCounter, ""},
		{"两个都有且相等", 1, 1, 1, tickFromCounter, ""},
		{"两个都有但不等", 10, 1, 0, "", "一个是错的"},
		{"柜台没报、人填了", 0, 0.1, 0.1, tickFromFlag, ""},
		{"两个都没有", 0, 0, 0, "", "归不到它该归的那一项"},
		{"柜台报了个负数 ⇒ 当作没报", -1, 5, 5, tickFromFlag, ""},
	} {
		tk, why, err := tickVerdict(c.counter, c.flag)
		if c.wantErrContains != "" {
			if err == nil {
				t.Errorf("⚠️ %s 应当拒绝跑，却放行了（tick=%g 出处 %s）", c.name, tk.Value, tk.Source)
			} else if !strings.Contains(err.Error(), c.wantErrContains) {
				t.Errorf("⚠️ %s 拒是拒了，但理由没说到点上（要含 %q）：%v", c.name, c.wantErrContains, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("⚠️ %s 不该报错：%v", c.name, err)
			continue
		}
		if tk.Value != c.wantValue || tk.Source != c.wantSource {
			t.Errorf("⚠️ %s：得到 tick=%g 出处 %q，应为 %g / %q",
				c.name, tk.Value, tk.Source, c.wantValue, c.wantSource)
		}
		if why == "" {
			t.Errorf("⚠️ %s 放行了却没说为什么 —— 这一行是事后复核唯一能看到的东西", c.name)
		}
	}
}

// TestTickVerdictDoesNotSilentlyPreferTheFlag 是「不等那一档」的反面守卫。
//
// ⚠️ 一个「两者不等时**取柜台的**」的实现，会让上面那张表里除不等那一行之外全部通过，
// 而它正是本函数存在的理由的反面：**不等时没有一个可信的选择，只能停下**。
func TestTickVerdictDoesNotSilentlyPreferTheFlag(t *testing.T) {
	for _, c := range [][2]float64{{10, 1}, {1, 10}, {0.1, 0.2}} {
		if _, _, err := tickVerdict(c[0], c[1]); err == nil {
			t.Errorf("⚠️ 柜台 %g / 手填 %g 不等，却选了一个跑下去 —— "+
				"「选谁」这件事本命令没有依据，停下才是对的", c[0], c[1])
		}
	}
}

// TestObserveRecordsTick 钉住语料里记下了用的哪个 tick、出处是谁。
//
// ⚠️ 不记的话，一个手填对了的 tick 与一个柜台报的 tick 在语料里**分不开**，
// 而它们的可信度不同。
func TestObserveRecordsTick(t *testing.T) {
	rc := rejectCase{Name: "低于跌停", Violates: []string{"涨跌停"}}
	o := observe("20260917", "21:05:00", "GFEX", "si2601", rc, ctp.OrderState{StatusMsg: "50:x"}, "rejected",
		tickUsed{Value: 5, Source: tickFromCounter})
	if o.PriceTick == nil || *o.PriceTick != 5 {
		t.Errorf("⚠️ 语料里没记下 tick：%v", o.PriceTick)
	}
	if o.TickSource != tickFromCounter {
		t.Errorf("⚠️ 语料里没记下 tick 的出处：%q", o.TickSource)
	}
	// 零值那一档：没有 tick 就不许伪造一个 0 —— 「没记」与「记成 0」必须分得开。
	z := observe("20260917", "21:05:00", "GFEX", "si2601", rc, ctp.OrderState{StatusMsg: "50:x"}, "rejected", tickUsed{})
	if z.PriceTick != nil {
		t.Errorf("⚠️ 没有 tick 时写进了 %v —— 「没记这一栏」与「tick 是 0」在下游同形", *z.PriceTick)
	}
}
