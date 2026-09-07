// Package pnl 计算盈亏，两套口径都给。
//
// # 它是纯函数，且不认识 position
//
// 按 docs/design.md 的依赖图，箭头是 `pnl → position`：**position 依赖 pnl，
// 不是反过来**。所以本包定义自己的输入类型 Leg，由调用方转换。
// 三行转换换一个无环的依赖图，值得。
//
// # 两套口径
//
//	逐日盯市 ByDate    基线是 Leg.Basis：昨仓用昨结算价、今仓用开仓价，每日结算重置
//	逐笔对冲 ByTrade   基线是 Leg.OpenPrice：原始成交价，跨结算日不重置
//
// 两条基线都由调用方在 Leg 里带进来，本包只做算术。这是刻意的：
// **「昨仓的基线到底是不是昨结算价」是待实测的规则**（判别实验 2），
// 而规则住在 position 的结算逻辑里，不该在算术里再判断一次。
// 同一条规则实现两遍，两遍会分岔，而分岔时没有任何东西会报警。
package pnl

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Leg 是参与盈亏计算的一段持仓或一段被平掉的持仓。
//
// 它同时带两条基线，因为两套口径同时存在。调用方从 position 的明细转换而来。
type Leg struct {
	Volume    int             // 手数，必须为正
	OpenPrice decimal.Decimal // 原始成交价 —— 逐笔对冲基线
	Basis     decimal.Decimal // 逐日盯市基线
}

// Result 是一次计算的两套口径。
type Result struct {
	ByDate  decimal.Decimal // 逐日盯市
	ByTrade decimal.Decimal // 逐笔对冲
}

// Equal 报告两套口径是否相等。
//
// ⚠️ 它们相等**不说明计算正确**：今仓的两条基线本就都是开仓价，
// 于是纯今仓的样本上两者必然相等。拿这种样本去「验证」两套口径都实现了，
// 验不出任何东西——与「空仓时两个权益口径必然相等」是同一个形状。
func (r Result) Equal() bool { return r.ByDate.Equal(r.ByTrade) }

// Mark 是**盘中**用哪个价给持仓计价。
//
// ⚠️ 它是判别实验 1/2 的产物，而那两条**尚未收敛**。
// 日终用今结算价没有疑问；盘中用最新成交价还是昨结算价，本库还没有证据。
//
// 零值是「未实测」，用它会**报错**——不回退到任何「合理的默认」。
// 这条不确定会传染到风险度，进而传染到追保与强平判据，即整个风控链路的入口。
type Mark uint8

const (
	// MarkUnmeasured 是零值：实验 1/2 未收敛，使用即报错。
	MarkUnmeasured Mark = iota
	// MarkLast 用最新成交价。
	MarkLast
	// MarkPreSettlement 用昨结算价。
	//
	// ⚠️ 这个候选下，**昨仓的盘中持仓盈亏恒为零**（基线与计价价相同），
	// 只有今仓会随行情动。这是它与 MarkLast 最容易分开的地方。
	MarkPreSettlement
	// MarkSettlement 用今结算价。日终结算时用它，盘中拿不到。
	MarkSettlement
)

func (m Mark) String() string {
	switch m {
	case MarkLast:
		return "最新成交价"
	case MarkPreSettlement:
		return "昨结算价"
	case MarkSettlement:
		return "今结算价"
	}
	return "未实测"
}

// Prices 是计价所需的一组价格。
//
// ⚠️ 每个价格都配一个「有没有」，区分「值是零」与「没有这个价」。
// 没有任何品种的价格会是零，`0` 只可能是缺失的伪装——
// 免费日线源上就有 7.7%～98.6% 的结算价字段是 0。
type Prices struct {
	Last             decimal.Decimal
	HasLast          bool
	PreSettlement    decimal.Decimal
	HasPreSettlement bool
	Settlement       decimal.Decimal
	HasSettlement    bool
}

// pick 按 Mark 取价，缺失时报错并说明**缺的是哪一个**。
func (p Prices) pick(m Mark) (decimal.Decimal, error) {
	switch m {
	case MarkUnmeasured:
		return decimal.Zero, fmt.Errorf("计价基准未指定：判别实验 1/2 尚未收敛，"+
			"本库不提供默认值。见 docs/state.md 的 rules_pending。"+
			"（候选：%v / %v / %v）", MarkLast, MarkPreSettlement, MarkSettlement)
	case MarkLast:
		if !p.HasLast {
			return decimal.Zero, fmt.Errorf("要用最新成交价计价，但没有最新价 —— " +
				"这是「没有」不是「零」，不能拿 0 顶替")
		}
		return p.Last, nil
	case MarkPreSettlement:
		if !p.HasPreSettlement {
			return decimal.Zero, fmt.Errorf("要用昨结算价计价，但没有昨结算价")
		}
		return p.PreSettlement, nil
	case MarkSettlement:
		if !p.HasSettlement {
			return decimal.Zero, fmt.Errorf("要用今结算价计价，但没有今结算价")
		}
		return p.Settlement, nil
	}
	return decimal.Zero, fmt.Errorf("未知的计价基准 %d", m)
}

// sign 把买卖方向转成 +1 / −1。
func sign(dir types.Direction) (decimal.Decimal, error) {
	switch dir {
	case types.Buy:
		return decimal.NewFromInt(1), nil
	case types.Sell:
		return decimal.NewFromInt(-1), nil
	}
	return decimal.Zero, fmt.Errorf("买卖方向未指定 —— 盈亏的符号取决于它，不能默认")
}

