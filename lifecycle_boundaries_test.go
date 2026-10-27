package futsim

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/lifecycle"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestLifecycleMonthAndYearBoundary 的日期全部合成，包含现实可能休市的日子，不冒充交易安排。
func TestLifecycleMonthAndYearBoundary(t *testing.T) {
	for _, c := range []struct {
		name, symbol                   string
		first, day, next, last, expiry types.TradingDay
	}{
		{"month", "SHFE.rb2610", 20260929, 20260930, 20261001, 20261030, 20261029},
		{"year", "SHFE.rb2701", 20261230, 20261231, 20270101, 20270103, 20270102},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, base, plan, oldID := lifecycleFixture(t)
			inst, _ := base.Instrument(oldID)
			rates, _ := base.MarginRates(oldID, types.Speculation)
			fees, _ := base.CommissionRates(oldID, types.Speculation)
			id, err := types.ParseSymbol(c.symbol, c.day)
			if err != nil {
				t.Fatal(err)
			}
			inst.ID, inst.ExpireDate = id, c.expiry
			base, err = refdata.NewBuilder(1).AddInstrument(inst).AddMarginRates(id, types.Speculation, rates).AddCommissionRates(id, types.Speculation, fees).Build()
			if err != nil {
				t.Fatal(err)
			}
			plan.First, plan.Last, plan.Source = c.first, c.last, "synthetic-month-year-boundary"
			contract := &plan.Contracts[0]
			contract.Instrument, contract.LastTradingDay, contract.LastHoldingDay = id, c.expiry, c.expiry
			contract.MarginSteps[0].From, contract.MarginSteps[1].From = c.first, c.next
			current, err := lifecycle.NewDailyRules(base, plan, c.day, 2)
			if err != nil {
				t.Fatal(err)
			}
			next, err := lifecycle.NewDailyRules(base, plan, c.next, 3)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Day, cfg.Rules = c.day, current
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			markOn(t, s, c.day, c.symbol, "100", "100")
			if err = s.ApplyTrade(c.day, trade(t, c.symbol, types.Buy, types.Open, "100", 1)); err != nil {
				t.Fatal(err)
			}
			if !s.Account().CurrMargin.Equal(dec("120")) {
				t.Fatalf("跨月/跨年旧日提前套用新台阶 case=%s margin=%s", c.name, s.Account().CurrMargin)
			}
			before := lifecycleState(t, s)
			bad, err := lifecycle.NewDailyRules(base, plan, c.day, 4)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.SettleWithRules(c.day, settlePx(t, c.symbol, "110"), c.day, bad); err == nil || !strings.Contains(err.Error(), "不晚于") {
				t.Fatalf("跨月/跨年同日重复结算被接受 case=%s err=%v", c.name, err)
			}
			if lifecycleState(t, s) != before {
				t.Fatal("跨月/跨年拒绝改变完整状态")
			}
			report, err := s.SettleWithRules(c.day, settlePx(t, c.symbol, "110"), c.next, next)
			if err != nil || report.Day != c.next || report.Contracts[0].MarginFrom != c.next || report.HasBlockingHoldings || !s.Account().PreBalance.Equal(dec("1099")) || !s.Account().CurrMargin.Equal(dec("253")) {
				t.Fatalf("跨月/跨年跨日手算不符 case=%s err=%v report=%+v", c.name, err, report)
			}
			st, err := s.State()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Day, cfg.Rules = c.next, next
			restored, err := Restore(cfg, st)
			if err != nil || lifecycleState(t, restored) != lifecycleState(t, s) {
				t.Fatalf("跨月/跨年恢复不等价 case=%s err=%v", c.name, err)
			}
		})
	}
}
