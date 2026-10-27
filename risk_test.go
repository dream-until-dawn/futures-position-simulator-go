package futsim

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/risk"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestFacadeRiskIsReadOnly 核对门面使用同一聚合账户且成功、失败均不改变完整存档。
func TestFacadeRiskIsReadOnly(t *testing.T) {
	s := newSim(t)
	mark(t, s, "SHFE.rb2701", "3000", "3000")
	if _, err := s.PlaceAccepted(simDay, "pending", order.Request{Instrument: simInst(t, "SHFE.rb2701"), Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: decimal.NewFromInt(3000), Volume: 1}); err != nil {
		t.Fatal(err)
	}
	before, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(before)
	p := risk.Policy{MaxRiskRatio: decimal.RequireFromString("0.8"), MinEquity: decimal.NewFromInt(1), MinAvailable: s.Account().Available.Add(decimal.NewFromInt(10))}
	r, err := s.AssessRisk(p)
	if err != nil || !r.NeedsMarginCall || !r.RequiredDeposit.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("门面风控未使用当前冻结后的可用 report=%+v err=%v", r, err)
	}
	direct, err := risk.Assess(s.acc.State(), p)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(r)
	b, _ := json.Marshal(direct)
	if string(a) != string(b) {
		t.Fatal("门面风控与纯包不同")
	}
	bad := p
	bad.MaxRiskRatio = decimal.Zero
	if _, err = s.AssessRisk(bad); err == nil {
		t.Fatal("门面接受未知策略")
	}
	s.broken = fmt.Errorf("synthetic incomplete commit")
	if _, err = s.AssessRisk(p); err == nil {
		t.Fatal("失效模拟器仍给出风控结论")
	}
	s.broken = nil
	after, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	now, _ := json.Marshal(after)
	if string(raw) != string(now) {
		t.Fatal("风控查询改变完整状态")
	}
	var zero Simulator
	if _, err = zero.AssessRisk(p); err == nil {
		t.Fatal("未初始化模拟器给出风控结论")
	}
	var missing *Simulator
	if _, err = missing.AssessRisk(p); err == nil {
		t.Fatal("nil模拟器给出风控结论")
	}
}
