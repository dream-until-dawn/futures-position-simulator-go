package fixture

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// MarginOf 按 margin 包算出一个持仓两个方向的保证金。
//
// ⚠️ 本函数存在的理由是**不让 view 包重算保证金**：
// view.PositionOf 刻意把保证金做成入参，因为重算会产生第二个实现，
// 而两个实现一起退化时测试全绿。保证金只有 margin 包一个实现，
// 这里做的只是把持仓翻译成 margin.Leg。
//
// ⚠️ 逐笔翻译成 leg，而不是「按方向合计手数」：
// 今仓与昨仓的计价基准可能不同（OpenTodayPreSettleHistory 那个候选），
// 合并之后就再也分不开了。基准候选未收敛之前，翻译必须保住这个维度。
func MarginOf(p *position.Position, rates refdata.MarginRates, multiplier,
	preSettlement decimal.Decimal, maxMarginSide bool,
	basis margin.PriceBasis, scope margin.SideScope) (long, short decimal.Decimal, err error) {

	if !multiplier.IsPositive() {
		return decimal.Zero, decimal.Zero, fmt.Errorf(
			"合约乘数必须为正，得到 %s —— 漏乘会得到量级正确到肉眼看不出的错值", multiplier)
	}
	if !preSettlement.IsPositive() {
		return decimal.Zero, decimal.Zero, fmt.Errorf(
			"昨结算价必须为正，得到 %s —— 这是「没有」不是「零」", preSettlement)
	}

	var legs []margin.Leg
	for _, d := range []types.Direction{types.Buy, types.Sell} {
		s, err := p.Side(d)
		if err != nil {
			return decimal.Zero, decimal.Zero, err
		}
		for _, l := range s.Lots() {
			legs = append(legs, margin.Leg{
				Instrument: p.Instrument, Direction: d, Volume: l.Volume,
				Multiplier: multiplier, Rates: rates,
				IsHistory: l.Settled, MaxMarginSide: maxMarginSide,
				OpenPrice:        l.OpenPrice,
				PreSettlement:    preSettlement,
				HasPreSettlement: true,
			})
		}
	}
	if len(legs) == 0 {
		// ⚠️ 空仓没有保证金，不是保证金为零 —— 与 view.Absent 同一条理由。
		// 调用方拿到零值时必须靠 err 区分，所以这里给 err 而不是给 0。
		return decimal.Zero, decimal.Zero, errNoPosition
	}
	res, err := margin.Compute(legs, basis, scope)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	// ⚠️ 用**交易所**口径与柜台比。公司口径含加收，而加收是券商侧的配置，
	// 不属于本次被测的规则；混用会让一个配置差异看起来像算法错。
	//
	// ⚠️ 但要说清楚：**本口子上这两个口径分不开**。
	// 实测费率里 CompanyAddOn 为零（快期没有加收，或者它给的 margin_long
	// 本来就是交易所口径 —— 两种读法都成立，样本分不开）。
	// 破坏验证演示过：把这两行换成 LongCompany/ShortCompany，全部测试照样绿。
	//
	// 也就是说「保证金分两层」这件事在这里**测不到**，
	// 与「平今费率测不到」（kq_facts 18）是同一类盲区：
	// 不是没建模，是这个口子上没有能把两者分开的信号。
	for _, g := range res.Groups {
		long = long.Add(g.LongExchange)
		short = short.Add(g.ShortExchange)
	}
	return long, short, nil
}

// errNoPosition 表示这个合约上没有持仓，因而**没有**保证金。
var errNoPosition = fmt.Errorf("该合约无持仓，没有保证金（这是「没有」不是「零」）")

// IsNoPosition 报告错误是不是「无持仓」。
func IsNoPosition(err error) bool { return err == errNoPosition }
