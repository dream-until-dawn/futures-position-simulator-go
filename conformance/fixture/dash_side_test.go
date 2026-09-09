package fixture

import (
	"sort"
	"testing"
)

// dashOnEmptySideFields 是 kq_facts 13 点名的那三个字段，逐方向。
var dashOnEmptySideFields = []string{"open_price", "position_price", "margin"}

// TestEmptySideGivesDashNotZero 给 kq_facts 13 补上守卫。
//
// # 那条事实此前只有一个**冻结的计数**
//
// state.md 把它写成「**188 份持仓截面，188/188 无反例**，
// 是本表里唯一一条干净的全称命题」。⚠️ 而 188 是 2026-09-08 的语料规模，
// 今天是 466 条持仓记录 —— 也就是说：
//
//	这条**被点名为最干净的全称命题**，恰恰是没有机制去复核的那一条。
//	新语料上出现反例，不会有任何动静：文档里的 188/188 永远是 188/188。
//
// ⚠️ 「唯一一条干净的全称命题」这个说法本身，就是它最该有守卫的理由。
//
// # 两个方向都要断言
//
// 只断言「空仓方向给 "-"」是**空转的**：一个所有方向都给 "-" 的语料
// 也会全绿，而那时这条事实什么也不区分。所以反过来那一半同样要钉住：
// **有仓的方向必须给数值**。
//
// ⚠️ 不许把这条推广到成本字段（`open_cost_*` / `position_cost_*` /
// `margin_*_today` 之类）—— 那些字段上 "-" 跟的是**今昨侧**，
// 与有没有仓无关，见 kq_facts 50 与 TestCostSplitAbsentSideFollowsPositionDateType。
func TestEmptySideGivesDashNotZero(t *testing.T) {
	emptyCells, heldCells := 0, 0
	var bad []string
	for _, f := range loadAll(t) {
		syms := make([]string, 0, len(f.Positions))
		for sym := range f.Positions {
			syms = append(syms, sym)
		}
		sort.Strings(syms)
		for _, sym := range syms {
			p := f.Positions[sym]
			for _, side := range []string{"long", "short"} {
				vol, ok := numberOf(p, "volume_"+side)
				if !ok {
					continue
				}
				held := vol.Sign() > 0
				for _, base := range dashOnEmptySideFields {
					v, ok := p[base+"_"+side]
					if !ok {
						continue
					}
					switch {
					case !held:
						emptyCells++
						if !v.Absent {
							bad = append(bad, f.Path+" "+sym+" "+base+"_"+side+
								" 空仓方向却不是 \"-\"（读成了 "+v.Number.String()+"）")
						}
					default:
						heldCells++
						if v.Absent {
							bad = append(bad, f.Path+" "+sym+" "+base+"_"+side+
								" **有仓**却给了 \"-\"")
						}
					}
				}
			}
		}
	}
	for _, b := range bad {
		t.Errorf("⚠️ %s", b)
	}
	// ⚠️ 两个下界缺一不可，理由与 TestDashParsesAsAbsentNotZero 同形：
	// 没有空仓格，上面那半永远绿；没有有仓格，下面那半永远绿。
	if emptyCells < 100 {
		t.Fatalf("⚠️ 只数出 %d 个空仓方向的格子 —— 这条断言在空转", emptyCells)
	}
	if heldCells < 20 {
		t.Fatalf("⚠️ 只数出 %d 个**有仓**方向的格子 —— "+
			"少了它，「空仓给 \"-\"」与「这三个字段永远给 \"-\"」分不开", heldCells)
	}
	t.Logf("空仓方向 %d 格全部是 \"-\"、有仓方向 %d 格全部是数值 —— "+
		"kq_facts 13 在**当前全语料**上仍然无反例（文档里那个 188 是 20260908 的规模）",
		emptyCells, heldCells)
}
