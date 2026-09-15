package fixture

import (
	"fmt"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

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

// ReconstructOnFacade 在**门面**上重建一个合约跨日之后的持仓：前一日成交 → 按交易所结算价 Settle → 当日成交（cur 为 nil 时只结转到 nextDay）。
//
// 对拍测试（F6b）与 `cmd/oracle -carry`（F7b）都走它：记账链条只在门面里有一份。
// F7b 之前还有 Carry / Reconstruct（`position.Settle` + `ReplayFrom` 的第二份实现），design.md「门面的形状」§10 / §11。
//
// # ⚠️ 为什么非结转不可
//
// 柜台的成交截面按交易日重置：D+1 日的截面里有昨仓，而 D+1 日的成交里**没有任何一笔能解释它** —— 那几手是昨天开的。
// 只重放当日成交会漏掉全部昨仓，而漏掉之后的持仓**看起来完全正常**，只是少几手。
//
// # ⚠️ 三个前提，缺一不可；本函数不判断前两条，满足不了的合约由调用方跳过并**记数报出来**
//
//	prev 是**紧邻的**前一交易日   不相邻的话中间少了一次结算，结转出来的昨仓是错的
//	settlement 来自**交易所**     拿柜台自己的结算价去验柜台自己的逐日盯市是同义反复
//	dateType 实测过               它决定结算时今仓变不变昨仓，猜错则今昨仓不滚动
//
// # 比 Carry 多要的三样，缺了报错
//
//	spec 的手续费率        门面每笔成交都算手续费；持仓不看它，但不许填零顶
//	前一日的昨结算价与最新价  门面有仓就要计价输入（占用、持仓盈亏）
//	前一日的 pre_balance     账户的起点；这个模拟器只装一个合约，账户侧的数**不拿去比**
//
// # ⚠️ 失去的一道检查
//
// ReplayFrom 的「三种消耗顺序各跑一遍、不一致就报歧义」在门面上没有对应物：门面的消耗顺序是写死的
// （UseHistory 上显式平今平昨、裸 CLOSE 按口径记作平昨；NoUseHistory 先平昨）。
// 快期上消耗顺序结构性测不出（kq_facts 40），那道检查在快期夹具上一直在答「分不开」；F7b 删 Reconstruct 时随之离开结转路径。
func ReconstructOnFacade(prev, cur *Fixture, symbol string, spec Spec,
	dateType refdata.PositionDateType, settlement decimal.Decimal, nextDay types.TradingDay) (*position.Position, error) {

	if cur != nil {
		if nextDay != cur.TradingDay {
			return nil, fmt.Errorf("nextDay %s 与当日夹具的交易日 %s 不同", nextDay, cur.TradingDay)
		}
	}
	if prev.TradingDay >= nextDay {
		return nil, fmt.Errorf("⚠️ 前一份夹具的交易日 %s 不早于 %s —— 结转方向反了，或者拿错了夹具", prev.TradingDay, nextDay)
	}
	inst, err := types.ParseSymbol(symbol, prev.TradingDay)
	if err != nil {
		return nil, fmt.Errorf("合约键 %q：%w", symbol, err)
	}
	trades := prev.TradesOf(symbol)
	if len(trades) == 0 {
		return nil, fmt.Errorf("夹具 %s 里 %s 一笔成交都没有 —— "+
			"⚠️ 结转一个没有成交记录的合约，得到的是空仓；而空仓与「有仓但没记录」在结果上长得一样", prev.Path, symbol)
	}
	pb, ok := numberOf(prev.Account, "pre_balance")
	if !ok {
		return nil, fmt.Errorf("夹具 %s 没有 pre_balance", prev.Path)
	}
	pre, ok := prev.PreSettlement(symbol)
	if !ok {
		return nil, fmt.Errorf("夹具 %s 里 %s 没有昨结算价", prev.Path, symbol)
	}
	last, ok := prev.Positions[symbol]["last_price"]
	if !ok || last.Absent || last.IsText || !last.Number.IsPositive() {
		return nil, fmt.Errorf("夹具 %s 里 %s 没有最新价 —— 这是「没有」不是「零」", prev.Path, symbol)
	}

	rules := specRules{version: 1, byID: map[types.InstrumentID]Spec{inst: spec},
		dates: map[types.InstrumentID]refdata.PositionDateType{inst: dateType}}
	sim, err := futsim.New(futsim.Config{Day: prev.TradingDay, PreBalance: pb, Rules: rules, Choices: fixtureChoices()})
	if err != nil {
		return nil, err
	}
	if err := sim.Mark(prev.TradingDay, futsim.Quote{Instrument: inst, Last: last.Number, HasLast: true,
		PreSettlement: pre, HasPreSettlement: true}); err != nil {
		return nil, err
	}
	apply := func(day types.TradingDay, ts []Trade) error {
		for _, tr := range ts {
			if err := sim.ApplyTrade(day, match.Trade{Instrument: inst, Direction: tr.Direction, Offset: tr.Offset,
				Hedge: types.Speculation, Price: tr.Price, Volume: tr.Volume}); err != nil {
				return fmt.Errorf("成交 %s（%v/%v %d 手）：%w", tr.TradeID, tr.Direction, tr.Offset, tr.Volume, err)
			}
		}
		return nil
	}
	if err := apply(prev.TradingDay, trades); err != nil {
		return nil, fmt.Errorf("重放 %s：%w", symbol, err)
	}
	if p, ok := sim.Position(inst, types.Speculation); !ok || p.IsFlat() {
		return nil, fmt.Errorf("⚠️ %s 在 %s 结束时是空仓，结转它没有意义 —— 若本以为有过夜种子，那说明种子被平掉了", symbol, prev.TradingDay)
	}
	if err := sim.Settle(prev.TradingDay, map[types.InstrumentID]decimal.Decimal{inst: settlement}, nextDay); err != nil {
		return nil, err
	}
	if cur != nil {
		if err := apply(nextDay, cur.TradesOf(symbol)); err != nil {
			return nil, fmt.Errorf("在结转结果上重放 %s 的当日成交：%w", symbol, err)
		}
	}
	p, _ := sim.Position(inst, types.Speculation)
	return p, nil
}
