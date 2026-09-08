package fixture

import (
	"fmt"
	"sort"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
)

// costSplitFields 是持仓成本的今昨拆分四格，按 (今昨侧, 方向) 排。
var costSplitFields = struct {
	today [2]string
	his   [2]string
}{
	today: [2]string{"position_cost_long_today", "position_cost_short_today"},
	his:   [2]string{"position_cost_long_his", "position_cost_short_his"},
}

// TestCostSplitAbsentSideFollowsPositionDateType 钉住持仓成本今昨拆分的四格形状。
//
// # 它更正了 kq_facts 28 的一句措辞
//
// 第 28 条写的是「而空的那一侧给 `"-"`」。⚠️ **那个判据是错的** ——
// 语料里 `SHFE.rb2701` 有一份截面是「多今1/多昨2/**空今1**」，
// 空头方向**有量**，而它的 `position_cost_short_today` 照样是 `"-"`；
// 同一条记录的 `position_cost_short_his` 无量却是数值 `0`。
//
//	"-" 跟的是**今昨侧**，不是**有没有仓**。
//
// 两个判据在此前的语料上给出完全相同的预测，因为此前
// 「空的方向」与「不被写的那一侧」恰好总是重合。
//
// # 实际的形状（20260909 全语料 466 条持仓记录，无反例）
//
//	未经历结算   四格全是数值 0，连 "-" 都不出现
//	经历过结算   由 PositionDateType 挑一侧写，**另一侧两个方向都给 "-"**
//	             UseHistory   → 写 _his，_today 两格是 "-"
//	             NoUseHistory → 写 _today，_his 两格是 "-"
//	             被写的那一侧，该方向没仓就是数值 0
//
// # 为什么要有第三段断言
//
// 前两段单独成立时是**空转的**：语料里若从来没有「有量却给 `"-"`」
// 的记录，那么「跟今昨侧」与「跟有没有仓」两个判据仍然分不开，
// 而测试照样全绿。⚠️ 于是第三段要求那种记录**确实出现过** ——
// 它是这条守卫与被它更正的那个错误判据之间**唯一**的分界。
func TestCostSplitAbsentSideFollowsPositionDateType(t *testing.T) {
	split, flat, discriminating := 0, 0, 0
	for _, f := range loadAll(t) {
		syms := make([]string, 0, len(f.Positions))
		for sym := range f.Positions {
			syms = append(syms, sym)
		}
		sort.Strings(syms)

		for _, sym := range syms {
			p := f.Positions[sym]
			todayAbsent, todayOK := sideAbsent(p, costSplitFields.today)
			hisAbsent, hisOK := sideAbsent(p, costSplitFields.his)
			if !todayOK || !hisOK {
				continue // 该份夹具没有这组字段
			}

			switch {
			case !todayAbsent && !hisAbsent:
				// 未经历结算：四格都该是数值 0。
				flat++
				for _, name := range append(costSplitFields.today[:], costSplitFields.his[:]...) {
					if v := p[name]; !v.Number.IsZero() {
						t.Errorf("⚠️ %s %s：四格都不是 \"-\"（未经结算态），"+
							"但 %s = %s 非零 —— 「没写拆分」与「写了拆分」"+
							"在这里就分不开了", f.Path, sym, name, v.Number)
					}
				}

			case todayAbsent != hisAbsent:
				split++
				want := refdata.UseHistory
				absentSide := "_today"
				if hisAbsent {
					want, absentSide = refdata.NoUseHistory, "_his"
				}
				if got := positionDateOf(t, sym); got != want {
					t.Errorf("⚠️ %s %s：给 \"-\" 的是 %s 侧，据此该合约应是 %v，"+
						"而实测的 PositionDateType 是 %v —— 二者必须一致，"+
						"否则 kq_facts 28「拆分写哪一侧由 PositionDateType 定」不成立",
						f.Path, sym, absentSide, want, got)
				}
				// 第三段：这条记录是否**有量却给 "-"**。
				//
				// ⚠️ 比的必须是**同一个今昨侧**的手数。拿总手数比会虚高：
				// rb2701 结算后 volume_long=3 全是昨仓，它的 _today 格给 "-"
				// 是「今仓侧确实没量」，**判别不出任何东西**，而按总手数
				// 它会被记成一条判别样本（实测 24 → 118，四倍多）。
				for i := range absentNames(todayAbsent) {
					if volumeOnSide(p, i, todayAbsent) > 0 {
						discriminating++
					}
				}

			default:
				t.Errorf("⚠️ %s %s：今昨两侧**同时**给 \"-\" —— "+
					"那样就没有任何一侧承载拆分值了，与 kq_facts 28 矛盾",
					f.Path, sym)
			}
		}
	}

	if split == 0 {
		t.Fatal("⚠️ 全语料没有一条**经历过结算**的拆分记录 —— " +
			"这条守卫因此什么也没验，而它会显示为绿")
	}
	if discriminating == 0 {
		t.Fatal("⚠️ 语料里没有任何一条「该方向**有量**、而它在不被写的那一侧给 \"-\"」" +
			"的记录 —— 少了它，「\"-\" 跟今昨侧」与 kq_facts 28 原来那句" +
			"「\"-\" 跟有没有仓」**给出相同预测**，这条守卫就是空转的")
	}
	t.Logf("持仓成本今昨拆分：未结算态 %d 条、已结算拆分 %d 条，"+
		"其中「有量却给 \"-\"」%d **格**（判别用；格 = 记录 × 方向）", flat, split, discriminating)
}

