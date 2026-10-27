package futsim

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/lifecycle"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// rulesLifecycleDay 仅按显式生命周期接口识别日期绑定，普通Provider保持原语义。
func rulesLifecycleDay(rules refdata.Provider) (types.TradingDay, bool) {
	bound, ok := rules.(interface{ LifecycleDay() types.TradingDay })
	if !ok {
		return 0, false
	}
	return bound.LifecycleDay(), true
}

// lifecycleHoldings 从真实明细安全合计；不从界面输出或可能溢出的裸int总量反推。
func (s *Simulator) lifecycleHoldings() (lifecycle.Holdings, error) {
	byID := map[types.InstrumentID]lifecycle.Holding{}
	maxInt := int(^uint(0) >> 1)
	for _, key := range sortedKeys(s.positions) {
		p := s.positions[key]
		if p == nil {
			return lifecycle.Holdings{}, fmt.Errorf("生命周期持仓明细未知")
		}
		if p.IsFlat() {
			continue
		}
		held := byID[key.inst]
		held.Instrument = key.inst
		for _, direction := range []types.Direction{types.Buy, types.Sell} {
			side, err := p.Side(direction)
			if err != nil {
				return lifecycle.Holdings{}, err
			}
			for _, lot := range side.Lots() {
				into := &held.Long
				if direction == types.Sell {
					into = &held.Short
				}
				if lot.Volume <= 0 || lot.Volume > maxInt-*into {
					return lifecycle.Holdings{}, fmt.Errorf("生命周期持仓手数未知或合计溢出")
				}
				*into += lot.Volume
			}
		}
		byID[key.inst] = held
	}
	out := lifecycle.Holdings{Known: true}
	ids := map[types.InstrumentID]bool{}
	for id := range byID {
		ids[id] = true
	}
	for _, id := range sortedInstruments(ids) {
		out.Positions = append(out.Positions, byID[id])
	}
	return out, nil
}

// AssessLifecycle 只读评估当前绑定规则的到期/持仓期限；未知资格或资料不伪装成无风险。
func (s *Simulator) AssessLifecycle() (lifecycle.Report, error) {
	if s == nil || s.acc == nil {
		return lifecycle.Report{}, fmt.Errorf("模拟器未初始化，不能评估生命周期")
	}
	if err := s.usable(s.acc.Day); err != nil {
		return lifecycle.Report{}, err
	}
	rules, ok := s.rules.(*lifecycle.DailyRules)
	if !ok || rules == nil {
		return lifecycle.Report{}, fmt.Errorf("规则未绑定生命周期资料")
	}
	held, err := s.lifecycleHoldings()
	if err != nil {
		return lifecycle.Report{}, err
	}
	return rules.Assess(held)
}

// settlementRuleInput 仅在临时实例结算当日时转发原Provider，隐藏旧日绑定；不用于外部配置。
type settlementRuleInput struct{ refdata.Provider }

// SettleWithRules 原子完成当日结算和次日规则切换；规则或持仓期限不满足时完整状态不变。
// 返回的生命周期报告绑定nextDay，错误时只作预检诊断，不能据此认为已经推进交易日。
func (s *Simulator) SettleWithRules(day types.TradingDay, prices map[types.InstrumentID]decimal.Decimal, nextDay types.TradingDay, nextRules *lifecycle.DailyRules) (lifecycle.Report, error) {
	if s == nil || s.acc == nil {
		return lifecycle.Report{}, fmt.Errorf("模拟器未初始化，不能切换规则")
	}
	if err := s.usable(day); err != nil {
		return lifecycle.Report{}, err
	}
	if nextRules == nil || nextRules.LifecycleDay() != nextDay || nextRules.Version() == 0 || nextRules.Version() == s.rules.Version() {
		return lifecycle.Report{}, fmt.Errorf("次日规则日期或版本未知/复用")
	}
	if current, ok := s.rules.(*lifecycle.DailyRules); ok && current.Investor() != nextRules.Investor() {
		return lifecycle.Report{}, fmt.Errorf("投资者资格不可随日规则切换改变")
	}
	held, err := s.lifecycleHoldings()
	if err != nil {
		return lifecycle.Report{}, err
	}
	check, err := nextRules.Assess(held)
	if err != nil {
		return lifecycle.Report{}, err
	}
	if check.HasBlockingHoldings {
		return check, fmt.Errorf("次日有超过交易/持仓期限的仓位，拒绝跨日，不自动平仓")
	}
	for _, key := range sortedKeys(s.positions) {
		if s.positions[key].IsFlat() {
			continue
		}
		old, err := s.rules.Instrument(key.inst)
		if err != nil {
			return check, err
		}
		next, err := nextRules.Instrument(key.inst)
		if err != nil {
			return check, err
		}
		if next.ID != old.ID || !next.VolumeMultiple.Equal(old.VolumeMultiple) || !next.PriceTick.Equal(old.PriceTick) || next.PositionDateType != old.PositionDateType {
			return check, fmt.Errorf("在仓合约的身份/乘数/价位/今昨规则改变，不能静默迁移")
		}
	}
	for _, price := range prices {
		if price.Exponent() < -64 || price.Exponent() > 64 || price.Coefficient().BitLen() > 512 {
			return check, fmt.Errorf("结算价超出有限精度范围")
		}
	}
	before, err := s.State()
	if err != nil {
		return check, err
	}
	cfg := Config{Day: day, Rules: s.rules, Choices: s.choices, Calendar: s.calendar, TickRounding: s.tickRounding, PositionLimits: s.positionLimits}
	candidate, err := Restore(cfg, before)
	if err != nil {
		return check, err
	}
	candidate.rules = settlementRuleInput{s.rules}
	if err = candidate.Settle(day, prices, nextDay); err != nil {
		return check, err
	}
	candidate.rules = nextRules
	value, err := candidate.value(candidate.positions, candidate.prices)
	if err != nil {
		return check, fmt.Errorf("次日新规则计价：%w", err)
	}
	if err = candidate.acc.SetMargin(nextDay, value.marginCompany, value.marginExchange); err != nil {
		return check, err
	}
	if err = candidate.acc.SetPositionProfit(nextDay, value.positionProfit); err != nil {
		return check, err
	}
	candidate.groups = value.groups
	after, err := candidate.State()
	if err != nil {
		return check, err
	}
	cfg.Day, cfg.Rules = nextDay, nextRules
	verified, err := Restore(cfg, after)
	if err != nil {
		return check, fmt.Errorf("次日新规则完整复核：%w", err)
	}
	*s = *verified
	return check, nil
}
