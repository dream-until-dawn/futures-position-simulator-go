package futsim

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/lifecycle"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// lifecycleFixture 的期限、台阶和合约参数全部合成，不是实际rb交割规则。
func lifecycleFixture(t *testing.T) (Config, *refdata.Snapshot, lifecycle.Schedule, types.InstrumentID) {
	t.Helper()
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2026, Month: 10}
	inst := refdata.Instrument{ID: id, VolumeMultiple: dec("10"), PriceTick: dec("1"), PositionDateType: refdata.UseHistory, ExpireDate: 20261029, IsTrading: true, MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000, HasPriceLimitRatio: true, PriceLimitRatio: dec("0.1")}
	first := refdata.MarginRates{LongByMoney: dec("0.1"), ShortByMoney: dec("0.1"), CompanyAddOn: dec("0.02")}
	base, err := refdata.NewBuilder(1).AddInstrument(inst).AddMarginRates(id, types.Speculation, first).AddCommissionRates(id, types.Speculation, refdata.CommissionRates{OpenByVolume: dec("1"), CloseByVolume: dec("2"), CloseTodayByVolume: dec("3")}).Build()
	if err != nil {
		t.Fatal(err)
	}
	plan := lifecycle.Schedule{First: 20261027, Last: 20261030, Investor: lifecycle.Institution, Source: "synthetic-only", Contracts: []lifecycle.Contract{{Instrument: id, LastTradingDay: 20261029, LastHoldingDay: 20261029, MarginSteps: []lifecycle.MarginStep{{From: 20261027, Rates: first}, {From: 20261028, Rates: refdata.MarginRates{LongByMoney: dec("0.2"), ShortByMoney: dec("0.2"), CompanyAddOn: dec("0.03")}}}}}}
	rules, err := lifecycle.NewDailyRules(base, plan, 20261027, 2)
	if err != nil {
		t.Fatal(err)
	}
	return Config{Day: 20261027, PreBalance: dec("1000"), Choices: ctpChoices(), Rules: rules}, base, plan, id
}

