package replay

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/risk"
	"github.com/shopspring/decimal"
)

// TestReplayPlainRuleAdoption 旧普通Provider显式采用完整次日日规则后，后续事件和报告必须继续可用。
func TestReplayPlainRuleAdoption(t *testing.T) {
	p := lifecycleDemo(t)
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(p.Lifecycle, &wire); err != nil {
		t.Fatal(err)
	}
	p.Rules, p.Lifecycle = wire["base"], nil
	r, err := Run(p, Options{AllowPlaceholder: true})
	if err != nil {
		t.Fatalf("普通规则显式接入日规则闭环失败: %v", err)
	}
	if r.InitialRules != nil || r.Initial.RulesVersion != 100 || r.Frames[0].Lifecycle != nil || r.Frames[1].Lifecycle != nil || r.Frames[2].Rules == nil || r.Final.RulesVersion != 5 || r.Final.Account.PreBalance.String() != "1360" {
		t.Fatal("普通规则转日规则丢失配置或手算链")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ReadReport(bytes.NewReader(raw)); err != nil {
		t.Fatalf("普通规则接入后的报告不可读: %v", err)
	}
}

// plainRiskReport 只用普通规则生成只读事件，用来隔开生命周期复核的冗余兜底。
func plainRiskReport(t *testing.T) Report {
	t.Helper()
	p := demoPayload(t)
	p.Events = p.Events[:1]
	p.Events = append(p.Events, Event{ID: "plain-risk", Day: p.Settings.Day, Kind: "risk", RiskPolicy: &risk.Policy{MaxRiskRatio: decimal.RequireFromString("0.8"), MinEquity: decimal.NewFromInt(1)}})
	r := execute(t, p)
	if r.InitialRules != nil || r.Frames[1].Risk == nil || r.Frames[1].Lifecycle != nil || !same(r.Frames[0].State, r.Frames[1].State) {
		t.Fatal("普通规则只读风控正例不成立")
	}
	raw, _ := json.Marshal(r)
	if _, err := ReadReport(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	return r
}

// TestReplayRiskReadOnlyEntireState 余额不变不代表完整State不变，空簿高水位也不能被风控修改。
func TestReplayRiskReadOnlyEntireState(t *testing.T) {
	r := plainRiskReport(t)
	r.Frames[1].State.LastOrderSequence++
	r.Final = &r.Frames[1].State
	raw, _ := json.Marshal(r)
	if _, err := ReadReport(bytes.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "只读事件改动状态") {
		t.Fatalf("只读风控报告接受了被修改的存档 err=%v", err)
	}
}

// TestReplayPlainVersionInvariant 无生命周期复核时，版本守卫仍须独立拒绝无切换的变更。
func TestReplayPlainVersionInvariant(t *testing.T) {
	r := plainRiskReport(t)
	r.Frames = r.Frames[:1]
	r.Frames[0].State.RulesVersion = 99
	r.Final = &r.Frames[0].State
	raw, _ := json.Marshal(r)
	if _, err := ReadReport(bytes.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "非切换事件自行更换规则版本") {
		t.Fatalf("普通报告允许非切换事件更换规则版本 err=%v", err)
	}
}
