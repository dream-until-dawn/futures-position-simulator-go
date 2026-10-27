package refdata

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestNightUnknownIsNotOpen 直接复现风险104；未知不得变成节后开盘，也不得假装已知休市。
func TestNightUnknownIsNotOpen(t *testing.T) {
	tab := SessionTable{Exchange: types.SHFE, Product: "rb", Day: []Session{{MustClockTime(9, 0, 0), MustClockTime(15, 0, 0)}}, Night: []Session{{MustClockTime(21, 0, 0), MustClockTime(23, 0, 0)}}}
	c, err := NewCalendar([]types.TradingDay{20260930, 20261009}, []SessionTable{tab}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := c.TradingDayAt(at(t, "2026-09-30 21:30:00"), types.SHFE, "rb"); err == nil || !strings.Contains(err.Error(), "夜盘覆盖未知") || errors.Is(err, ErrOutsideSession) || d != 0 {
		t.Fatalf("未知夜盘被当成确定归属或休市 day=%v err=%v", d, err)
	}
	if d, err := c.TradingDayAt(at(t, "2026-09-30 10:00:00"), types.SHFE, "rb"); err != nil || d != 20260930 {
		t.Fatalf("未知夜盘错误阻断已知日盘 day=%v err=%v", d, err)
	}
}

// nightCoverageInputs 是合成配置，只验证未知/已知与锚日算法，不宣称真实停盘安排。
func nightCoverageInputs() ([]types.TradingDay, []SessionTable) {
	day := []Session{{MustClockTime(9, 0, 0), MustClockTime(15, 0, 0)}}
	night := []Session{{MustClockTime(21, 0, 0), MustClockTime(1, 0, 0)}}
	return []types.TradingDay{20260907, 20260908, 20260909, 20260914}, []SessionTable{
		{Exchange: types.SHFE, Product: "cu", Day: day, Night: night},
		{Exchange: types.DCE, Product: "m", Day: day, Night: night},
	}
}

// TestNightCoverageExchangeAndAnchor 覆盖按交易所隔离，凌晨必须使用前夜锚日。
func TestNightCoverageExchangeAndAnchor(t *testing.T) {
	days, tables := nightCoverageInputs()
	coverage := []NightCoverage{
		{Exchange: types.SHFE, First: 20260907, Last: 20260908, Suspended: []int32{20260907}, Source: "synthetic-SHFE"},
		{Exchange: types.DCE, First: 20260907, Last: 20260907, Source: "synthetic-DCE"},
	}
	c, err := NewCalendarWithNightCoverage(days, tables, coverage)
	if err != nil {
		t.Fatal(err)
	}
	for _, when := range []string{"2026-09-07 21:30:00", "2026-09-08 00:30:00"} {
		if _, err := c.SessionAt(at(t, when), types.SHFE, "cu"); err == nil || !strings.Contains(err.Error(), "夜盘不开") {
			t.Fatalf("跨所/跨零点停盘隔离失败 at=%s err=%v", when, err)
		}
		span, err := c.SessionAt(at(t, when), types.DCE, "m")
		if err != nil || span.Anchor != 20260907 || span.TradingDay != 20260908 || !span.Night {
			t.Fatalf("夜盘覆盖锚日错误 at=%s span=%+v err=%v", when, span, err)
		}
	}
	if _, err := c.SessionAt(at(t, "2026-09-08 21:30:00"), types.DCE, "m"); err == nil || !strings.Contains(err.Error(), "夜盘覆盖未知") {
		t.Fatalf("夜盘覆盖外推到未声明日期 err=%v", err)
	}
	if day, err := c.TradingDayAt(at(t, "2026-09-08 21:30:00"), types.SHFE, "cu"); err != nil || day != 20260909 {
		t.Fatalf("显式未停夜盘未接受 day=%v err=%v", day, err)
	}
	// 不能用SHFE的声明替DCE回答。
	one, err := NewCalendarWithNightCoverage(days, tables, coverage[:1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := one.SessionAt(at(t, "2026-09-07 21:30:00"), types.DCE, "m"); err == nil || errors.Is(err, ErrOutsideSession) {
		t.Fatal("未声明交易所被另一家名单补齐")
	}
	before, err := NewCalendarWithNightCoverage(days, tables, []NightCoverage{{Exchange: types.SHFE, First: 20260908, Last: 20260914, Source: "synthetic"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = before.SessionAt(at(t, "2026-09-07 21:30:00"), types.SHFE, "cu"); err == nil || !strings.Contains(err.Error(), "夜盘覆盖未知") {
		t.Fatal("夜盘覆盖外推到起点之前")
	}
}

// TestNightCoverageRejectsAmbiguity 独立字面非法输入不应得到可用日历。
func TestNightCoverageRejectsAmbiguity(t *testing.T) {
	days, tables := nightCoverageInputs()
	valid := NightCoverage{Exchange: types.SHFE, First: 20260907, Last: 20260914, Source: "synthetic"}
	for _, kind := range []string{"unknown-exchange", "missing-table", "source", "first", "last", "reverse", "below", "above", "suspended-invalid", "suspended-outside", "suspended-duplicate", "overlap"} {
		t.Run(kind, func(t *testing.T) {
			x := valid
			switch kind {
			case "unknown-exchange":
				x.Exchange = "INVALID"
			case "missing-table":
				x.Exchange = types.INE
			case "source":
				x.Source = " \t"
			case "first":
				x.First = 20260230
			case "last":
				x.Last = 0
			case "reverse":
				x.First, x.Last = 20260908, 20260907
			case "below":
				x.First = 20260906
			case "above":
				x.Last = 20260915
			case "suspended-invalid":
				x.Suspended = []int32{20260230}
			case "suspended-outside":
				x.Suspended = []int32{20260915}
			case "suspended-duplicate":
				x.Suspended = []int32{20260907, 20260907}
			}
			list := []NightCoverage{x}
			if kind == "overlap" {
				list = append(list, valid)
			}
			if c, err := NewCalendarWithNightCoverage(days, tables, list); err == nil || c != nil {
				t.Fatalf("夜盘非法覆盖被接受 kind=%s err=%v", kind, err)
			}
		})
	}
	for _, stops := range [][]int32{{20260230}, {20260907, 20260907}, {20260906}} {
		if c, err := NewCalendar(days, tables, stops); err == nil || c != nil {
			t.Fatal("旧名单非法日期被接受")
		}
	}
	if c, err := NewCalendar([]types.TradingDay{20260230}, tables, nil); err == nil || c != nil {
		t.Fatal("非法日历日期被接受")
	}
}

// TestNightCoverageCopiesInputs 防止调用者构造后改时段、停盘日或覆盖区间，悄悄改变日历。
func TestNightCoverageCopiesInputs(t *testing.T) {
	days, tables := nightCoverageInputs()
	stops := []int32{20260907}
	coverage := []NightCoverage{{Exchange: types.SHFE, First: 20260907, Last: 20260914, Suspended: stops, Source: "synthetic"}}
	c, err := NewCalendarWithNightCoverage(days, tables, coverage)
	if err != nil {
		t.Fatal(err)
	}
	days[0] = 20260906
	stops[0] = 20260908
	coverage[0].First = 20260909
	tables[0].Night[0] = Session{MustClockTime(19, 0, 0), MustClockTime(20, 0, 0)}
	if _, err = c.TradingDayAt(at(t, "2026-09-07 21:30:00"), types.SHFE, "cu"); err == nil || !strings.Contains(err.Error(), "夜盘不开") {
		t.Fatalf("日历被外部切片改写 err=%v", err)
	}
	if got, err := c.TradingDayAt(at(t, "2026-09-08 21:30:00"), types.SHFE, "cu"); err != nil || got != 20260909 {
		t.Fatalf("已知空名单或时段被外部改写 day=%v err=%v", got, err)
	}
}

// TestNightCoveragePersistence 真实序列化路径必须保存未知、已知空和按交易所覆盖，不能换档。
func TestNightCoveragePersistence(t *testing.T) {
	snap, _ := trickySnapshot(t)
	days, tables := nightCoverageInputs()
	for _, kind := range []string{"unknown", "empty", "scoped"} {
		t.Run(kind, func(t *testing.T) {
			var c *Calendar
			var err error
			switch kind {
			case "unknown":
				c, err = NewCalendar(days, tables, nil)
			case "empty":
				c, err = NewCalendar(days, tables, []int32{})
			case "scoped":
				c, err = NewCalendarWithNightCoverage(days, tables, []NightCoverage{{Exchange: types.SHFE, First: 20260907, Last: 20260914, Source: "synthetic"}})
			}
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err = snap.Save(&buf, EvidenceDocumented, "synthetic", time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC), c); err != nil {
				t.Fatal(err)
			}
			_, loaded, _, err := Load(bytes.NewReader(buf.Bytes()), LoadOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, ex := range []types.Exchange{types.SHFE, types.DCE} {
				product := "cu"
				if ex == types.DCE {
					product = "m"
				}
				day, err := loaded.TradingDayAt(at(t, "2026-09-07 21:30:00"), ex, product)
				wantKnown := kind == "empty" || (kind == "scoped" && ex == types.SHFE)
				if wantKnown {
					if err != nil || day != 20260908 {
						t.Fatalf("已知夜盘往返后丢失 kind=%s ex=%s day=%v err=%v", kind, ex, day, err)
					}
				} else if err == nil || day != 0 || !strings.Contains(err.Error(), "夜盘覆盖未知") {
					t.Fatalf("未知夜盘往返后被升级 kind=%s ex=%s day=%v err=%v", kind, ex, day, err)
				}
			}
			var wire map[string]json.RawMessage
			if err = json.Unmarshal(buf.Bytes(), &wire); err != nil {
				t.Fatal(err)
			}
			var wc map[string]json.RawMessage
			if err = json.Unmarshal(wire["calendar"], &wc); err != nil {
				t.Fatal(err)
			}
			if kind == "empty" && string(wc["night_suspended"]) != "[]" {
				t.Fatal("已知空夜盘序列化为未知")
			}
			if kind == "unknown" {
				delete(wc, "night_suspended")
				wire["calendar"], _ = json.Marshal(wc)
				old, _ := json.Marshal(wire)
				_, oldCal, _, err := Load(bytes.NewReader(old), LoadOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = oldCal.TradingDayAt(at(t, "2026-09-07 21:30:00"), types.SHFE, "cu"); err == nil {
					t.Fatal("旧快照缺字段被升级成确认空名单")
				}
			}
			if kind == "scoped" {
				wc["night_suspended"] = json.RawMessage("[]")
				wire["calendar"], _ = json.Marshal(wc)
				mixed, _ := json.Marshal(wire)
				if _, _, _, err = Load(bytes.NewReader(mixed), LoadOptions{}); err == nil {
					t.Fatal("新旧夜盘名单歧义被加载")
				}
			}
		})
	}
}
