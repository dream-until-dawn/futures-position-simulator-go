package fixture

// ⚠️ 探针（F6b 前期，2026-09-15）：核门面与 FrozenAccountOf / FrozenOf、Reconstruct 在全部语料上是否逐项相同。
// 迁移的判据（design.md §10「F6b 修订」）：冻结那一半随旧函数一起删；跨日那一半转正为 Reconstruct ≡ 门面的等价守卫。

import (
	"fmt"
	"strings"
	"testing"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func facadeCarry(t *testing.T, prev, cur *Fixture, sym string, settle decimal.Decimal) (string, error) {
	t.Helper()
	specs := specsFor(t, prev)
	spec, ok := specs[sym]
	if !ok {
		return "", fmt.Errorf("没有规格")
	}
	inst, _ := types.ParseSymbol(sym, prev.TradingDay)
	rules := specRules{version: 1, byID: map[types.InstrumentID]Spec{inst: spec}, dates: map[types.InstrumentID]refdata.PositionDateType{inst: positionDates[sym]}}
	ch := futsim.KQChoices()
	ch.FeeRounding, ch.SideScope = fee.NoRounding, margin.ByInstrument
	pb, _ := numberOf(prev.Account, "pre_balance")
	sim, err := futsim.New(futsim.Config{Day: prev.TradingDay, PreBalance: pb, Rules: rules, Choices: ch})
	if err != nil {
		return "", err
	}
	pre, _ := prev.PreSettlement(sym)
	q := futsim.Quote{Instrument: inst, PreSettlement: pre, HasPreSettlement: true}
	if last, ok := prev.Positions[sym]["last_price"]; ok && !last.Absent && !last.IsText && last.Number.IsPositive() {
		q.Last, q.HasLast = last.Number, true
	} else {
		return "", fmt.Errorf("前一日没有最新价")
	}
	if err := sim.Mark(prev.TradingDay, q); err != nil {
		return "", err
	}
	for _, tr := range prev.TradesOf(sym) {
		if err := sim.ApplyTrade(prev.TradingDay, matchTrade(tr)); err != nil {
			return "", err
		}
	}
	if err := sim.Settle(prev.TradingDay, map[types.InstrumentID]decimal.Decimal{inst: settle}, cur.TradingDay); err != nil {
		return "", err
	}
	if cur != nil {
		for _, tr := range cur.TradesOf(sym) {
			if err := sim.ApplyTrade(cur.TradingDay, matchTrade(tr)); err != nil {
				return "", err
			}
		}
	}
	p, _ := sim.Position(inst, types.Speculation)
	return signature(p), nil
}

func TestProbeFacadeCarryEqualsOld(t *testing.T) {
	all := loadAll(t)
	byPath := map[string]*Fixture{}
	for _, f := range all {
		byPath[f.Path] = f
	}
	const sym = "SHFE.rb2701"
	settle, _ := settlementOf(t, sym)
	same, diff, errs := 0, 0, 0
	byKind := map[string]int{}
	check := func(label string, prev, cur *Fixture) {
		p, err := Reconstruct(prev, cur, sym, types.Speculation, positionDateOf(t, sym), settle)
		n, nerr := facadeCarry(t, prev, cur, sym, settle)
		switch {
		case err != nil || nerr != nil:
			if (err == nil) != (nerr == nil) {
				diff++
				t.Errorf("%s：旧 %v / 新 %v", label, err, nerr)
			} else {
				errs++
				t.Logf("%s：两边都报错 旧 %v / 新 %v", label, err, nerr)
			}
		case signature(p) != n:
			diff++
			t.Errorf("%s：\n旧 %s\n新 %s", label, signature(p), n)
		default:
			same++
			byKind[strings.Fields(label)[0]]++
		}
	}
	prev8 := byPath["status-20260908-8.json"]
	for _, f := range all {
		if f.TradingDay.String() != "20260909" {
			continue
		}
		if _, ok := f.Positions[sym]; !ok {
			continue
		}
		check("reconstruct "+f.Path, prev8, f)
		if f.HasHistoryPosition(sym) {
			// carryStartFor 的挑法：上一交易日、有该合约成交、采集最晚
			var pick *Fixture
			for _, o := range all {
				if o.TradingDay.String() != "20260908" || len(o.TradesOf(sym)) == 0 {
					continue
				}
				if pick == nil || o.CapturedAt > pick.CapturedAt {
					pick = o
				}
			}
			if _, ok, why := carryStartFor(t, all, f, sym); !ok {
				t.Logf("carrywire %s 跳过：%s", f.Path, why)
				continue
			}
			check("carrywire "+f.Path+" ← "+pick.Path, pick, f)
		}
	}
	t.Logf("相同 %d（%v）/ 不同 %d / 两边都报错 %d", same, byKind, diff, errs)
}

func matchTrade(tr Trade) match.Trade {
	return match.Trade{Instrument: tr.Instrument, Direction: tr.Direction, Offset: tr.Offset,
		Hedge: types.Speculation, Price: tr.Price, Volume: tr.Volume}
}
