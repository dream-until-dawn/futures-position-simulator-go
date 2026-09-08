// Package decimalx 是金额与价格的取整口径。
//
// # 为什么它单独成包
//
// `fee` / `margin` / `pnl` 三个纯函数包都要用同一套舍入规则。三处各写各的，
// 就会出现「同一笔钱在两个包里差一分」这种**谁都不报错**的分歧。
//
// # 待实测 5 落在这里
//
// ⚠️ 手续费按金额算出来的几乎必然是无限小数。柜台在哪一步取整、取到几位、
// 四舍五入还是截断，本库**还没有证据**（判别实验 5，见 docs/state.md）。
//
// 所以取整口径是一个**必填参数**，零值报错，不回退到任何「合理的默认」。
// 单笔偏差只有几厘，但它**逐日累加进结存**，长周期回测会漂出可见的量——
// 而漂移的方向始终一致，看起来像策略本身的特性。
package decimalx

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Rounding 是金额的取整口径。
type Rounding uint8

const (
	// RoundingUnmeasured 是零值：判别实验 5 未收敛，使用即报错。
	RoundingUnmeasured Rounding = iota
	// HalfUpToCent 四舍五入到分（两位小数）。
	//
	// ⚠️ 负数按**远离零**舍入（−0.005 → −0.01），与 shopspring 的 Round 一致。
	// 这一点由 TestLibrarySemantics 直接向库求证，不靠假设。
	HalfUpToCent
	// TruncateToCent 截断到分（两位小数）。
	//
	// ⚠️ 负数按**朝零**截断（−0.019 → −0.01），不是向下取整。
	// 两者在负数上差一分，而手续费恒非负、盈亏可正可负——
	// 于是这个区别只在盈亏上显形，且只差一分。
	TruncateToCent
	// NoRounding 不取整，保留完整精度。
	NoRounding
)

// Cents 是「分」的小数位数。
const Cents int32 = 2

func (r Rounding) String() string {
	switch r {
	case HalfUpToCent:
		return "四舍五入到分"
	case TruncateToCent:
		return "截断到分"
	case NoRounding:
		return "不取整"
	}
	return "未实测"
}

// Apply 按取整口径处理一个金额。
func (r Rounding) Apply(v decimal.Decimal) (decimal.Decimal, error) {
	switch r {
	case RoundingUnmeasured:
		return decimal.Zero, fmt.Errorf("金额取整口径未指定：判别实验 5 尚未收敛，"+
			"本库不提供默认值。见 docs/state.md 的 rules_pending。"+
			"（候选：%v / %v / %v）", HalfUpToCent, TruncateToCent, NoRounding)
	case HalfUpToCent:
		return v.Round(Cents), nil
	case TruncateToCent:
		return v.Truncate(Cents), nil
	case NoRounding:
		return v, nil
	}
	return decimal.Zero, fmt.Errorf("未知的取整口径 %d", r)
}

// IsTickMultiple 报告价格是否为最小变动价位的整数倍。
//
// ⚠️ 实测（快期模拟，2026-09-07）：不是整数倍的限价单，柜台答
// 「下单价格不是价格单位的整倍数」。所以这条校验必须在报单前做，
// 而不是等柜台拒了再处理。
func IsTickMultiple(price, tick decimal.Decimal) (bool, error) {
	if !tick.IsPositive() {
		return false, fmt.Errorf("最小变动价位必须为正，得到 %s", tick)
	}
	return price.Div(tick).Sub(price.Div(tick).Round(0)).IsZero(), nil
}

// TickMode 是价格对齐到最小变动价位的方向。
type TickMode uint8

const (
	// TickNearest 取最近的整数倍。
	TickNearest TickMode = iota
	// TickDown 向下取整数倍（买方向的保守选择）。
	TickDown
	// TickUp 向上取整数倍（卖方向的保守选择）。
	TickUp
)

// AlignToTick 把价格对齐到最小变动价位的整数倍。
//
// ⚠️ 它**不替调用方决定方向**：买单向下取、卖单向上取才是保守的，
// 而「保守」在不同场景下方向相反。所以 mode 是必填参数，
// 由知道上下文的那一层给出。
func AlignToTick(price, tick decimal.Decimal, mode TickMode) (decimal.Decimal, error) {
	if !tick.IsPositive() {
		return decimal.Zero, fmt.Errorf("最小变动价位必须为正，得到 %s", tick)
	}
	if !price.IsPositive() {
		return decimal.Zero, fmt.Errorf("价格必须为正，得到 %s", price)
	}
	n := price.Div(tick)
	switch mode {
	case TickNearest:
		n = n.Round(0)
	case TickDown:
		n = n.Floor()
	case TickUp:
		n = n.Ceil()
	default:
		return decimal.Zero, fmt.Errorf("未知的对齐方向 %d", mode)
	}
	return n.Mul(tick), nil
}
