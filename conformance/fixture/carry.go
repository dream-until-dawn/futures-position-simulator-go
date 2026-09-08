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
