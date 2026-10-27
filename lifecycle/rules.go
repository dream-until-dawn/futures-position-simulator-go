package lifecycle

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// NewDailyRules 从完整基准和显式Schedule构造当日规则，覆盖、版本或参数未知时失败。
// 输入会复制；修改调用者基准指针或Schedule切片不影响已经构造的视图。
func NewDailyRules(base *refdata.Snapshot, schedule Schedule, day types.TradingDay, version int64) (*DailyRules, error) {
	if base == nil || base.Count() == 0 || version <= 0 || version == base.Version() {
		return nil, fmt.Errorf("生命周期缺基准或新规则版本无效/复用")
	}
	if schedule.First.Validate() != nil || schedule.Last.Validate() != nil || day.Validate() != nil || schedule.First > schedule.Last || day < schedule.First || day > schedule.Last {
		return nil, fmt.Errorf("生命周期日期无效或超出明确覆盖")
	}
	if (schedule.Investor != Individual && schedule.Investor != Institution) || strings.TrimSpace(schedule.Source) == "" {
		return nil, fmt.Errorf("生命周期资格或来源未知")
	}
	if len(schedule.Contracts) != base.Count() {
		return nil, fmt.Errorf("生命周期清单必须完整覆盖基准合约")
	}
	out := &DailyRules{day: day, schedule: schedule, contracts: map[types.InstrumentID]Status{}}
	out.schedule.Contracts = make([]Contract, 0, len(schedule.Contracts))
	copyBase, daily := refdata.NewBuilder(base.Version()), refdata.NewBuilder(version)
	for _, contract := range schedule.Contracts {
		if _, dup := out.contracts[contract.Instrument]; dup {
			return nil, fmt.Errorf("生命周期合约重复")
		}
		inst, err := base.Instrument(contract.Instrument)
		if err != nil {
			return nil, fmt.Errorf("生命周期合约不在基准：%w", err)
		}
		if contract.LastTradingDay.Validate() != nil || contract.LastHoldingDay.Validate() != nil || contract.LastHoldingDay > contract.LastTradingDay || inst.ExpireDate != contract.LastTradingDay {
			return nil, fmt.Errorf("生命周期交易/持仓期限未知或与基准矛盾")
		}
		if inst.PositionDateType != refdata.UseHistory && inst.PositionDateType != refdata.NoUseHistory {
			return nil, fmt.Errorf("生命周期今昨仓规则未知")
		}
		for _, v := range []decimal.Decimal{inst.VolumeMultiple, inst.PriceTick, inst.PriceLimitRatio} {
			if !finite(v) {
				return nil, fmt.Errorf("生命周期规格超出有限精度")
			}
		}
		if len(contract.MarginSteps) == 0 || contract.MarginSteps[0].From != schedule.First {
			return nil, fmt.Errorf("保证金首台阶必须明确覆盖首日")
		}
		selected := contract.MarginSteps[0]
		for i, step := range contract.MarginSteps {
			if step.From.Validate() != nil || step.From < schedule.First || step.From > schedule.Last || (i > 0 && step.From <= contract.MarginSteps[i-1].From) {
				return nil, fmt.Errorf("保证金台阶日期无效、重复或倒序")
			}
			for _, v := range []decimal.Decimal{step.Rates.LongByMoney, step.Rates.LongByVolume, step.Rates.ShortByMoney, step.Rates.ShortByVolume, step.Rates.CompanyAddOn} {
				if !finite(v) {
					return nil, fmt.Errorf("保证金台阶超出有限精度")
				}
			}
			// 未来未生效的台阶也走原Builder的费率校验，不能藏一个负值等未来才失败。
			if _, err = refdata.NewBuilder(1).AddInstrument(inst).AddMarginRates(inst.ID, types.Speculation, step.Rates).Build(); err != nil {
				return nil, fmt.Errorf("保证金台阶不合法：%w", err)
			}
			if step.From <= day {
				selected = step
			}
		}
		copyBase.AddInstrument(inst)
		active := inst
		active.IsTrading = inst.IsTrading && day <= contract.LastTradingDay
		daily.AddInstrument(active).AddMarginRates(inst.ID, types.Speculation, selected.Rates)
		for _, hedge := range []types.HedgeFlag{types.Speculation, types.Arbitrage, types.Hedge} {
			if rates, err := base.MarginRates(inst.ID, hedge); err == nil {
				for _, v := range []decimal.Decimal{rates.LongByMoney, rates.LongByVolume, rates.ShortByMoney, rates.ShortByVolume, rates.CompanyAddOn} {
					if !finite(v) {
						return nil, fmt.Errorf("生命周期基准保证金超出有限精度")
					}
				}
				copyBase.AddMarginRates(inst.ID, hedge, rates)
				if hedge != types.Speculation {
					daily.AddMarginRates(inst.ID, hedge, rates)
				}
			}
			rates, err := base.CommissionRates(inst.ID, hedge)
			if err != nil {
				if hedge == types.Speculation {
					return nil, fmt.Errorf("生命周期基准缺投机手续费：%w", err)
				}
				continue
			}
			for _, v := range []decimal.Decimal{rates.OpenByMoney, rates.OpenByVolume, rates.CloseByMoney, rates.CloseByVolume, rates.CloseTodayByMoney, rates.CloseTodayByVolume} {
				if !finite(v) {
					return nil, fmt.Errorf("生命周期费率超出有限精度")
				}
			}
			copyBase.AddCommissionRates(inst.ID, hedge, rates)
			daily.AddCommissionRates(inst.ID, hedge, rates)
		}
		contract.MarginSteps = append([]MarginStep(nil), contract.MarginSteps...)
		out.schedule.Contracts = append(out.schedule.Contracts, contract)
		out.contracts[inst.ID] = Status{Instrument: inst.ID, MarginFrom: selected.From, LastTradingDay: contract.LastTradingDay, LastHoldingDay: contract.LastHoldingDay, Expired: day > contract.LastTradingDay, CloseOnly: day > contract.LastHoldingDay, Tradable: active.IsTrading}
		out.ids = append(out.ids, inst.ID)
	}
	var err error
	if out.base, err = copyBase.Build(); err != nil {
		return nil, err
	}
	if out.data, err = daily.Build(); err != nil {
		return nil, err
	}
	sort.Slice(out.ids, func(i, j int) bool { return out.ids[i].Canonical() < out.ids[j].Canonical() })
	return out, nil
}

