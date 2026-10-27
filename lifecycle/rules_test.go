package lifecycle

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func money(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// scheduleFixture 使用纯合成期限和比率，不是rb实际交割日期或自然人规则。
func scheduleFixture(t testing.TB) (*refdata.Snapshot, Schedule, types.InstrumentID) {
	t.Helper()
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2026, Month: 10}
	inst := refdata.Instrument{ID: id, VolumeMultiple: money("10"), PriceTick: money("1"), PositionDateType: refdata.UseHistory, ExpireDate: 20261029, IsTrading: true, MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000, HasPriceLimitRatio: true, PriceLimitRatio: money("0.1")}
	b := refdata.NewBuilder(1).AddInstrument(inst).AddMarginRates(id, types.Speculation, refdata.MarginRates{LongByMoney: money("0.1"), ShortByMoney: money("0.1"), CompanyAddOn: money("0.02")}).AddCommissionRates(id, types.Speculation, refdata.CommissionRates{OpenByVolume: money("1"), CloseByVolume: money("2"), CloseTodayByVolume: money("3")})
	base, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	schedule := Schedule{First: 20261027, Last: 20261030, Investor: Individual, Source: "synthetic-policy", Contracts: []Contract{{Instrument: id, LastTradingDay: 20261029, LastHoldingDay: 20261028, MarginSteps: []MarginStep{{From: 20261027, Rates: refdata.MarginRates{LongByMoney: money("0.1"), ShortByMoney: money("0.1"), CompanyAddOn: money("0.02")}}, {From: 20261028, Rates: refdata.MarginRates{LongByMoney: money("0.2"), ShortByMoney: money("0.2"), CompanyAddOn: money("0.03")}}}}}}
	return base, schedule, id
}

// TestLifecycleZeroIsUnknown 零值规则不能把已知空仓误报成完整健康评估。
func TestLifecycleZeroIsUnknown(t *testing.T) {
	var rules DailyRules
	if report, err := rules.Assess(Holdings{Known: true}); err == nil || report.Day != 0 {
		t.Fatalf("零值规则给出假健康结论 report=%+v err=%v", report, err)
	}
	if rules.Version() != 0 || rules.LifecycleDay() != 0 || rules.Investor() != InvestorUnknown {
		t.Fatal("零值规则元数据不为未知")
	}
	if _, err := rules.Instrument(types.InstrumentID{}); err == nil {
		t.Fatal("零值规则返回规格")
	}
	if _, err := rules.MarginRates(types.InstrumentID{}, types.Speculation); err == nil {
		t.Fatal("零值规则返回保证金")
	}
	if _, err := rules.CommissionRates(types.InstrumentID{}, types.Speculation); err == nil {
		t.Fatal("零值规则返回费率")
	}
	if rules.ProductInstruments(types.SHFE, "rb") != nil {
		t.Fatal("零值规则返回品种清单")
	}
}

