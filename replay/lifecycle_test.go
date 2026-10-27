package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/lifecycle"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/risk"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func lifecycleDemo(t *testing.T) Payload {
	t.Helper()
	d := decimal.RequireFromString
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2026, Month: 10}
	cal, err := refdata.NewCalendar([]types.TradingDay{20261027, 20261028, 20261029, 20261030}, []refdata.SessionTable{{Exchange: types.SHFE, Product: "rb", Day: []refdata.Session{{Start: refdata.MustClockTime(9, 0, 0), End: refdata.MustClockTime(15, 0, 0)}}}}, []int32{})
	if err != nil {
		t.Fatal(err)
	}
	plan := lifecycle.Schedule{First: 20261027, Last: 20261030, Investor: lifecycle.Individual, Source: "synthetic-only", Contracts: []lifecycle.Contract{{Instrument: id, LastTradingDay: 20261029, LastHoldingDay: 20261029, MarginSteps: []lifecycle.MarginStep{{From: 20261027, Rates: refdata.MarginRates{LongByMoney: d("0.1"), ShortByMoney: d("0.1"), CompanyAddOn: d("0.02")}}, {From: 20261028, Rates: refdata.MarginRates{LongByMoney: d("0.2"), ShortByMoney: d("0.2"), CompanyAddOn: d("0.03")}}, {From: 20261029, Rates: refdata.MarginRates{LongByMoney: d("0.3"), ShortByMoney: d("0.3"), CompanyAddOn: d("0.03")}}}}}}
	packet := func(day types.TradingDay, version int64, openFee string) json.RawMessage {
		inst := refdata.Instrument{ID: id, VolumeMultiple: d("10"), PriceTick: d("1"), PositionDateType: refdata.UseHistory, ExpireDate: 20261029, IsTrading: true, MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000, HasPriceLimitRatio: true, PriceLimitRatio: d("0.1")}
		base, err := refdata.NewBuilder(100).AddInstrument(inst).AddMarginRates(id, types.Speculation, plan.Contracts[0].MarginSteps[0].Rates).AddCommissionRates(id, types.Speculation, refdata.CommissionRates{OpenByVolume: d(openFee), CloseByVolume: d("2"), CloseTodayByVolume: d("3")}).Build()
		if err != nil {
			t.Fatal(err)
		}
		rules, err := lifecycle.NewDailyRules(base, plan, day, version)
		if err != nil {
			t.Fatal(err)
		}
		var raw bytes.Buffer
		if err = rules.Save(&raw, refdata.EvidencePlaceholder, "synthetic-base", time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC), cal); err != nil {
			t.Fatal(err)
		}
		return raw.Bytes()
	}
	choices := futsim.CTPChoices()
	choices.FeeRounding = fee.NoRounding
	p := Payload{Lifecycle: packet(20261027, 2, "1"), Settings: Settings{Day: 20261027, PreBalance: d("1000"), Choices: choices, TickRounding: []TickRule{{Exchange: types.SHFE, Rounding: refdata.TickFloor}}, PositionLimits: []Limit{{Instrument: id, Volume: 100}}}}
	add := func(e Event) { e.ID = fmt.Sprintf("life-%02d", len(p.Events)); p.Events = append(p.Events, e) }
	trade := func(day types.TradingDay, side types.Direction, offset types.Offset, price string) {
		add(Event{Day: day, Kind: "trade", Trade: &match.Trade{Instrument: id, Direction: side, Offset: offset, Hedge: types.Speculation, Price: d(price), Volume: 1}})
	}
	settle := func(day, next types.TradingDay, price string, raw json.RawMessage) {
		add(Event{Day: day, Kind: "settle", Settlement: &Settlement{NextDay: next, Prices: []Price{{Instrument: id, Value: d(price)}}, NextRules: raw}})
	}
	policy := risk.Policy{MaxRiskRatio: d("0.4"), MinEquity: d("1"), MinAvailable: d("0")}
	add(Event{Day: 20261027, Kind: "mark", Quote: &futsim.Quote{Instrument: id, Last: d("100"), HasLast: true, PreSettlement: d("100"), HasPreSettlement: true}})
	trade(20261027, types.Buy, types.Open, "100")
	settle(20261027, 20261028, "110", packet(20261028, 3, "7"))
	trade(20261028, types.Buy, types.Open, "110")
	add(Event{Day: 20261028, Kind: "risk", RiskPolicy: &policy})
	deposit := d("173")
	add(Event{Day: 20261028, Kind: "deposit", Amount: &deposit})
	add(Event{Day: 20261028, Kind: "risk", RiskPolicy: &policy})
	trade(20261028, types.Sell, types.CloseToday, "110")
	settle(20261028, 20261029, "120", packet(20261029, 4, "9"))
	add(Event{Day: 20261029, Kind: "risk", RiskPolicy: &policy})
	trade(20261029, types.Sell, types.CloseYesterday, "120")
	settle(20261029, 20261030, "130", packet(20261030, 5, "9"))
	add(Event{Day: 20261030, Kind: "risk", RiskPolicy: &policy})
	return p
}

