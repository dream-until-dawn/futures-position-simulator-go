package main

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// preflightFake 只实现查询接口，故障按步骤注入，不能报单。
type preflightFake struct {
	fail      string
	spec      *def.CThostFtdcInstrumentField
	rates     *def.CThostFtdcInstrumentCommissionRateField
	positions map[string]*def.CThostFtdcInvestorPositionField
	orders    []*def.CThostFtdcOrderField
	account   *def.CThostFtdcTradingAccountField
}

func (f *preflightFake) TradingDay() string { return "20261023" }
func (f *preflightFake) failure(step string) error {
	if f.fail == step {
		return errors.New("注入查询失败")
	}
	return nil
}
func (f *preflightFake) Instrument(string, time.Duration) (*def.CThostFtdcInstrumentField, error) {
	return f.spec, f.failure("instrument")
}
func (f *preflightFake) CommissionRate(string, time.Duration) (*def.CThostFtdcInstrumentCommissionRateField, error) {
	return f.rates, f.failure("rates")
}
func (f *preflightFake) Positions(time.Duration) (map[string]*def.CThostFtdcInvestorPositionField, error) {
	return f.positions, f.failure("positions")
}
func (f *preflightFake) UnfinishedOrders(time.Duration) ([]*def.CThostFtdcOrderField, error) {
	return f.orders, f.failure("orders")
}
func (f *preflightFake) Account(time.Duration) (*def.CThostFtdcTradingAccountField, error) {
	return f.account, f.failure("account")
}

func newPreflightFake() *preflightFake {
	f := &preflightFake{spec: &def.CThostFtdcInstrumentField{PriceTick: 5, VolumeMultiple: 16}, rates: &def.CThostFtdcInstrumentCommissionRateField{OpenRatioByMoney: .0002, CloseRatioByMoney: .0002, CloseTodayRatioByMoney: .0004}, account: &def.CThostFtdcTradingAccountField{Available: 100000}}
	copy(f.spec.ExchangeID[:], "DCE")
	f.spec.IsTrading = 1
	f.rates.InvestorRange = def.THOST_FTDC_IR_All
	copy(f.spec.InstrumentID[:], "lh2701")
	copy(f.spec.ExpireDate[:], "20270126")
	copy(f.rates.InstrumentID[:], "lh2701")
	return f
}

// TestRoundingPreflightStages 空仓、种子待结算、已有昨仓三种情况必须区别，均不得变成下单许可。
func TestRoundingPreflightStages(t *testing.T) {
	for _, c := range []struct {
		today, history, opened int
		stage                  string
	}{{0, 0, 0, "empty-needs-seed-plan"}, {1, 0, 1, "seed-awaiting-settlement"}, {0, 1, 0, "history-seed-needs-experiment-authorization"}, {0, 0, 1, "blocked"}, {1, 1, 1, "blocked"}} {
		f := newPreflightFake()
		p := &def.CThostFtdcInvestorPositionField{PosiDirection: def.THOST_FTDC_PD_Long, PositionDate: def.THOST_FTDC_PSD_Today, HedgeFlag: def.THOST_FTDC_HF_Speculation, Position: def.TThostFtdcVolumeType(c.today + c.history), TodayPosition: def.TThostFtdcVolumeType(c.today), OpenVolume: def.TThostFtdcVolumeType(c.opened)}
		copy(p.InstrumentID[:], "lh2701")
		copy(p.ExchangeID[:], "DCE")
		f.positions = map[string]*def.CThostFtdcInvestorPositionField{"p": p}
		got, err := collectRoundingPreflight(f, "DCE.lh2701", time.Second)
		if err != nil || got.Stage != c.stage {
			t.Fatalf("阶段判错：%+v %v", got, err)
		}
		if len(got.Requires) == 0 || !strings.Contains(strings.Join(got.Requires, " "), "授权") {
			t.Fatal("报告遗漏下单授权边界")
		}
	}
}

// TestRoundingPreflightFailureAndRedaction 任意查询失败不得给出成功报告，原始身份字段不许进入输出。
func TestRoundingPreflightFailureAndRedaction(t *testing.T) {
	for _, step := range []string{"instrument", "rates", "positions", "orders", "account"} {
		f := newPreflightFake()
		f.fail = step
		if got, err := collectRoundingPreflight(f, "DCE.lh2701", time.Second); err == nil || got != nil {
			t.Fatalf("查询失败被报告为成功：%s", step)
		}
	}
	f := newPreflightFake()
	copy(f.rates.InvestorID[:], "secret-investor")
	copy(f.account.AccountID[:], "secret-account")
	got, err := collectRoundingPreflight(f, "DCE.lh2701", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-") || strings.Contains(string(raw), "InvestorID") || strings.Contains(string(raw), "AccountID") {
		t.Fatal("前置报告泄漏原始身份字段")
	}
	f.spec.PriceTick = def.TThostFtdcPriceType(math.NaN())
	if _, err = collectRoundingPreflight(f, "DCE.lh2701", time.Second); err == nil {
		t.Fatal("接受无效声明数值")
	}
	f = newPreflightFake()
	f.orders = []*def.CThostFtdcOrderField{{}}
	got, err = collectRoundingPreflight(f, "DCE.lh2701", time.Second)
	if err != nil || got.Stage != "blocked" {
		t.Fatal("有委托未阻断")
	}
}

// TestRoundingPreflightRejectsInvalidInputs 无效声明与缺失记录不可退化成“空仓可登记”。
func TestRoundingPreflightRejectsInvalidInputs(t *testing.T) {
	for _, change := range []func(*preflightFake){
		func(f *preflightFake) { f.spec = nil },
		func(f *preflightFake) { f.rates = nil },
		func(f *preflightFake) { f.account = nil },
		func(f *preflightFake) { f.spec.PriceTick = def.TThostFtdcPriceType(math.MaxFloat64) },
		func(f *preflightFake) { copy(f.spec.ExpireDate[:], "20251023") },
		func(f *preflightFake) { f.positions = map[string]*def.CThostFtdcInvestorPositionField{"bad": nil} },
		func(f *preflightFake) { f.orders = []*def.CThostFtdcOrderField{nil} },
	} {
		f := newPreflightFake()
		change(f)
		if got, err := collectRoundingPreflight(f, "DCE.lh2701", time.Second); err == nil || got != nil {
			t.Fatal("无效输入产生成功报告")
		}
	}
	f := newPreflightFake()
	f.rates.CloseTodayRatioByVolume = 1
	if got, err := collectRoundingPreflight(f, "DCE.lh2701", time.Second); err != nil || got.Stage != "blocked" {
		t.Fatal("混合费率未阻断")
	}
}