// TestLifecycleStepsAndDeadlines 台阶含起点；期限不会自动抹掉持仓，资格只选择显式不同的资料。
func TestLifecycleStepsAndDeadlines(t *testing.T) {
	for _, day := range []types.TradingDay{20261027, 20261028, 20261029, 20261030} {
		base, plan, id := scheduleFixture(t)
		rules, err := NewDailyRules(base, plan, day, 2)
		if err != nil {
			t.Fatal(err)
		}
		rates, err := rules.MarginRates(id, types.Speculation)
		if err != nil {
			t.Fatal(err)
		}
		want := "0.1"
		from := types.TradingDay(20261027)
		if day >= 20261028 {
			want = "0.2"
			from = 20261028
		}
		if !rates.LongByMoney.Equal(money(want)) {
			t.Fatalf("保证金台阶边界错误 day=%v rates=%+v", day, rates)
		}
		inst, err := rules.Instrument(id)
		if err != nil || inst.IsTrading != (day <= 20261029) {
			t.Fatalf("到期交易标志错误 day=%v inst=%+v err=%v", day, inst, err)
		}
		report, err := rules.Assess(Holdings{Known: true, Positions: []Holding{{Instrument: id, Long: 2, Short: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Contracts) != 1 {
			t.Fatal("生命周期报告合约缺失")
		}
		status := report.Contracts[0]
		if status.MarginFrom != from || status.Long != 2 || status.Short != 1 || status.Expired != (day > 20261029) || status.CloseOnly != (day > 20261028) || status.ExitByClose != (day == 20261028) || report.HasBlockingHoldings != (day > 20261028) || status.HoldingViolation != report.HasBlockingHoldings {
			t.Fatalf("生命周期期限或仓位报告错误 day=%v report=%+v", day, report)
		}
		if err = rules.CheckOpening(id); (err != nil) != (day > 20261028) {
			t.Fatalf("超过持仓期限仍可新开 day=%v err=%v", day, err)
		}
		empty, err := rules.Assess(Holdings{Known: true})
		if err != nil || empty.HasBlockingHoldings || empty.Contracts[0].Long != 0 {
			t.Fatalf("已知空仓被当成未知/有仓 report=%+v err=%v", empty, err)
		}
		if _, err = rules.Assess(Holdings{}); err == nil {
			t.Fatal("未知持仓被当成空")
		}
		if day == 20261029 {
			plan.Investor = Institution
			plan.Contracts[0].LastHoldingDay = 20261029
			other, err := NewDailyRules(base, plan, day, 3)
			if err != nil {
				t.Fatal(err)
			}
			allowed, err := other.Assess(Holdings{Known: true, Positions: []Holding{{Instrument: id, Long: 2}}})
			if err != nil || allowed.HasBlockingHoldings || !allowed.Contracts[0].ExitByClose {
				t.Fatalf("机构未使用自己的显式期限 report=%+v err=%v", allowed, err)
			}
		}
	}
}

// TestLifecycleCopiesAndPreserves 构造后修改输入不能更改规则，已知其他档位仍保留。
func TestLifecycleCopiesAndPreserves(t *testing.T) {
	base, plan, id := scheduleFixture(t)
	inst, _ := base.Instrument(id)
	fees, _ := base.CommissionRates(id, types.Speculation)
	mr, _ := base.MarginRates(id, types.Speculation)
	var err error
	base, err = refdata.NewBuilder(1).AddInstrument(inst).AddMarginRates(id, types.Speculation, mr).AddCommissionRates(id, types.Speculation, fees).AddMarginRates(id, types.Hedge, refdata.MarginRates{LongByMoney: money("0.05"), ShortByMoney: money("0.05")}).AddCommissionRates(id, types.Hedge, refdata.CommissionRates{OpenByVolume: money("2")}).Build()
	if err != nil {
		t.Fatal(err)
	}
	rules, err := NewDailyRules(base, plan, 20261028, 2)
	if err != nil {
		t.Fatal(err)
	}
	plan.Contracts[0].MarginSteps[1].Rates.LongByMoney = money("9")
	plan.Contracts[0].LastHoldingDay = 20261027
	*base = refdata.Snapshot{}
	rates, err := rules.MarginRates(id, types.Speculation)
	if err != nil || !rates.LongByMoney.Equal(money("0.2")) {
		t.Fatalf("生命周期规则被外部参数改写 rates=%+v err=%v", rates, err)
	}
	rates, err = rules.MarginRates(id, types.Hedge)
	if err != nil || !rates.LongByMoney.Equal(money("0.05")) {
		t.Fatalf("其他档位保证金丢失 rates=%+v err=%v", rates, err)
	}
	fee, err := rules.CommissionRates(id, types.Hedge)
	if err != nil || !fee.OpenByVolume.Equal(money("2")) {
		t.Fatalf("其他档位费用丢失 rates=%+v err=%v", fee, err)
	}
	if err = rules.CheckOpening(id); err != nil {
		t.Fatalf("复制后期限被输入修改 err=%v", err)
	}
	ids := rules.ProductInstruments(id.Exchange, id.Product)
	if len(ids) != 1 {
		t.Fatal("品种合约丢失")
	}
	ids[0] = types.InstrumentID{}
	if rules.ProductInstruments(id.Exchange, id.Product)[0] != id {
		t.Fatal("品种索引暴露内部切片")
	}
}

// TestLifecycleStoppedBase 不可交易的基准不能仅因尚未到期就获得独立开仓许可。
func TestLifecycleStoppedBase(t *testing.T) {
	base, plan, id := scheduleFixture(t)
	inst, _ := base.Instrument(id)
	inst.IsTrading = false
	fees, _ := base.CommissionRates(id, types.Speculation)
	rates, _ := base.MarginRates(id, types.Speculation)
	base, err := refdata.NewBuilder(1).AddInstrument(inst).AddMarginRates(id, types.Speculation, rates).AddCommissionRates(id, types.Speculation, fees).Build()
	if err != nil {
		t.Fatal(err)
	}
	rules, err := NewDailyRules(base, plan, 20261027, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err = rules.CheckOpening(id); err == nil {
		t.Fatal("基准已停止交易仍获得开仓许可")
	}
	report, err := rules.Assess(Holdings{Known: true})
	if err != nil || report.Contracts[0].Tradable || report.Contracts[0].Expired {
		t.Fatalf("基准不可交易被误报为到期 report=%+v err=%v", report, err)
	}
}

// TestLifecycleDuplicateContract 使用两合约基准隔开数量检查，独立检出清单重复。
func TestLifecycleDuplicateContract(t *testing.T) {
	base, plan, id := scheduleFixture(t)
	inst, _ := base.Instrument(id)
	fees, _ := base.CommissionRates(id, types.Speculation)
	rates, _ := base.MarginRates(id, types.Speculation)
	b := refdata.NewBuilder(1).AddInstrument(inst).AddMarginRates(id, types.Speculation, rates).AddCommissionRates(id, types.Speculation, fees)
	other := inst
	other.ID.Month = 11
	b.AddInstrument(other).AddMarginRates(other.ID, types.Speculation, rates).AddCommissionRates(other.ID, types.Speculation, fees)
	base, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	plan.Contracts = append(plan.Contracts, plan.Contracts[0])
	if rules, err := NewDailyRules(base, plan, 20261027, 2); err == nil || rules != nil || !strings.Contains(err.Error(), "合约重复") {
		t.Fatalf("重复合约未由唯一性检查拒绝 rules=%v err=%v", rules, err)
	}
}

// TestLifecycleBaseBudget 未使用的基准档位仍会进入持久化，不能绕过表示规模限制。
func TestLifecycleBaseBudget(t *testing.T) {
	base, plan, id := scheduleFixture(t)
	inst, _ := base.Instrument(id)
	fees, _ := base.CommissionRates(id, types.Speculation)
	rates, _ := base.MarginRates(id, types.Speculation)
	base, err := refdata.NewBuilder(1).AddInstrument(inst).AddMarginRates(id, types.Speculation, rates).AddCommissionRates(id, types.Speculation, fees).AddMarginRates(id, types.Hedge, refdata.MarginRates{LongByMoney: decimal.New(1, 1000)}).Build()
	if err != nil {
		t.Fatal(err)
	}
	if rules, err := NewDailyRules(base, plan, 20261027, 2); err == nil || rules != nil {
		t.Fatal("无界基准保证金进入生命周期快照")
	}
}

// TestLifecycleRejectsIncomplete 不完整/错序/未知规则与超范围不得构造可用视图。
func TestLifecycleRejectsIncomplete(t *testing.T) {
	for _, kind := range []string{"nil-base", "version-zero", "same-version", "before", "after", "invalid-day", "coverage", "unknown-investor", "invalid-investor", "source", "missing-contract", "unknown-contract", "trade-date", "holding-date", "holding-after-trade", "missing-step", "first-step", "step-duplicate", "step-after", "future-negative", "future-unbounded"} {
		t.Run(kind, func(t *testing.T) {
			base, plan, id := scheduleFixture(t)
			day := types.TradingDay(20261027)
			version := int64(2)
			switch kind {
			case "nil-base":
				base = nil
			case "version-zero":
				version = 0
			case "same-version":
				version = 1
			case "before":
				day = 20261026
			case "after":
				day = 20261031
			case "invalid-day":
				day = 20260230
			case "coverage":
				plan.First, plan.Last = 20261030, 20261027
			case "unknown-investor":
				plan.Investor = InvestorUnknown
			case "invalid-investor":
				plan.Investor = 99
			case "source":
				plan.Source = " \t"
			case "missing-contract":
				plan.Contracts = nil
			case "unknown-contract":
				plan.Contracts[0].Instrument.Month = 11
			case "trade-date":
				plan.Contracts[0].LastTradingDay = 20261028
			case "holding-date":
				plan.Contracts[0].LastHoldingDay = 0
			case "holding-after-trade":
				plan.Contracts[0].LastHoldingDay = 20261030
			case "missing-step":
				plan.Contracts[0].MarginSteps = nil
			case "first-step":
				plan.Contracts[0].MarginSteps[0].From = 20261028
			case "step-duplicate":
				plan.Contracts[0].MarginSteps[1].From = 20261027
			case "step-after":
				plan.Contracts[0].MarginSteps[1].From = 20261031
			case "future-negative":
				plan.Contracts[0].MarginSteps[1].Rates.LongByMoney = money("-0.1")
			case "future-unbounded":
				plan.Contracts[0].MarginSteps[1].Rates.LongByMoney = decimal.New(1, 1000)
			}
			if rules, err := NewDailyRules(base, plan, day, version); err == nil || rules != nil {
				t.Fatalf("未知或矛盾生命周期资料被接受 kind=%s id=%v err=%v", kind, id, err)
			}
		})
	}
	base, plan, id := scheduleFixture(t)
	rules, err := NewDailyRules(base, plan, 20261027, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, positions := range [][]Holding{{{Instrument: id, Long: -1}}, {{Instrument: id}, {Instrument: id}}, {{Instrument: types.InstrumentID{Exchange: types.DCE, Product: "m", Year: 2027, Month: 1}, Long: 1}}} {
		if report, err := rules.Assess(Holdings{Known: true, Positions: positions}); err == nil || report.Day != 0 {
			t.Fatalf("未知/重复/负持仓被接受 positions=%+v err=%v", positions, err)
		}
	}
	if err = rules.CheckOpening(types.InstrumentID{}); err == nil || !strings.Contains(err.Error(), "缺合约") {
		t.Fatal("未知合约获得开仓许可")
	}
}