// TestReplayLifecycleLedger 三个合成交易日、三档费率、显式入金及到期空仓独立手算。
func TestReplayLifecycleLedger(t *testing.T) {
	p := lifecycleDemo(t)
	before, _ := json.Marshal(p)
	if _, err := Run(p, Options{}); err == nil {
		t.Fatal("生命周期回放默认允许占位规则")
	}
	packet, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(packet))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Run(decoded, Options{AllowPlaceholder: true})
	if err != nil {
		t.Fatalf("生命周期回放闭环失败: %v", err)
	}
	if len(r.Frames) != 13 || r.InitialRules == nil || r.Final.RulesVersion != 5 || r.Final.Day != 20261030 || !r.Final.Account.PreBalance.Equal(decimal.NewFromInt(1360)) {
		t.Fatalf("生命周期手算最终账本不符 final=%+v frames=%d", r.Final, len(r.Frames))
	}
	if !r.Frames[2].Account.PreBalance.Equal(decimal.NewFromInt(1099)) || !r.Frames[2].Account.CurrMargin.Equal(decimal.NewFromInt(253)) || !r.Frames[3].Account.Commission.Equal(decimal.NewFromInt(7)) || !r.Frames[8].Account.PreBalance.Equal(decimal.NewFromInt(1362)) || !r.Frames[8].Account.CurrMargin.Equal(decimal.NewFromInt(396)) {
		t.Fatal("跨日规则或新手续费未沿用候选配置")
	}
	if r.Frames[4].Risk == nil || !r.Frames[4].Risk.NeedsMarginCall || !r.Frames[4].Risk.RequiredDeposit.Equal(decimal.NewFromInt(173)) || r.Frames[6].Risk.NeedsMarginCall {
		t.Fatal("回放风控信号或显式补资手算错误")
	}
	for _, i := range []int{4, 6, 9, 12} {
		if !same(r.Frames[i-1].State, r.Frames[i].State) {
			t.Fatalf("风控事件改动完整状态 frame=%d", i)
		}
	}
	if r.Frames[9].Lifecycle == nil || r.Frames[12].Lifecycle == nil || !r.Frames[9].Lifecycle.Contracts[0].ExitByClose || !r.Frames[12].Lifecycle.Contracts[0].Expired || r.Frames[12].Lifecycle.Contracts[0].Long != 0 {
		t.Fatal("到期/最后持仓日告警或空仓事实丢失")
	}
	for _, i := range []int{2, 8, 11} {
		if r.Frames[i].Rules == nil || r.Frames[i].Rules.Evidence != refdata.EvidencePlaceholder {
			t.Fatal("跨日绑定丢失完整规则或证据")
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadReport(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if diff, err := Compare(r, loaded); err != nil || !diff.Equal {
		t.Fatalf("生命周期报告往返不等价 diff=%+v err=%v", diff, err)
	}
	after, _ := json.Marshal(p)
	if !bytes.Equal(before, after) {
		t.Fatal("生命周期回放改变调用方输入")
	}
}

func changeLife(t *testing.T, raw json.RawMessage, change func(map[string]any)) json.RawMessage {
	t.Helper()
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		t.Fatal(err)
	}
	change(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestReplayLifecycleFailures 事件失败返回完整有效前缀，不提交候选规则、状态或证据。
func TestReplayLifecycleFailures(t *testing.T) {
	p := lifecycleDemo(t)
	prefix := copyPayload(t, p)
	prefix.Events = prefix.Events[:2]
	expected := execute(t, prefix)
	for _, kind := range []string{"missing-next", "wrong-day", "same-version", "qualification", "holding-deadline", "calendar", "placeholder", "budget"} {
		t.Run(kind, func(t *testing.T) {
			bad := copyPayload(t, p)
			s := bad.Events[2].Settlement
			want := ""
			switch kind {
			case "missing-next":
				s.NextRules = nil
				want = "SettleWithRules"
			case "wrong-day":
				s.NextRules = changeLife(t, s.NextRules, func(m map[string]any) { m["day"] = json.Number("20261029") })
				want = "日期或日历"
			case "same-version":
				s.NextRules = changeLife(t, s.NextRules, func(m map[string]any) { m["version"] = json.Number("2") })
				want = "日期或版本"
			case "qualification":
				s.NextRules = changeLife(t, s.NextRules, func(m map[string]any) { m["schedule"].(map[string]any)["Investor"] = json.Number("2") })
				want = "资格"
			case "holding-deadline":
				s.NextRules = changeLife(t, s.NextRules, func(m map[string]any) {
					m["schedule"].(map[string]any)["Contracts"].([]any)[0].(map[string]any)["LastHoldingDay"] = json.Number("20261027")
				})
				want = "持仓期限"
			case "calendar":
				s.NextRules = changeLife(t, s.NextRules, func(m map[string]any) {
					m["base"].(map[string]any)["calendar"].(map[string]any)["trading_days"] = []any{json.Number("20261027"), json.Number("20261029"), json.Number("20261030")}
				})
				want = "严格下一交易日"
			case "placeholder":
				s.NextRules = changeLife(t, s.NextRules, func(m map[string]any) { m["base"].(map[string]any)["evidence"] = "invented" })
				want = "证据等级"
			case "budget":
				s.NextRules = changeLife(t, s.NextRules, func(m map[string]any) { m["schedule"].(map[string]any)["Source"] = strings.Repeat("x", MaxInputBytes) })
				want = "上限"
			}
			before, _ := json.Marshal(bad)
			r, err := Run(bad, Options{AllowPlaceholder: true})
			if kind == "budget" {
				if err == nil || r.Status != "failed" {
					t.Fatal("超限回放被接受")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), want) || r.FailedIndex == nil || *r.FailedIndex != 2 || len(r.Frames) != 2 || !same(r.Final, expected.Final) || !same(r.InitialRules, expected.InitialRules) {
				t.Fatalf("跨日失败前缀或规则不原子 kind=%s index=%v err=%v want=%s", kind, r.FailedIndex, err, want)
			}
			after, _ := json.Marshal(bad)
			if !bytes.Equal(before, after) {
				t.Fatalf("失败回放改动输入 kind=%s", kind)
			}
			if raw, err := json.Marshal(r); err != nil {
				t.Fatal(err)
			} else if _, err = ReadReport(bytes.NewReader(raw)); err != nil {
				t.Fatalf("失败报告前缀不可复核 kind=%s err=%v", kind, err)
			}
		})
	}
	for _, kind := range []string{"both", "neither", "wrong-risk-day", "unknown-policy"} {
		bad := copyPayload(t, p)
		if kind == "both" {
			bad.Rules = demoPayload(t).Rules
		}
		if kind == "neither" {
			bad.Lifecycle = nil
		}
		if kind == "wrong-risk-day" {
			bad.Events[4].Day = 20261027
		}
		if kind == "unknown-policy" {
			bad.Events[4].RiskPolicy.MaxRiskRatio = decimal.Zero
		}
		if r, err := Run(bad, Options{AllowPlaceholder: true}); err == nil || r.Status != "failed" {
			t.Fatalf("未知或歧义输入获得成功 kind=%s", kind)
		}
	}
}

// TestReplayLifecycleReportTampering 修改报告中的版本、资格、来源或风险结果必须被独立读者拒绝。
func TestReplayLifecycleReportTampering(t *testing.T) {
	r := execute(t, lifecycleDemo(t))
	for _, kind := range []string{"missing-initial", "unbound-version", "missing-binding", "binding-version", "evidence", "qualification", "risk-value", "risk-missing", "lifecycle-value", "lifecycle-missing"} {
		t.Run(kind, func(t *testing.T) {
			raw, _ := json.Marshal(r)
			var bad Report
			if err := json.Unmarshal(raw, &bad); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing-initial":
				bad.InitialRules = nil
			case "unbound-version":
				bad.Frames[1].State.RulesVersion = 99
			case "missing-binding":
				bad.Frames[2].Rules = nil
			case "binding-version":
				bad.Frames[2].Rules.Lifecycle = changeLife(t, bad.Frames[2].Rules.Lifecycle, func(m map[string]any) { m["version"] = json.Number("99") })
			case "evidence":
				bad.Frames[2].Rules.Evidence = refdata.EvidenceMeasuredCTP
			case "qualification":
				bad.InitialRules.Lifecycle = changeLife(t, bad.InitialRules.Lifecycle, func(m map[string]any) { m["schedule"].(map[string]any)["Investor"] = json.Number("2") })
			case "risk-value":
				bad.Frames[4].Risk.RequiredDeposit = decimal.Zero
			case "risk-missing":
				bad.Frames[4].Risk = nil
			case "lifecycle-value":
				bad.Frames[9].Lifecycle.Contracts[0].ExitByClose = false
			case "lifecycle-missing":
				bad.Frames[9].Lifecycle = nil
			}
			raw, _ = json.Marshal(bad)
			if _, err := ReadReport(bytes.NewReader(raw)); err == nil {
				t.Fatalf("生命周期/风控报告篡改未被检出 kind=%s", kind)
			}
		})
	}
}

// TestReplayRuleEvidenceIsPerBinding 只验证证据标签传递；合成数据换标签不会因此成为真实规则。
func TestReplayRuleEvidenceIsPerBinding(t *testing.T) {
	p := lifecycleDemo(t)
	p.Events[8].Settlement.NextRules = changeLife(t, p.Events[8].Settlement.NextRules, func(m map[string]any) { m["base"].(map[string]any)["evidence"] = "documented" })
	r := execute(t, p)
	if r.Evidence != refdata.EvidencePlaceholder || r.Frames[8].Rules.Evidence != refdata.EvidenceDocumented || r.Frames[11].Rules.Evidence != refdata.EvidencePlaceholder {
		t.Fatal("后续规则证据被顶层标签覆盖或擅自升级")
	}
	raw, _ := json.Marshal(r)
	if _, err := ReadReport(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
}

// TestReplayRuleBudgetRollback 找到恰在首次切换处不足的报告预算，核对旧规则及完整前缀。
func TestReplayRuleBudgetRollback(t *testing.T) {
	p := lifecycleDemo(t)
	p.Events = p.Events[:3]
	complete := execute(t, p)
	if complete.Final.RulesVersion != 3 {
		t.Fatal("预算正例没有实际切换规则")
	}
	prefix := copyPayload(t, p)
	prefix.Events = prefix.Events[:2]
	expected := execute(t, prefix)
	found := false
	for limit := 10000; limit <= 60000; limit += 1000 {
		r, err := runBounded(p, Options{AllowPlaceholder: true}, limit)
		if err == nil || r.FailedIndex == nil || *r.FailedIndex != 2 {
			continue
		}
		if !strings.Contains(err.Error(), "报告大小") || len(r.Frames) != 2 || !same(r.Final, expected.Final) || !same(r.InitialRules, expected.InitialRules) {
			t.Fatalf("报告预算失败留下候选规则或状态 limit=%d err=%v", limit, err)
		}
		found = true
		break
	}
	if !found {
		t.Fatal("测试没有覆盖规则切换已准备但报告预算不足的边界")
	}
}

// TestReplayLifecycleMoneyBudget 不能借RawMessage绕过普通回放金额范围，产生读者无法读取的报告。
func TestReplayLifecycleMoneyBudget(t *testing.T) {
	p := lifecycleDemo(t)
	p.Events = p.Events[:2]
	p.Lifecycle = changeLife(t, p.Lifecycle, func(m map[string]any) {
		m["schedule"].(map[string]any)["Contracts"].([]any)[0].(map[string]any)["MarginSteps"].([]any)[0].(map[string]any)["Rates"].(map[string]any)["LongByMoney"] = strings.Repeat("9", 154)
	})
	r, err := Run(p, Options{AllowPlaceholder: true})
	if err == nil || !strings.Contains(err.Error(), "有界定点") || r.Initial != nil {
		t.Fatalf("生命周期原始JSON绕过回放金额边界 err=%v", err)
	}
}