// sideAbsent 判一个今昨侧的两格是不是都给了 "-"。
//
// ⚠️ 它要求两个方向**一致**：一侧只有一个方向给 "-" 的话，
// 「按今昨侧」这个判据本身就错了，那要当场报出来而不是各判各的。
func sideAbsent(p map[string]Value, names [2]string) (absent, ok bool) {
	a, okA := p[names[0]]
	b, okB := p[names[1]]
	if !okA || !okB {
		return false, false
	}
	if a.Absent != b.Absent {
		return false, false
	}
	return a.Absent, true
}

func absentNames(todayAbsent bool) [2]string {
	if todayAbsent {
		return costSplitFields.today
	}
	return costSplitFields.his
}

// volumeOnSide 取方向 i（0 多 1 空）在某个今昨侧上的持仓量。
//
// ⚠️ 侧别是参数而不是省略项：这个函数唯一的用处是回答
// 「给了 "-" 的**那一格**对应的手数是不是非零」，
// 而拿总手数去答那个问题**看起来对且数更大**。
func volumeOnSide(p map[string]Value, i int, todaySide bool) int64 {
	name := "volume_long"
	if i == 1 {
		name = "volume_short"
	}
	if todaySide {
		name += "_today"
	} else {
		name += "_his"
	}
	v, ok := p[name]
	if !ok || v.Absent {
		return 0
	}
	return v.Number.IntPart()
}

// TestShortHistoryCostHasNeverBeenObserved 是一条**会在采样成功那天变红**的绊线。
//
// # 它钉住的是一个洞，不是一条规则
//
// 全语料 466 条持仓记录里，`volume_short_his` / `volume_short_yd` /
// `pos_short_his` 三者 **>0 的次数各为 0** —— 空头昨仓从来没有存在过。
// 而空头**今**仓是有的（27 条）。于是：
//
//	kq_facts 28/33 那条「拆分在结算时按 PositionDateType 写一侧」，
//	**全部非零证据都在多头侧**。空头侧一次都没有贡献过非零观测。
//
// ⚠️ 因此「这条规则是方向中性的」与「柜台只在多头侧写这个拆分」
// **在当前语料上完全分不开** —— 而本库建的是前者。
//
// # 这条绊线红了要做什么
//
// 红了说明**那次采样成功了**：`position_cost_short_his` 第一次拿到了值。
// 该做的是把观测写进 `kq_facts`，判定上面那个二选一，然后**删掉这条绊线**。
//
// ⚠️ 不是把它改绿。改绿它只需要在下一行加个例外，而那样就把
// 「第一次量到」这件事变成了一次静默的常量修改。
func TestShortHistoryCostHasNeverBeenObserved(t *testing.T) {
	var hits []string
	for _, f := range loadAll(t) {
		for sym, p := range f.Positions {
			for _, name := range []string{
				"volume_short_his", "volume_short_yd", "pos_short_his",
				"volume_short_frozen_his",
			} {
				if v, ok := p[name]; ok && !v.Absent && v.Number.Sign() > 0 {
					hits = append(hits, fmt.Sprintf("%s %s %s=%s",
						f.Path, sym, name, v.Number))
				}
			}
			if v, ok := p["position_cost_short_his"]; ok && !v.Absent && v.Number.Sign() > 0 {
				hits = append(hits, fmt.Sprintf("%s %s position_cost_short_his=%s",
					f.Path, sym, v.Number))
			}
		}
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("⚠️ **空头昨仓第一次出现了** —— 这条绊线红了不是坏消息，"+
			"是那次采样成功了：\n  %v\n"+
			"该做的是：把观测写进 kq_facts，判定「拆分是方向中性的」还是"+
			"「柜台只在多头侧写」，然后**删掉这条绊线**（不是给它加例外）",
			hits)
	}
	t.Log("空头昨仓仍是零观测：kq_facts 28/33 的非零证据**全部在多头侧**，" +
		"「方向中性」与「只写多头侧」尚未分开")
}
