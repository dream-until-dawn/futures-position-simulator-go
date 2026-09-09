package fixture

import (
	"testing"

	"github.com/shopspring/decimal"
)

// tickFrac 返回价格偏离最近一个「更小的整数倍」的零头，单位是**一个 tick**。
//
//	0        正好是整数倍
//	(0,1)    偏离多少个 tick 的几分之几
func tickFrac(price, tick decimal.Decimal) decimal.Decimal {
	if !tick.IsPositive() {
		return decimal.Zero
	}
	n := price.Div(tick).Floor()
	return price.Sub(n.Mul(tick)).Div(tick)
}

// orderFact 是从夹具里读出来的一笔委托，够判「违反了哪几项」。
type orderFact struct {
	where string
	frac  decimal.Decimal
	over  bool
	msg   string
}

// collectOrderFacts 把全部夹具里**能判定**的委托收出来。
//
// ⚠️ 判不了的一律跳过而不是猜：缺规格判不了整数倍、
// 同一份夹具里没有该合约的行情就判不了越界。
func collectOrderFacts(t *testing.T) []orderFact {
	t.Helper()
	specs := loadSpecsForTest(t)
	var out []orderFact
	for _, f := range loadAll(t) {
		for id, o := range f.Orders {
			sym, ok := textOf(o, "exchange_id", "instrument_id")
			if !ok {
				continue
			}
			sp, ok := specs[sym]
			if !ok || !sp.PriceTick.IsPositive() {
				continue
			}
			price, ok := numberOf(o, "limit_price")
			if !ok {
				continue
			}
			up, okU := numberOf(f.Quotes[sym], "upper_limit")
			low, okL := numberOf(f.Quotes[sym], "lower_limit")
			if !okU || !okL {
				continue
			}
			msg := ""
			if v, ok := o["last_msg"]; ok && v.IsText {
				msg = v.Text
			}
			out = append(out, orderFact{
				where: f.Path + " " + id,
				frac:  tickFrac(price, sp.PriceTick),
				over:  price.GreaterThan(up) || price.LessThan(low),
				msg:   msg,
			})
		}
	}
	return out
}

// TestCounterTickToleranceIsHalfATick 钉住一条**反直觉**的实测边界。
//
// # 事实
//
// 快期模拟只在价格零头 **小于半个 tick** 时报「不是价格单位的整倍数」；
// 零头 ≥ 半个 tick 的价格它**照单全收**（价内的直接挂上去，
// 委托记录里留着那个带零头的价）。20260909 在 SHFE.ag2702（tick=1）与
// DCE.i2701（tick=0.5）上各扫了一遍零头，边界都落在半个 tick 上 ——
// 所以它随 tick 缩放，不是某个绝对数。
//
// # 为什么这条值得单独钉
//
// ⚠️ 它让我翻过一次案。原先我拿「涨停价 × 1.05」构造「同时违反两项」的单子，
// 那个价的零头是 0.7 个 tick —— **柜台根本不认为它偏离**，
// 于是那笔单只违反了涨跌停一项。我据此把 order 的两项优先级对调了，
// 而对调之后全套测试**照样绿**（那时优先级还没有任何守卫）。
//
// ⚠️ 更该记住的是：实验里那层「违规真的构造出来了吗」的零层核对**没拦住**，
// 因为它用的是**本库的**判据 —— 而分歧恰恰在判据本身。
// 一个用自己的定义去核对自己的守卫，对「定义分歧」没有判别力。
func TestCounterTickToleranceIsHalfATick(t *testing.T) {
	facts := collectOrderFacts(t)
	half := decimal.RequireFromString("0.5")
	below := map[string][]string{} // 零头 < 半 tick 且被拒
	atOrAbove := map[string][]string{}
	accepted := 0
	for _, f := range facts {
		if f.frac.IsZero() {
			continue // 正好整数倍，与这条边界无关
		}
		if f.msg == "" {
			accepted++ // 没有拒因 —— 柜台收下了
			if f.frac.LessThan(half) {
				t.Errorf("⚠️ %s 的零头是 %s 个 tick（< 半个），柜台却没有拒因 —— "+
					"半 tick 这条边界在这一笔上不成立", f.where, f.frac)
			}
			continue
		}
		if f.frac.LessThan(half) {
			below[f.msg] = append(below[f.msg], f.where)
		} else {
			atOrAbove[f.msg] = append(atOrAbove[f.msg], f.where)
		}
	}
	t.Logf("零头 < 半 tick 且被拒：%d 种原话；零头 ≥ 半 tick 且被拒：%d 种；被收下的 %d 笔",
		len(below), len(atOrAbove), accepted)

	// ⚠️ 判别力：三组都要有样本，否则下面的「不相交」是空真。
	if len(below) == 0 || len(atOrAbove) == 0 || accepted == 0 {
		t.Skipf("⚠️ 样本不全（< 半 tick 被拒 %d 种 / ≥ 半 tick 被拒 %d 种 / 被收下 %d 笔）—— "+
			"这条边界要三组都有才说明得了", len(below), len(atOrAbove), accepted)
	}
	// 零头 < 半 tick 的一律报同一句：那一项**盖过**了同一笔单上的其它违规。
	if len(below) != 1 {
		t.Errorf("⚠️ 零头 < 半 tick 的委托报了 %d 种原话 %v —— "+
			"原先的结论是「那一项盖过其它一切」，现在不成立了", len(below), sortedKeys(below))
	}
	for m, where := range below {
		if w, dup := atOrAbove[m]; dup {
			t.Errorf("⚠️ 原话 %q 在零头 < 半 tick（%v）和 ≥ 半 tick（%v）两侧都出现 —— "+
				"半 tick 这条边界分不开两组", m, where, w)
		}
	}
}