// lifecycleState 对完整可恢复状态取规范JSON，避免只比较余额漏掉明细或版本改变。
func lifecycleState(t *testing.T, s *Simulator) string {
	t.Helper()
	state, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestLifecycleAtomicStepAndRestore 手算跨日权益和新日占用，核对日期绑定与失败不动状态。
func TestLifecycleAtomicStepAndRestore(t *testing.T) {
	cfg, base, plan, id := lifecycleFixture(t)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Mark(cfg.Day, Quote{Instrument: id, Last: dec("100"), HasLast: true, PreSettlement: dec("100"), HasPreSettlement: true}); err != nil {
		t.Fatal(err)
	}
	if err = s.ApplyTrade(cfg.Day, trade(t, "SHFE.rb2610", types.Buy, types.Open, "100", 1)); err != nil {
		t.Fatal(err)
	}
	if !s.Account().CurrMargin.Equal(dec("120")) || !s.Account().Commission.Equal(dec("1")) {
		t.Fatalf("当日基准错误 %+v", s.Account())
	}
	before := lifecycleState(t, s)
	if err = s.Settle(cfg.Day, settlePx(t, "SHFE.rb2610", "110"), 20261028); err == nil || !strings.Contains(err.Error(), "SettleWithRules") {
		t.Fatalf("普通结算绕过绑定 err=%v", err)
	}
	if lifecycleState(t, s) != before {
		t.Fatal("普通结算失败改动完整状态")
	}
	// 次日手续费也换一份明确基准；原日已经收取的费用不能被新基准追溯改写。
	inst, _ := base.Instrument(id)
	rates, _ := base.MarginRates(id, types.Speculation)
	nextBase, err := refdata.NewBuilder(100).AddInstrument(inst).AddMarginRates(id, types.Speculation, rates).AddCommissionRates(id, types.Speculation, refdata.CommissionRates{OpenByVolume: dec("7"), CloseByVolume: dec("2"), CloseTodayByVolume: dec("3")}).Build()
	if err != nil {
		t.Fatal(err)
	}
	next, err := lifecycle.NewDailyRules(nextBase, plan, 20261028, 3)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.SettleWithRules(cfg.Day, settlePx(t, "SHFE.rb2610", "110"), 20261028, next)
	if err != nil || report.Day != 20261028 || report.HasBlockingHoldings {
		t.Fatalf("正常规则切换失败 report=%+v err=%v", report, err)
	}
	a := s.Account()
	// 当日手续费1、兑现盈亏100，次日公司占用110×10×(.2+.03)=253。
	if !a.PreBalance.Equal(dec("1099")) || !a.CurrMargin.Equal(dec("253")) || !a.ExchangeMargin.Equal(dec("220")) || !a.Commission.IsZero() {
		t.Fatalf("跨日台阶或权益错误 %+v", a)
	}
	if err = s.ApplyTrade(20261028, trade(t, "SHFE.rb2610", types.Buy, types.Open, "110", 1)); err != nil {
		t.Fatal(err)
	}
	if !s.Account().Commission.Equal(dec("7")) || !s.Account().CurrMargin.Equal(dec("506")) {
		t.Fatalf("次日未使用新基准 %+v", s.Account())
	}
	state, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Day, cfg.Rules = 20261028, next
	restored, err := Restore(cfg, state)
	if err != nil || lifecycleState(t, restored) != lifecycleState(t, s) {
		t.Fatalf("新配置恢复不等价 err=%v", err)
	}
	cfg.Rules = base
	if _, err = Restore(cfg, state); err == nil {
		t.Fatal("旧版本恢复成功")
	}
	cfg.Rules, err = lifecycle.NewDailyRules(base, plan, 20261027, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = New(cfg); err == nil {
		t.Fatal("旧日规则开启新日账户")
	}
}

// TestLifecycleSettlementFailures 每个失败均核完整状态与失效标志，不能把半提交留给调用者。
func TestLifecycleSettlementFailures(t *testing.T) {
	for _, kind := range []string{"nil", "zero", "wrong-day", "same-version", "missing-price", "zero-price", "unbounded-price", "multiplier", "tick", "date-type", "pending", "frozen", "backward"} {
		t.Run(kind, func(t *testing.T) {
			cfg, base, plan, id := lifecycleFixture(t)
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			markOn(t, s, cfg.Day, "SHFE.rb2610", "100", "100")
			if err = s.ApplyTrade(cfg.Day, trade(t, "SHFE.rb2610", types.Buy, types.Open, "100", 1)); err != nil {
				t.Fatal(err)
			}
			day, version := types.TradingDay(20261028), int64(3)
			nextDay := day
			prices := settlePx(t, "SHFE.rb2610", "110")
			want := ""
			switch kind {
			case "wrong-day":
				day = 20261029
				want = "日期或版本"
			case "same-version":
				version = 2
				want = "日期或版本"
			case "missing-price":
				prices = nil
				want = "没有今结算价"
			case "zero-price":
				prices[id] = decimal.Zero
				want = "不为正"
			case "unbounded-price":
				prices[id] = decimal.New(1, 1000)
				want = "有限精度"
			case "backward":
				day = cfg.Day
				nextDay = cfg.Day
				want = "不晚于"
			case "pending":
				if _, err = s.PlaceAccepted(cfg.Day, "pending", order.Request{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: dec("100"), Volume: 1}); err != nil {
					t.Fatal(err)
				}
				want = "还有 1 笔挂单"
			case "frozen":
				if err = s.acc.Freeze(cfg.Day, dec("1"), decimal.Zero); err != nil {
					t.Fatal(err)
				}
				want = "冻结"
			case "multiplier", "tick", "date-type":
				inst, _ := base.Instrument(id)
				rates, _ := base.MarginRates(id, types.Speculation)
				fees, _ := base.CommissionRates(id, types.Speculation)
				if kind == "multiplier" {
					inst.VolumeMultiple = dec("20")
				}
				if kind == "tick" {
					inst.PriceTick = dec("2")
				}
				if kind == "date-type" {
					inst.PositionDateType = refdata.NoUseHistory
				}
				base, err = refdata.NewBuilder(1).AddInstrument(inst).AddMarginRates(id, types.Speculation, rates).AddCommissionRates(id, types.Speculation, fees).Build()
				if err != nil {
					t.Fatal(err)
				}
				want = "不能静默迁移"
			}
			next, err := lifecycle.NewDailyRules(base, plan, day, version)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "nil" {
				next = nil
				want = "日期或版本"
			}
			if kind == "zero" {
				next = &lifecycle.DailyRules{}
				want = "日期或版本"
			}
			before := lifecycleState(t, s)
			if _, err = s.SettleWithRules(cfg.Day, prices, nextDay, next); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("切换反例未由目标守卫拒绝 kind=%s err=%v want=%s", kind, err, want)
			}
			if lifecycleState(t, s) != before {
				t.Fatalf("切换失败改变完整状态 kind=%s", kind)
			}
			if s.broken != nil {
				t.Fatalf("切换失败使原实例失效 kind=%s", kind)
			}
		})
	}
}

// TestLifecycleOpeningAndFacts 原八项检查成功后仍禁止超期新开；实际已发生事实不能丢掉。
func TestLifecycleOpeningAndFacts(t *testing.T) {
	cfg, base, plan, id := lifecycleFixture(t)
	plan.Investor = lifecycle.Individual
	plan.Contracts[0].LastHoldingDay = 20261028
	rules, err := lifecycle.NewDailyRules(base, plan, 20261029, 4)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Day, cfg.Rules = 20261029, rules
	cfg.Calendar, err = refdata.NewCalendar([]types.TradingDay{20261029, 20261030}, []refdata.SessionTable{{Exchange: types.SHFE, Product: "rb", Day: []refdata.Session{{Start: refdata.MustClockTime(9, 0, 0), End: refdata.MustClockTime(15, 0, 0)}}}}, []int32{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.TickRounding = MeasuredTickRounding()
	cfg.PositionLimits = map[types.InstrumentID]int{id: 100}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	markOn(t, s, cfg.Day, "SHFE.rb2610", "100", "100")
	at := time.Date(2026, 10, 29, 10, 0, 0, 0, refdata.CNZone())
	request := order.Request{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: dec("100"), Volume: 1}
	before := lifecycleState(t, s)
	if _, err = s.Submit(cfg.Day, at, request); err == nil || !strings.Contains(err.Error(), "不许新增开仓") {
		t.Fatalf("Submit绕过生命周期开仓守卫 err=%v", err)
	}
	if _, err = s.Place(cfg.Day, at, "denied", request); err == nil || !strings.Contains(err.Error(), "不许新增开仓") {
		t.Fatalf("Place绕过生命周期开仓守卫 err=%v", err)
	}
	if lifecycleState(t, s) != before {
		t.Fatal("开仓拒绝改变完整状态")
	}
	if err = s.ApplyTrade(cfg.Day, trade(t, "SHFE.rb2610", types.Buy, types.Open, "100", 1)); err != nil {
		t.Fatalf("已发生开仓事实被丢弃 err=%v", err)
	}
	before = lifecycleState(t, s)
	report, err := s.AssessLifecycle()
	if err != nil || !report.HasBlockingHoldings || report.Contracts[0].Long != 1 {
		t.Fatalf("越期持仓事实被抹去 report=%+v err=%v", report, err)
	}
	if lifecycleState(t, s) != before {
		t.Fatal("只读生命周期评估改变完整状态")
	}
	// 柜台已经接受的挂单属于事实输入；本地新报单守卫不应用来丢掉它。
	if _, err = s.PlaceAccepted(cfg.Day, "accepted-fact", order.Request{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: dec("100"), Volume: 1}); err != nil {
		t.Fatalf("已接受挂单事实被丢弃 err=%v", err)
	}
	if err = s.Cancel(cfg.Day, "accepted-fact"); err != nil {
		t.Fatal(err)
	}
	request.Direction, request.Offset = types.Sell, types.CloseToday
	if _, err = s.Submit(cfg.Day, at, request); err != nil {
		t.Fatalf("最后交易日前减仓被拒绝 err=%v", err)
	}
	if report, err = s.AssessLifecycle(); err != nil || report.HasBlockingHoldings || report.Contracts[0].Long != 0 {
		t.Fatalf("实际减仓后仍误报仓位 report=%+v err=%v", report, err)
	}
	next, err := lifecycle.NewDailyRules(base, plan, 20261030, 5)
	if err != nil {
		t.Fatal(err)
	}
	if report, err = s.SettleWithRules(cfg.Day, nil, 20261030, next); err != nil || !report.Contracts[0].Expired {
		t.Fatalf("到期空仓切换失败 report=%+v err=%v", report, err)
	}
	request.Direction, request.Offset = types.Buy, types.Open
	if err = next.CheckOpening(id); err == nil {
		t.Fatal("到期空仓仍允许新开")
	}
	var zero Simulator
	var missing *Simulator
	for _, invalid := range []*Simulator{&zero, missing, newSim(t)} {
		if _, err = invalid.AssessLifecycle(); err == nil {
			t.Fatal("缺生命周期规则仍给出健康报告")
		}
	}
	s.broken = fmt.Errorf("synthetic incomplete update")
	if _, err = s.AssessLifecycle(); err == nil {
		t.Fatal("失效模拟器仍评估生命周期")
	}
}

// TestLifecycleDeadlineAtomic 期限阻断时完整状态不变，不能通过切换投资者资格绕过。
func TestLifecycleDeadlineAtomic(t *testing.T) {
	cfg, base, plan, _ := lifecycleFixture(t)
	plan.Investor = lifecycle.Individual
	plan.Contracts[0].LastHoldingDay = 20261027
	current, err := lifecycle.NewDailyRules(base, plan, cfg.Day, 2)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Rules = current
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	markOn(t, s, cfg.Day, "SHFE.rb2610", "100", "100")
	if err = s.ApplyTrade(cfg.Day, trade(t, "SHFE.rb2610", types.Buy, types.Open, "100", 1)); err != nil {
		t.Fatal(err)
	}
	before := lifecycleState(t, s)
	next, err := lifecycle.NewDailyRules(base, plan, 20261028, 3)
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.SettleWithRules(cfg.Day, settlePx(t, "SHFE.rb2610", "110"), 20261028, next)
	if err == nil || !report.HasBlockingHoldings || !strings.Contains(err.Error(), "持仓期限") {
		t.Fatalf("越期仓位未阻断 report=%+v err=%v", report, err)
	}
	if lifecycleState(t, s) != before {
		t.Fatal("期限拒绝改动完整状态")
	}
	plan.Investor = lifecycle.Institution
	plan.Contracts[0].LastHoldingDay = 20261029
	next, err = lifecycle.NewDailyRules(base, plan, 20261028, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SettleWithRules(cfg.Day, settlePx(t, "SHFE.rb2610", "110"), 20261028, next); err == nil || !strings.Contains(err.Error(), "资格") {
		t.Fatalf("跨日换资格绕过期限 err=%v", err)
	}
	if lifecycleState(t, s) != before {
		t.Fatal("资格拒绝改动完整状态")
	}
}