// finite 限制表示规模，拒绝巨大指数，具体市场比率仍须由调用者给事实。
func finite(v decimal.Decimal) bool {
	return v.Exponent() >= -64 && v.Exponent() <= 64 && v.Coefficient().BitLen() <= 512
}

// LifecycleDay 返回本规则绑定日；nil表示未知并返回0。
func (r *DailyRules) LifecycleDay() types.TradingDay {
	if r == nil || r.data == nil {
		return 0
	}
	return r.day
}

// Investor 返回规则集合适用的显式资格，nil返回未知。
func (r *DailyRules) Investor() InvestorKind {
	if r == nil || r.data == nil {
		return InvestorUnknown
	}
	return r.schedule.Investor
}

// Version 返回生成者明确指定的规则版本，nil为0。
func (r *DailyRules) Version() int64 {
	if r == nil || r.data == nil {
		return 0
	}
	return r.data.Version()
}

// Instrument 返回已应用到期交易状态的独立规格值。
func (r *DailyRules) Instrument(id types.InstrumentID) (refdata.Instrument, error) {
	if r == nil || r.data == nil {
		return refdata.Instrument{}, fmt.Errorf("生命周期规则未知")
	}
	return r.data.Instrument(id)
}

// MarginRates 返回绑定日选中台阶后的完整保证金率。
func (r *DailyRules) MarginRates(id types.InstrumentID, hedge types.HedgeFlag) (refdata.MarginRates, error) {
	if r == nil || r.data == nil {
		return refdata.MarginRates{}, fmt.Errorf("生命周期规则未知")
	}
	return r.data.MarginRates(id, hedge)
}

// CommissionRates 保留基准中明确存在的手续费率，不从保证金台阶推手续费。
func (r *DailyRules) CommissionRates(id types.InstrumentID, hedge types.HedgeFlag) (refdata.CommissionRates, error) {
	if r == nil || r.data == nil {
		return refdata.CommissionRates{}, fmt.Errorf("生命周期规则未知")
	}
	return r.data.CommissionRates(id, hedge)
}

// ProductInstruments 返回同品种合约的副本，不改变原Provider的大边分组口径。
func (r *DailyRules) ProductInstruments(exchange types.Exchange, product string) []types.InstrumentID {
	if r == nil || r.data == nil {
		return nil
	}
	return r.data.ProductInstruments(exchange, product)
}

// CheckOpening 检查是否仍能新增仓位；减仓和已发生事实不由本方法删除或改写。
func (r *DailyRules) CheckOpening(id types.InstrumentID) error {
	if r == nil || r.data == nil {
		return fmt.Errorf("生命周期规则未知")
	}
	status, ok := r.contracts[id]
	if !ok {
		return fmt.Errorf("生命周期缺合约规则")
	}
	if status.Expired || status.CloseOnly || !status.Tradable {
		return fmt.Errorf("合约%s不可交易或超过交易/持仓期限，不许新增开仓", id)
	}
	return nil
}

// Assess 评估完整已知仓位，未知、重复或负值不当成0。
func (r *DailyRules) Assess(holdings Holdings) (Report, error) {
	if r == nil || r.data == nil || !holdings.Known {
		return Report{}, fmt.Errorf("生命周期规则或持仓快照未知")
	}
	byID := map[types.InstrumentID]Holding{}
	for _, held := range holdings.Positions {
		if _, ok := r.contracts[held.Instrument]; !ok {
			return Report{}, fmt.Errorf("持仓合约缺生命周期规则")
		}
		if _, dup := byID[held.Instrument]; dup || held.Long < 0 || held.Short < 0 {
			return Report{}, fmt.Errorf("持仓重复或手数为负")
		}
		byID[held.Instrument] = held
	}
	out := Report{Day: r.day, Investor: r.schedule.Investor, Source: r.schedule.Source}
	for _, id := range r.ids {
		status := r.contracts[id]
		held := byID[id]
		status.Long, status.Short = held.Long, held.Short
		hasPosition := held.Long > 0 || held.Short > 0
		status.ExitByClose = hasPosition && r.day == status.LastHoldingDay
		status.HoldingViolation = hasPosition && (status.Expired || status.CloseOnly)
		out.HasBlockingHoldings = out.HasBlockingHoldings || status.HoldingViolation
		out.Contracts = append(out.Contracts, status)
	}
	return out, nil
}
