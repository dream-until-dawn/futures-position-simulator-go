package replay

import (
	"bytes"
	"encoding/json"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/shopspring/decimal"
	"strings"
	"testing"
)

func demoPayload(t *testing.T) Payload {
	t.Helper()
	p, err := Demo()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func execute(t *testing.T, p Payload) Report {
	t.Helper()
	r, err := Run(p, Options{AllowPlaceholder: true})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func copyPayload(t *testing.T, p Payload) Payload {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var copied Payload
	if err = json.Unmarshal(raw, &copied); err != nil {
		t.Fatal(err)
	}
	return copied
}

// TestReplayLedger 使用独立手算：首日双向往返盈亏抵消，携带一手浮盈10，费用9；次日费用11。
func TestReplayLedger(t *testing.T) {
	p := demoPayload(t)
	before, _ := json.Marshal(p)
	r := execute(t, p)
	if r.Evidence != refdata.EvidencePlaceholder {
		t.Fatal("回放擅自提升证据等级")
	}
	after, _ := json.Marshal(p)
	if !bytes.Equal(before, after) {
		t.Fatal("回放修改调用方输入")
	}
	if r.Status != "passed" || len(r.Frames) != 25 || r.Final == nil || !r.Final.Account.PreBalance.Equal(decimal.NewFromInt(1000000)) || r.Final.LastOrderSequence != 8 || len(r.Final.Orders) != 0 || len(r.Final.Quotas) != 0 || r.Final.Account.CommissionTrades != 0 {
		t.Fatalf("手算回放账本不符: %+v", r.Final)
	}
	if len(r.Final.Positions) != 1 || len(r.Final.Positions[0].Long) != 1 || r.Final.Positions[0].Long[0].Volume != 1 || !r.Final.Account.CurrMargin.Equal(decimal.NewFromInt(3001)) {
		t.Fatal("跨日持仓或占用不符")
	}
	var statements []Statement
	frozen := false
	for _, f := range r.Frames {
		if f.Account.FrozenMargin.IsPositive() {
			frozen = true
		}
		if f.Statement != nil {
			statements = append(statements, *f.Statement)
		}
	}
	if !frozen || len(statements) != 2 || !statements[0].Commission.Equal(decimal.NewFromInt(9)) || !statements[0].PositionProfit.Equal(decimal.NewFromInt(10)) || !statements[0].Balance.Equal(decimal.NewFromInt(1000001)) || !statements[1].Commission.Equal(decimal.NewFromInt(11)) || !statements[1].Balance.Equal(decimal.NewFromInt(1000000)) {
		t.Fatalf("结算账单手算不符: %+v", statements)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadReport(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	diff, err := Compare(r, decoded)
	if err != nil || !diff.Equal {
		t.Fatalf("报告往返不等价: %+v %v", diff, err)
	}
	if _, err = Run(p, Options{}); err == nil {
		t.Fatal("占位规则未显式授权仍运行")
	}
}

// TestReplayFailurePrefix 要先证明前缀真的执行，再验证失败不发布副本或后续事件。
func TestReplayFailurePrefix(t *testing.T) {
	base := demoPayload(t)
	tests := []struct {
		name   string
		change func(*Payload)
		index  int
	}{
		{"duplicate-id", func(p *Payload) { p.Events[3].ID = p.Events[0].ID }, 3},
		{"unknown-kind", func(p *Payload) { p.Events[3].Kind = "guess-fill" }, 3},
		{"mismatch-payload", func(p *Payload) { p.Events[3].Ref = "ignored-extra" }, 3},
		{"wrong-day", func(p *Payload) { p.Events[3].Day = p.Events[3].Day + 1 }, 3},
		{"invalid-trade", func(p *Payload) { p.Events[3].Order.Request.Volume = -1 }, 3},
		{"skip-calendar-day", func(p *Payload) { p.Events[11].Settlement.NextDay = 20261028 }, 11},
		{"missing-settlement", func(p *Payload) { p.Events[11].Settlement.Prices = nil }, 11},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := copyPayload(t, base)
			tc.change(&p)
			r, err := Run(p, Options{AllowPlaceholder: true})
			if err == nil || r.Status != "failed" || r.FailedIndex == nil || *r.FailedIndex != tc.index || len(r.Frames) != tc.index {
				t.Fatalf("失败前缀不符: %+v %v", r, err)
			}
			prefix := copyPayload(t, p)
			prefix.Events = prefix.Events[:tc.index]
			expected := execute(t, prefix)
			if !same(r.Final, expected.Final) || same(r.Initial, r.Final) {
				t.Fatal("失败事件提交状态或有效前缀没有实际执行")
			}
			raw, _ := json.Marshal(r)
			if _, err = ReadReport(bytes.NewReader(raw)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReplayInitialAndConfig(t *testing.T) {
	p := demoPayload(t)
	first := execute(t, p)
	resumed := copyPayload(t, p)
	resumed.Initial = first.Final
	resumed.Settings.Day = first.Final.Day
	resumed.Events = nil
	r := execute(t, resumed)
	if !same(first.Final, r.Final) {
		t.Fatal("回放初始存档恢复不等价")
	}
	for _, change := range []func(*Payload){
		func(p *Payload) { p.Settings.Day++ },
		func(p *Payload) { p.Initial.RulesVersion++ },
		func(p *Payload) { p.Settings.Choices.Algorithm = 0 },
		func(p *Payload) {
			p.Settings.TickRounding = append(p.Settings.TickRounding, p.Settings.TickRounding[0])
		},
		func(p *Payload) {
			p.Settings.PositionLimits = append(p.Settings.PositionLimits, p.Settings.PositionLimits[0])
		},
	} {
		bad := copyPayload(t, resumed)
		change(&bad)
		r, err := Run(bad, Options{AllowPlaceholder: true})
		if err == nil || r.Status != "failed" || r.Initial != nil || r.InitialAccount != nil || r.Final != nil {
			t.Fatal("无效配置洗成已知零或空簿成功")
		}
		raw, _ := json.Marshal(r)
		if _, err := ReadReport(bytes.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
	}
	p.Events = make([]Event, MaxEvents+1)
	r, err := Run(p, Options{AllowPlaceholder: true})
	if err == nil || !strings.Contains(err.Error(), "事件数量") || r.Initial != nil {
		t.Fatal("事件资源上限失效")
	}
}
