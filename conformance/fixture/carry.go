package fixture

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Carry 把一份夹具里某个合约的持仓**结转到下一个交易日**。
//
// 它是跨交易日对拍的第一段：
//
//	D 日夹具的成交  → 重放出 D 日持仓
//	D 日结算价      → Settle，今仓滚成昨仓、逐日盯市基线重置
//	                  ⚠️ 逐笔对冲基线（OpenPrice）**不动**
//	→ 得到 D+1 日的起始持仓，交给 ReplayFrom
//
// ⚠️ 这一段不能省，理由在 ReplayFrom 的注释里：
// 柜台的成交截面按交易日重置，D+1 日的成交里**没有任何一笔能解释昨仓**。
//
// ⚠️ settlement 必须由调用方给，而且应当来自**交易所**而不是柜台。
// 拿柜台自己的结算价去验柜台自己的逐日盯市，是同义反复 ——
// refdata/exchange 就是为此存在的。本函数不去猜、不去取，只要求给。
func Carry(f *Fixture, symbol string, hedge types.HedgeFlag,
	dateType refdata.PositionDateType,
	settlement decimal.Decimal, nextDay types.TradingDay) (*position.Position, error) {

	// ⚠️ 合约代码从 symbol 直接解析，**不从 trades[0] 拿**。
	//
	// 两个理由：① 被问的是这个合约，那它就该是输入，而不是从数据里推回来；
	// ② 从 trades[0] 拿会在成交为空时越界 panic —— 破坏验证当场撞到了：
	// 把下面那条长度守卫改成 if false 之后，红的是 panic 而不是断言。
	// 一个「守卫没了就 panic」的函数，守卫是承重的而不是防御性的。
	inst, err := types.ParseSymbol(symbol, f.TradingDay)
	if err != nil {
		return nil, fmt.Errorf("合约键 %q：%w", symbol, err)
	}
	trades := f.TradesOf(symbol)
	if len(trades) == 0 {
		return nil, fmt.Errorf("夹具 %s 里 %s 一笔成交都没有 —— "+
			"⚠️ 结转一个没有成交记录的合约，得到的是空仓；"+
			"而空仓与「有仓但没记录」在结果上长得一样", f.Path, symbol)
	}
	p, err := Replay(inst, hedge, dateType, f.TradingDay, trades)
	if err != nil {
		return nil, fmt.Errorf("重放 %s 失败：%w", symbol, err)
	}
	if p.IsFlat() {
		return nil, fmt.Errorf("⚠️ %s 在 %s 结束时是空仓，结转它没有意义 —— "+
			"若本以为有过夜种子，那说明种子被平掉了", symbol, f.TradingDay)
	}
	if err := p.Settle(f.TradingDay, settlement, nextDay); err != nil {
		return nil, err
	}
	return p, nil
}

// Split 报告结转之后两条基线**是不是真的分开了**。
//
// ⚠️ 这不是锦上添花的检查，它是整个昨仓批的**前提**：
// 逐日盯市与逐笔对冲的区别，只有在两条基线取到不同的数时才可观测。
// 结算价恰好等于开仓均价时它们仍然相等，那时的样本对这条区分**没有判别力**，
// 而对拍会照样全绿 —— 一次什么都没验证的全绿。
//
// 返回：逐笔对冲基线的加权均价、逐日盯市基线（即结算价）、以及两者是否不同。
func Split(p *position.Position, dir types.Direction) (openAvg, basisAvg decimal.Decimal, differs bool, err error) {
	s, err := p.Side(dir)
	if err != nil {
		return decimal.Zero, decimal.Zero, false, err
	}
	o, hasO := s.AvgOpenPrice()
	b, hasB := s.AvgBasis()
	if !hasO || !hasB {
		return decimal.Zero, decimal.Zero, false,
			fmt.Errorf("%v 方向空仓，没有基线可比", dir)
	}
	return o, b, !o.Equal(b), nil
}

// Reconstruct 重建一份夹具里**完整的**持仓 —— 昨仓那部分从前一交易日结转来，
// 今仓那部分由当日成交重放上去。
//
// # ⚠️ 它补的是什么洞
//
// `Replay` 只回放**当日**成交。20260909 夜盘第一次出现「同一合约既有昨仓、
// 又有当日成交」的截面之后，凡是带昨仓的方向，重放出来的手数与均价
// 必然比柜台少一块 —— 那不是本库算错，是夹具的边界。
//
// 当时两条对拍（重放、保证金）的处理是**跳过**那些方向，各 7 处。
// 跳过是诚实的，但它把覆盖让出去了，而让出去的正好是新出现的、
// 最值得对的那批截面（今昨并存）。本函数把它们接回来。
//
// # ⚠️ 三个前提，缺一不可，缺了就报错而不是凑
//
//	prev 是**紧邻的**前一交易日          不相邻的话中间少了一次结算，结转出来的昨仓是错的
//	settlement 来自**交易所**            拿柜台自己的结算价去验柜台自己的逐日盯市是同义反复
//	dateType 实测过                      它决定结算时今仓变不变昨仓，猜错则今昨仓不滚动
//
// ⚠️ 本函数**不判断**这三条里的前两条能不能满足 —— 满足不了的合约
// （例如大商所：日行情 412 未打通，拿不到结算价）应当由调用方跳过，
// 并把跳过**记数报出来**。在这里悄悄回退成「只重放当日」是最坏的选择：
// 结果看起来完整，实际少了昨仓那一块。
func Reconstruct(prev, cur *Fixture, symbol string, hedge types.HedgeFlag,
	dateType refdata.PositionDateType, settlement decimal.Decimal) (*position.Position, error) {

	if prev.TradingDay >= cur.TradingDay {
		return nil, fmt.Errorf("⚠️ 前一份夹具的交易日 %s 不早于当前的 %s —— "+
			"结转方向反了，或者拿错了夹具", prev.TradingDay, cur.TradingDay)
	}
	carried, err := Carry(prev, symbol, hedge, dateType, settlement, cur.TradingDay)
	if err != nil {
		return nil, fmt.Errorf("结转 %s：%w", symbol, err)
	}
	inst, err := types.ParseSymbol(symbol, cur.TradingDay)
	if err != nil {
		return nil, err
	}
	// ⚠️ 当日没有成交是**正常**的（那天只是持有），此时结转结果就是答案。
	// ReplayFrom 对空成交也成立，这里不特判，免得两条路分岔。
	p, err := ReplayFrom(carried, inst, hedge, dateType, cur.TradingDay, cur.TradesOf(symbol))
	if err != nil {
		return nil, fmt.Errorf("在结转结果上重放 %s 的当日成交：%w", symbol, err)
	}
	return p, nil
}
