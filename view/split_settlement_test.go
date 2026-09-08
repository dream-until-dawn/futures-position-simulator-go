package view_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestPositionCostSplitIsFrozenWithinATradingDay 断言 `position_cost_*` 的今昨拆分
// **在一个交易日之内一动不动**，哪怕手数在动。
//
// ⚠️ 这条是 20260909 夜盘换来的，而它替掉的那条断言更弱也更错。
//
// 原来的断言是「有量的那一侧必须是数字」。那晚在 rb2701 上开了一手空头今仓，
// 而 `position_cost_short_today` 给的是 `"-"` —— 有量却没值，当场红了 7 处。
// 顺着查下去才看清：**这个拆分是日终结算时算出来的，盘中根本不更新**。
//
// 于是判据要换个地方落：不是「有量就该有值」，而是**「值不随盘中成交变」**。
// 后者才是「结算时写的」这句话的可证伪形式。
//
// # ⚠️ 判别力从哪来
//
// 一个恒定的值，在手数也恒定的样本上是**平凡成立**的。所以本条同时要求：
// 至少有一组 (交易日, 合约, 方向) 的**手数确实变过**，而拆分没变。
// 没有这样一组时直接判失败 —— 那时这条测试什么都没验。
//
// # ⚠️ 顺带的坑
//
// 有盘中成交时 `_today + _his ≠ position_cost`。实测 rb2701：
// 合计 126970 = 95310（昨 3 手 × 3177）+ 31660（今 1 手 × 开仓价 3166），
// 而拆分只给得出前一项。拿拆分去凑合计的人会差一整块，且不报错。
func TestPositionCostSplitIsFrozenWithinATradingDay(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "testdata", "probes", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ day, sym, side, which string }
	splits := map[key]map[string]bool{} // 拆分值的取值集合（含 "-"）
	vols := map[key]map[float64]bool{}  // 同一组里手数的取值集合

	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			TradingDay string                    `json:"trading_day"`
			Positions  map[string]map[string]any `json:"positions"`
		}
		if json.Unmarshal(b, &f) != nil || f.TradingDay == "" {
			continue
		}
		for sym, pos := range f.Positions {
			for _, side := range []string{"long", "short"} {
				today, _ := pos["volume_"+side+"_today"].(float64)
				his, _ := pos["volume_"+side+"_his"].(float64)
				for _, w := range []string{"today", "his"} {
					k := key{f.TradingDay, sym, side, w}
					if splits[k] == nil {
						splits[k] = map[string]bool{}
						vols[k] = map[float64]bool{}
					}
					splits[k][fmt.Sprint(pos["position_cost_"+side+"_"+w])] = true
					vols[k][today+his] = true
				}
			}
		}
	}
	if len(splits) == 0 {
		t.Fatal("⚠️ 一组样本都没收集到 —— 本条在空集上跑，那会全绿")
	}

	// ⚠️ 判别力：必须有一组「手数变过」的，否则「拆分不变」平凡成立。
	discriminating := 0
	var bad []string
	for k, vals := range splits {
		volChanged := len(vols[k]) > 1
		if volChanged {
			discriminating++
		}
		if len(vals) > 1 {
			names := make([]string, 0, len(vals))
			for v := range vals {
				names = append(names, v)
			}
			sort.Strings(names)
			bad = append(bad, fmt.Sprintf(
				"%s %s.position_cost_%s_%s 在同一个交易日内取过 %v（手数变过：%t）",
				k.day, k.sym, k.side, k.which, names, volChanged))
		}
	}
	sort.Strings(bad)
	for _, s := range bad {
		t.Errorf("⚠️ %s —— "+
			"拆分在交易日内变了，那与「结算时算出来、盘中不更新」矛盾。"+
			"**先查清楚是不是跨了一次结算**（同一个 trading_day 里不该有第二次结算）", s)
	}
	if discriminating == 0 {
		t.Fatal("⚠️ 没有任何一组的手数在交易日内变过 —— " +
			"「拆分不随成交变」此时是平凡成立的，本条什么都没验。" +
			"要一份「先拍一张、再成交、再拍一张」的样本")
	}
	t.Logf("%d 组 (交易日,合约,方向,今昨)，其中手数变过的 %d 组；拆分取值不唯一的 %d 组",
		len(splits), discriminating, len(bad))
}
