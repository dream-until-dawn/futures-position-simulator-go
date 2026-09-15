package fixture

// ⚠️ 探针（F6b 前期，2026-09-15）：核门面与 FrozenAccountOf / FrozenOf、Reconstruct 在全部语料上是否逐项相同。
// 迁移的判据（design.md §10「F6b 修订」）：冻结那一半随旧函数一起删；跨日那一半转正为 Reconstruct ≡ 门面的等价守卫。

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

type probeRules struct {
	specRules
	dates map[types.InstrumentID]refdata.PositionDateType
}

func (r probeRules) Instrument(id types.InstrumentID) (refdata.Instrument, error) {
	in, err := r.specRules.Instrument(id)
	if d, ok := r.dates[id]; ok {
		in.PositionDateType = d
	}
	return in, err
}

func TestProbeFacadeFreezeEqualsOld(t *testing.T) {
	all := loadAll(t)
	compared, diffs, closeOnUnmeasured := 0, 0, 0
	offsets := map[string]int{}
	for _, f := range all {
		if !f.HasOrders {
			continue
		}
		specs, ok := specsForOrders(t, f)
		if !ok {
			continue
		}
		old, err := FrozenAccountOf(f, specs)
		if err != nil {
			t.Logf("%s 旧：%v", f.Path, err)
			continue
		}
		rules := probeRules{specRules{version: 1, byID: map[types.InstrumentID]Spec{}}, map[types.InstrumentID]refdata.PositionDateType{}}
		syms, _ := LiveOrderSymbols(f)
		for _, sym := range syms {
			inst, _ := types.ParseSymbol(sym, f.TradingDay)
			rules.byID[inst] = specs[sym]
			if d, ok := positionDates[sym]; ok {
				rules.dates[inst] = d
			}
		}
		ch := futsim.KQChoices()
		ch.FeeRounding, ch.SideScope = fee.NoRounding, margin.ByInstrument
		sim, err := futsim.New(futsim.Config{Day: f.TradingDay, PreBalance: decimal.NewFromInt(1), Rules: rules, Choices: ch})
		if err != nil {
			t.Fatal(err)
		}
		for _, sym := range syms {
			inst, _ := types.ParseSymbol(sym, f.TradingDay)
			pre, _ := f.PreSettlement(sym)
			if err := sim.Mark(f.TradingDay, futsim.Quote{Instrument: inst, PreSettlement: pre, HasPreSettlement: true}); err != nil {
				t.Fatal(err)
			}
		}
		live, _ := liveOrders(f)
		ids := make([]string, 0, len(live))
		for id := range live {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		sumM, sumC := decimal.Zero, decimal.Zero
		vol := map[string]order.Frozen{}
		failed := false
		for _, id := range ids {
			o := live[id]
			sym, _ := textOf(o, "exchange_id", "instrument_id")
			inst, _ := types.ParseSymbol(sym, f.TradingDay)
			dir, off, _ := dirOffsetOf(o)
			left, _ := numberOf(o, "volume_left")
			px, _ := numberOf(o, "limit_price")
			offsets[fmt.Sprintf("%s %v", sym, off)]++
			if off.IsClose() {
				if _, ok := positionDates[sym]; !ok {
					closeOnUnmeasured++
				}
			}
			fr, err := sim.FreezeOf(f.TradingDay, order.Request{Instrument: inst, Direction: dir, Offset: off,
				Hedge: types.Speculation, Price: px, Volume: int(left.IntPart())})
			if err != nil {
				t.Errorf("%s 委托 %s（%s %v）新：%v", f.Path, id, sym, off, err)
				failed = true
				continue
			}
			sumM, sumC = sumM.Add(fr.Margin), sumC.Add(fr.Commission)
			side := sym + "/long"
			if dir == types.Buy {
				side = sym + "/short"
			}
			v := vol[side]
			v.VolumeToday += fr.VolumeToday
			v.VolumeHistory += fr.VolumeHistory
			vol[side] = v
		}
		if failed {
			continue
		}
		compared++
		if !sumM.Equal(old.Margin) || !sumC.Equal(old.Commission) {
			diffs++
			t.Errorf("%s 金额：旧 %s / %s，新 %s / %s", f.Path, old.Margin, old.Commission, sumM, sumC)
		}
		for _, sym := range syms {
			l, s, has, err := FrozenOf(f, sym, NakedCloseIsYesterday)
			if err != nil || !has {
				t.Errorf("%s %s FrozenOf：%v %v", f.Path, sym, has, err)
				continue
			}
			nl, ns := vol[sym+"/long"], vol[sym+"/short"]
			if l.VolumeToday != nl.VolumeToday || l.VolumeHistory != nl.VolumeHistory || s.VolumeToday != ns.VolumeToday || s.VolumeHistory != ns.VolumeHistory {
				diffs++
				t.Errorf("%s %s 手数：旧 多%+v 空%+v，新 多%+v 空%+v", f.Path, sym, l, s, nl, ns)
			}
		}
	}
	t.Logf("比了 %d 份，差异 %d，未实测 PositionDateType 上的平仓单 %d；开平分布 %v", compared, diffs, closeOnUnmeasured, offsets)
}

func facadeCarry(t *testing.T, prev, cur *Fixture, sym string, settle decimal.Decimal) (string, error) {
	t.Helper()
	specs := specsFor(t, prev)
	spec, ok := specs[sym]
	if !ok {
		return "", fmt.Errorf("没有规格")
	}
	inst, _ := types.ParseSymbol(sym, prev.TradingDay)
	rules := probeRules{specRules{version: 1, byID: map[types.InstrumentID]Spec{inst: spec}}, map[types.InstrumentID]refdata.PositionDateType{inst: positionDates[sym]}}
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
