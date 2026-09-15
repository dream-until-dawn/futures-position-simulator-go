package futsim

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Settle 结束交易日 day：按今结算价兑现持仓盈亏、滚今昨、推基线，账户推进到 nextDay。
//
// prices 是**今结算价**，按合约给；nextDay 由调用方的日历给（本库不推算交易日，design.md §6.5）。
//
// 一步之内（design.md「门面的形状」§6）：
//
//	① 查齐     有仓的合约都要有结算价且为正 —— 缺一个报错，不拿昨结算价 / 最新价顶
//	② 兑现     按结算价算全部持仓的持仓盈亏 ⇒ 账户结算（上日结存 = 结存，当日累计清零）
//	③ 滚仓     持仓副本上 position.Settle：今仓 → 昨仓、基线 → 结算价；空仓丢掉
//	④ 次日计价 昨结算价 = 最新价 = 结算价；没仓的合约的计价输入丢掉（次日要重新 Mark）
//	⑤ 次日占用 按新基线、新昨结算价重算
//
// ①–⑤ 的计算全部先做完、状态一点不动；写账户那几步排在最后，中途失败则失效。
func (s *Simulator) Settle(day types.TradingDay, prices map[types.InstrumentID]decimal.Decimal, nextDay types.TradingDay) error {
	if err := s.usable(day); err != nil {
		return err
	}
	if !nextDay.After(day) {
		return fmt.Errorf("下一交易日 %d 不晚于当前交易日 %d", nextDay, day)
	}
	if err := nextDay.Validate(); err != nil {
		return fmt.Errorf("下一交易日不合法：%w", err)
	}
	if a := s.acc.Snapshot(); !a.FrozenMargin.IsZero() || !a.FrozenCommission.IsZero() || !a.FrozenCash.IsZero() {
		return fmt.Errorf("结算时仍有冻结额（保证金 %s / 手续费 %s）—— 挂单的跨日规则门面还没有（F4）", a.FrozenMargin, a.FrozenCommission)
	}

	// ① 查齐 + ② 结算那一刻的计价：最新价 = 结算价（日终计价只有这一个价，与 Choices.Mark 无关）
	atSettle := make(map[types.InstrumentID]priceState)
	next := make(map[posKey]*position.Position)
	nextPrices := make(map[types.InstrumentID]priceState)
	for _, k := range sortedKeys(s.positions) {
		p := s.positions[k]
		if p.IsFlat() {
			continue
		}
		px, ok := prices[k.inst]
		if !ok {
			return fmt.Errorf("%s 有持仓而没有今结算价 —— 不拿昨结算价或最新价顶（结算价推不出来）", k.inst)
		}
		if !px.IsPositive() {
			return fmt.Errorf("%s 今结算价 %s 不为正 —— 零只可能是缺失的伪装（design.md §6.5）", k.inst, px)
		}
		st := s.prices[k.inst]
		st.last, st.hasLast = px, true
		atSettle[k.inst] = st

		// ③ 滚仓（副本上）
		c := p.Clone()
		if err := c.Settle(day, px, nextDay); err != nil {
			return fmt.Errorf("%s 结算：%w", k.inst, err)
		}
		next[k] = c
		// ④ 次日计价
		nextPrices[k.inst] = priceState{last: px, hasLast: true, pre: px, hasPre: true}
	}
	settled, err := s.value(s.positions, atSettle)
	if err != nil {
		return fmt.Errorf("按结算价计价：%w", err)
	}
	// ⑤ 次日占用
	nextVal, err := s.value(next, nextPrices)
	if err != nil {
		return fmt.Errorf("次日占用：%w", err)
	}

	steps := []func() error{
		func() error { return s.acc.SetPositionProfit(day, settled.positionProfit) },
		func() error { return s.acc.SetMargin(day, settled.marginCompany, settled.marginExchange) },
		func() error { return s.acc.Settle(day, nextDay) },
		func() error { return s.acc.SetMargin(nextDay, nextVal.marginCompany, nextVal.marginExchange) },
		func() error { return s.acc.SetPositionProfit(nextDay, nextVal.positionProfit) },
		s.acc.Check,
	}
	for _, step := range steps {
		if err := step(); err != nil {
			s.broken = err
			return fmt.Errorf("⚠️ 结算写账户中途失败，模拟器失效：%w", err)
		}
	}
	s.positions, s.prices = next, nextPrices
	return nil
}
