package fixture

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestOrderFrozenMarginShape 钉住委托记录里 `frozen_margin` 的**形状**。
//
// # 实测（20260909，431 条开仓委托 + 404 条平仓委托）
//
//	开仓 · 被柜台接受且尚未成交   有 frozen_margin，非零   60/60
//	开仓 · 被拒                  **整个字段缺席**         339/339
//	开仓 · 已成交                **整个字段缺席**         32/32
//	平仓 · 任何状态              **整个字段缺席**         404/404
//
// 一致的解释：字段在委托**进簿**时出现，**成交时被清掉**（冻结转成持仓保证金），
// **撤单后留下最后那个值**。被拒的单从来没进过簿，所以从来没有过这个字段。
//
// ⚠️ 「缺席」与「0」在这里是两句话：缺席是柜台在说「这笔单没有冻结这回事」，
// 0 是「有这回事，值是零」。本仓库一贯把两者分开（Value.Absent / kq.Num）。
//
// # ⚠️ 它更正了一句写在白名单里的话
//
// orderKeep 里 frozen_margin 的注释原写「实测**柜台不发这个字段**
// （20260909 三份样本里都缺席）」。⚠️ 那三份样本**全是平仓单** ——
// 从「平仓单上没有」推出了「柜台不发」。
//
// > **从一类样本上的缺席，推不出这个字段不存在。**
//
// # ⚠️ 还有一个陷阱，一并钉住
//
// **已终结（撤掉）的委托上，frozen_margin 仍是非零的那个历史值。**
// 它是一枚戳记，不是当前占用。把委托记录里的 frozen_margin 直接加起来，
// 会把早已撤掉的单算进冻结 —— 而那个和看起来完全合理，只是偏大。
// FrozenAccountOf 只数还挂着的委托，正是为此。
func TestOrderFrozenMarginShape(t *testing.T) {
	type bucket struct{ with, without int }
	var openAccepted, openRejected, openTraded, closed bucket
	staleStamp := 0
	for _, f := range loadAll(t) {
		for id, o := range f.Orders {
			_, off, err := dirOffsetOf(o)
			if err != nil {
				continue
			}
			v, has := o["frozen_margin"]
			b := &closed
			if off == types.Open {
				// ⚠️ 判「被拒」用拒因文案非空。本批语料里撤单不带文案，
				// 所以这个判据成立；换语料要重新想。
				rejected := false
				if m, ok := o["last_msg"]; ok && m.IsText && m.Text != "" {
					rejected = true
				}
				orig, ok1 := numberOf(o, "volume_orign")
				left, ok2 := numberOf(o, "volume_left")
				traded := ok1 && ok2 && left.LessThan(orig)
				switch {
				case rejected:
					b = &openRejected
				case traded:
					b = &openTraded
				default:
					b = &openAccepted
				}
			}
			if has {
				b.with++
			} else {
				b.without++
			}
			if b == &openAccepted && has {
				if v.Absent || v.IsText || !v.Number.IsPositive() {
					t.Errorf("⚠️ %s 的开仓委托 %s 进了簿，frozen_margin 却是 %v —— "+
						"开仓要冻保证金，非正只可能是没算", f.Path, id, v)
				}
				if s, ok := o["status"]; ok && s.IsText && s.Text != "ALIVE" {
					staleStamp++
				}
			}
		}
	}
	t.Logf("开仓·进簿未成交 带 %d/缺 %d；开仓·被拒 带 %d/缺 %d；"+
		"开仓·已成交 带 %d/缺 %d；平仓 带 %d/缺 %d；已终结却仍带戳记 %d",
		openAccepted.with, openAccepted.without, openRejected.with, openRejected.without,
		openTraded.with, openTraded.without, closed.with, closed.without, staleStamp)

	for _, c := range []struct {
		name string
		b    bucket
		want string // "all" 全都要有；"none" 全都不该有
	}{
		{"开仓·进簿未成交", openAccepted, "all"},
		{"开仓·被拒", openRejected, "none"},
		{"开仓·已成交", openTraded, "none"},
		{"平仓·任何状态", closed, "none"},
	} {
		if c.want == "all" && c.b.without > 0 {
			t.Errorf("⚠️ %s 里有 %d 条**没有** frozen_margin —— "+
				"实测这一类都带着它。柜台改了口径，还是白名单漏了？", c.name, c.b.without)
		}
		if c.want == "none" && c.b.with > 0 {
			t.Errorf("⚠️ %s 里有 %d 条**带着** frozen_margin —— "+
				"实测这一类整个字段缺席。⚠️ 缺席与 0 是两句话，"+
				"出现了就去更新 kq_facts 49", c.name, c.b.with)
		}
		// ⚠️ 每一类都要有样本，否则那一行断言是空真。
		if c.b.with+c.b.without == 0 {
			t.Errorf("⚠️ 「%s」一条样本都没有 —— 这一行什么都没断言", c.name)
		}
	}
	// ⚠️ 那个陷阱也要有样本，否则注释里那句警告没人验过。
	if staleStamp == 0 {
		t.Error("⚠️ 没有「已终结却仍带着非零 frozen_margin」的样本 —— " +
			"注释里「它是一枚戳记不是当前占用」那句现在没有语料支撑了")
	}
}
