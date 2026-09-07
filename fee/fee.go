// Package fee 计算手续费。
//
// # 三档费率，两种形态
//
//	开仓  OpenRatioByMoney       OpenRatioByVolume
//	平昨  CloseRatioByMoney      CloseRatioByVolume
//	平今  CloseTodayRatioByMoney CloseTodayRatioByVolume
//
//	单笔手续费 = 成交量 × 成交价 × 乘数 × ByMoney + 成交量 × ByVolume
//
// ⚠️ **两项都要算并相加**，哪怕其中一项恒为零。同一品种通常只用其中一种形态，
// 另一种为 0——而**一个恒为 0 的加项不会暴露自己被漏掉了**。
//
// # 平今费率可以远高于平昨
//
// ⚠️ 也可以为 0（部分品种免平今）。日内策略的手续费几乎全部落在平今上——
// 把平今当平昨算，日内策略的回测收益会**系统性偏高**，
// 且偏高的幅度正比于交易频率：**频率越高的策略被高估得越多**。
//
// # 取整口径未实测
//
// 见 internal/decimalx：判别实验 5 尚未收敛，取整口径是必填参数，零值报错。
package fee

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Rates 是手续费率，**类型住在 refdata**。
//
// ⚠️ 它是规则数据，不是计算逻辑。放在本包会让 refdata 反过来 import fee，
// 而 docs/design.md 的依赖图是 `refdata ← {fee, margin, pnl}` —— 那样就成环。
type Rates = refdata.CommissionRates

// Validate 检查费率的形态。
//
// ⚠️ 费率允许为零（免平今是真实存在的），但**不允许为负**：
// 返佣不在本库范围内，而一个负费率会让手续费变成收入，
// 且它会一直看起来像是策略在赚钱。
func validateRates(r Rates) error {
	for _, f := range []struct {
		name string
		v    decimal.Decimal
	}{
		{"开仓按金额", r.OpenByMoney}, {"开仓按手数", r.OpenByVolume},
		{"平昨按金额", r.CloseByMoney}, {"平昨按手数", r.CloseByVolume},
		{"平今按金额", r.CloseTodayByMoney}, {"平今按手数", r.CloseTodayByVolume},
	} {
		if f.v.IsNegative() {
			return fmt.Errorf("费率「%s」为负（%s）—— 返佣不在本库范围内，请在外部处理",
				f.name, f.v)
		}
	}
	return nil
}

// pick 按开平标志选出该用哪一档费率。
func pick(r Rates, offset types.Offset) (byMoney, byVolume decimal.Decimal, err error) {
	switch offset {
	case types.Open:
		return r.OpenByMoney, r.OpenByVolume, nil

	case types.CloseToday:
		return r.CloseTodayByMoney, r.CloseTodayByVolume, nil

	case types.Close, types.CloseYesterday:
		// CTP 的 CloseRatio 就是平昨费率；裸 Close 在不区分今昨的交易所上也走这一档。
		//
		// ⚠️ 但「裸 Close 在 UseHistory 交易所上到底算平昨还是由柜台择优」
		// 尚未由 SimNow 裁决（state.md 的 simnow_pending#1）。
		// 那个判断属于 order 包，不在这里重复——本包只按传进来的标志算钱。
		return r.CloseByMoney, r.CloseByVolume, nil

	case types.ForceClose, types.ForceOff, types.LocalForceClose:
		// ⚠️ 强平按平今还是平昨计费，取决于它平掉的是哪一边，
		// 而**那个信息不在开平标志里**。本包拒绝猜。
		return decimal.Zero, decimal.Zero, fmt.Errorf(
			"强平标志 %v 没有对应的费率档：它按平今还是平昨计费，取决于平掉的是哪一边，"+
				"而那个信息不在开平标志里。调用方必须先解析成 %v 或 %v",
			offset, types.CloseToday, types.CloseYesterday)
	}
	return decimal.Zero, decimal.Zero, fmt.Errorf("开平标志 %v 无法映射到费率档", offset)
}

// Compute 计算一笔成交的手续费。
//
//	手续费 = 成交量 × 成交价 × 乘数 × ByMoney + 成交量 × ByVolume
//
// 返回值恒非负。rounding 是必填的取整口径，零值报错（判别实验 5 未收敛）。
func Compute(
	rates Rates, offset types.Offset,
	price, multiplier decimal.Decimal, volume int,
	rounding decimalx.Rounding,
) (decimal.Decimal, error) {
	if err := validateRates(rates); err != nil {
		return decimal.Zero, err
	}
	byMoney, byVolume, err := pick(rates, offset)
	if err != nil {
		return decimal.Zero, err
	}
	if volume <= 0 {
		return decimal.Zero, fmt.Errorf("成交手数必须为正，得到 %d", volume)
	}
	if !price.IsPositive() {
		return decimal.Zero, fmt.Errorf("成交价必须为正，得到 %s", price)
	}
	if !multiplier.IsPositive() {
		// ⚠️ 乘数漏乘会得到一个量级正确到肉眼看不出的错值。
		return decimal.Zero, fmt.Errorf("合约乘数必须为正，得到 %s", multiplier)
	}

	v := decimal.NewFromInt(int64(volume))
	// ⚠️ 两项都算并相加，哪怕其中一项的费率恒为零。
	// 一个恒为 0 的加项不会暴露自己被漏掉了。
	amount := v.Mul(price).Mul(multiplier).Mul(byMoney)
	perLot := v.Mul(byVolume)

	return rounding.Apply(amount.Add(perLot))
}

// ComputeRaw 返回**未取整**的手续费，供判别实验 5 用。
//
// ⚠️ 实验 5 要比较「柜台给的值」与「理论值」，而理论值必须是未取整的——
// 先取整再比较，等于把待验证的口径当成了已知。
func ComputeRaw(
	rates Rates, offset types.Offset,
	price, multiplier decimal.Decimal, volume int,
) (decimal.Decimal, error) {
	return Compute(rates, offset, price, multiplier, volume, decimalx.NoRounding)
}

// CloseTodayPremium 返回「平今比平昨贵多少」的比值，用于判断某品种是否日内友好。
//
// 第二个返回值报告比值是否有意义：平昨费率为零时无法作比。
//
// ⚠️ 它存在的理由是让「平今费率远高于平昨」这件事**可被量化地看见**。
// 把平今当平昨算的错误，其危害正比于这个比值，而危害本身不会报警。
func CloseTodayPremium(r Rates, price, multiplier decimal.Decimal) (decimal.Decimal, bool) {
	if !price.IsPositive() || !multiplier.IsPositive() {
		return decimal.Zero, false
	}
	notional := price.Mul(multiplier)
	closeFee := notional.Mul(r.CloseByMoney).Add(r.CloseByVolume)
	todayFee := notional.Mul(r.CloseTodayByMoney).Add(r.CloseTodayByVolume)
	if !closeFee.IsPositive() {
		return decimal.Zero, false
	}
	return todayFee.Div(closeFee), true
}