// TestRejectPriorityFromFixtures 从入库夹具里读出**最小变动价位优先于涨跌停**。
//
// 判据：同一批委托里
//
//	只越界（零头为 0）        → 一句原话
//	只偏离（零头 < 半 tick，价内）→ 另一句
//	两样都占                  → 与「只偏离」那句**相同**
//
// ⚠️ 逐字比，不认关键字。关键字匹配会把断言变成「我猜柜台怎么措辞」，
// 措辞一改，一条错误的结论会继续绿着。
func TestRejectPriorityFromFixtures(t *testing.T) {
	facts := collectOrderFacts(t)
	half := decimal.RequireFromString("0.5")
	tickOnly := map[string][]string{}
	limitOnly := map[string][]string{}
	both := map[string][]string{}
	for _, f := range facts {
		if f.msg == "" {
			continue
		}
		offTick := f.frac.IsPositive() && f.frac.LessThan(half)
		switch {
		case offTick && f.over:
			both[f.msg] = append(both[f.msg], f.where)
		case offTick:
			tickOnly[f.msg] = append(tickOnly[f.msg], f.where)
		case f.over:
			limitOnly[f.msg] = append(limitOnly[f.msg], f.where)
		}
	}
	t.Logf("只偏离 %d 种原话；只越界 %d 种；两样都占 %d 种",
		len(tickOnly), len(limitOnly), len(both))
	if len(tickOnly) == 0 || len(limitOnly) == 0 || len(both) == 0 {
		t.Skipf("⚠️ 样本不全（只偏离 %d / 只越界 %d / 都占 %d）—— 三组齐了才判得了先后",
			len(tickOnly), len(limitOnly), len(both))
	}
	if len(tickOnly) != 1 || len(both) != 1 {
		t.Errorf("⚠️ 「只偏离」%d 种、「两样都占」%d 种 —— 每组必须只有一句，否则混进了别的拒因",
			len(tickOnly), len(both))
		return
	}
	for m, where := range both {
		if _, same := tickOnly[m]; !same {
			t.Errorf("⚠️ 两样都占时报的是 %q（%v），而「只偏离」报的是 %v —— "+
				"那意味着涨跌停被优先报出，与 order.CheckPriceTick 排在 "+
				"order.CheckPriceLimit 前面**相反**", m, where, sortedKeys(tickOnly))
		}
		if _, clash := limitOnly[m]; clash {
			t.Errorf("⚠️ 两样都占时报的原话 %q 同时也是「只越界」那一组的原话 —— "+
				"两把标尺一样长，这一组分不出先后", m)
		}
	}
}