func validate(legs []Leg, multiplier decimal.Decimal) error {
	if !multiplier.IsPositive() {
		// ⚠️ 乘数漏乘是静默风险清单里的一条：金额量级看起来仍然合理。
		// 所以它必须是必填参数，且非正即报错。
		return fmt.Errorf("合约乘数必须为正，得到 %s —— 漏乘乘数会得到一个"+
			"量级正确到肉眼看不出的错值", multiplier)
	}
	for i, l := range legs {
		if l.Volume <= 0 {
			return fmt.Errorf("第 %d 段的手数是 %d，必须为正", i, l.Volume)
		}
		if !l.OpenPrice.IsPositive() {
			return fmt.Errorf("第 %d 段的开仓价是 %s，必须为正", i, l.OpenPrice)
		}
		if !l.Basis.IsPositive() {
			return fmt.Errorf("第 %d 段的逐日盯市基线是 %s，必须为正", i, l.Basis)
		}
	}
	return nil
}

// CloseProfit 计算平仓盈亏，两套口径都给。
//
//	逐日盯市 = Σ (平仓价 − 该段.Basis)     × 手数 × 乘数 × 方向
//	逐笔对冲 = Σ (平仓价 − 该段.OpenPrice) × 手数 × 乘数 × 方向
//
// legs 来自 position 的平仓结果（那里已按今昨分好，且每段带着自己的两条基线）。
func CloseProfit(legs []Leg, dir types.Direction, closePrice, multiplier decimal.Decimal) (Result, error) {
	var r Result
	sg, err := sign(dir)
	if err != nil {
		return r, err
	}
	if err := validate(legs, multiplier); err != nil {
		return r, err
	}
	if !closePrice.IsPositive() {
		return r, fmt.Errorf("平仓价必须为正，得到 %s", closePrice)
	}
	for _, l := range legs {
		q := decimal.NewFromInt(int64(l.Volume)).Mul(multiplier).Mul(sg)
		r.ByDate = r.ByDate.Add(closePrice.Sub(l.Basis).Mul(q))
		r.ByTrade = r.ByTrade.Add(closePrice.Sub(l.OpenPrice).Mul(q))
	}
	return r, nil
}

// PositionProfit 计算持仓盈亏（逐日盯市口径）。
//
//	Σ (计价价 − 该段.Basis) × 手数 × 乘数 × 方向
//
// mark 决定计价价。⚠️ 盘中该用哪个价是判别实验 1/2 的问题，尚未收敛，
// 所以本函数**要求显式指定**，零值报错。
func PositionProfit(legs []Leg, dir types.Direction, prices Prices, mark Mark, multiplier decimal.Decimal) (decimal.Decimal, error) {
	px, err := prices.pick(mark)
	if err != nil {
		return decimal.Zero, err
	}
	sg, err := sign(dir)
	if err != nil {
		return decimal.Zero, err
	}
	if err := validate(legs, multiplier); err != nil {
		return decimal.Zero, err
	}
	out := decimal.Zero
	for _, l := range legs {
		q := decimal.NewFromInt(int64(l.Volume)).Mul(multiplier).Mul(sg)
		out = out.Add(px.Sub(l.Basis).Mul(q))
	}
	return out, nil
}

// FloatProfit 计算浮动盈亏：基线是**开仓价**，与逐笔对冲同源。
//
//	Σ (计价价 − 该段.OpenPrice) × 手数 × 乘数 × 方向
//
// ⚠️ 对**今仓**而言它必然等于 PositionProfit（两条基线都是开仓价）；
// 对**昨仓**则不等——这正是把两者分开的判别点，见 TestFloatDiffersOnHistory。
func FloatProfit(legs []Leg, dir types.Direction, prices Prices, mark Mark, multiplier decimal.Decimal) (decimal.Decimal, error) {
	px, err := prices.pick(mark)
	if err != nil {
		return decimal.Zero, err
	}
	sg, err := sign(dir)
	if err != nil {
		return decimal.Zero, err
	}
	if err := validate(legs, multiplier); err != nil {
		return decimal.Zero, err
	}
	out := decimal.Zero
	for _, l := range legs {
		q := decimal.NewFromInt(int64(l.Volume)).Mul(multiplier).Mul(sg)
		out = out.Add(px.Sub(l.OpenPrice).Mul(q))
	}
	return out, nil
}

// DayProfit 是当日盈亏 = 平仓盈亏 + 持仓盈亏，均为逐日盯市口径。
func DayProfit(closeProfit, positionProfit decimal.Decimal) decimal.Decimal {
	return closeProfit.Add(positionProfit)
}

// SolveBasis 从两个盈亏字段反解出逐日盯市所用的基线，**不需要合约乘数**。
//
//	浮动盈亏 = (计价价 − 开仓价) × 手数 × 乘数
//	持仓盈亏 = (计价价 − X)      × 手数 × 乘数
//	⇒ X = 计价价 − (计价价 − 开仓价) × 持仓盈亏 / 浮动盈亏
//
// 乘数与手数在相除时约掉了。判别实验 2 用它把「基线是昨结算价」与
// 「基线是开仓价」分开，而不必先把规则数据链路建起来——
// **为了拿一个约得掉的量去引入另一条未验证的链路，是把实验押在别的东西上。**
//
// ⚠️ ok 为 false 表示分母太接近零，**解不出来**。那是「测不出」，
// 不是「基线等于开仓价」。
func SolveBasis(markPx, openPx, floatProfit, positionProfit decimal.Decimal) (decimal.Decimal, bool) {
	if floatProfit.Abs().LessThan(decimal.New(1, -9)) {
		return decimal.Zero, false
	}
	return markPx.Sub(markPx.Sub(openPx).Mul(positionProfit).Div(floatProfit)), true
}
