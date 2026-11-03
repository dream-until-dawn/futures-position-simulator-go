package refdata

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

func firstOpenTable() SessionTable {
	return SessionTable{Exchange: types.SHFE, Product: "rb", Day: []Session{{MustClockTime(13, 30, 0), MustClockTime(15, 0, 0)}, {MustClockTime(9, 0, 0), MustClockTime(10, 15, 0)}}, Night: []Session{{MustClockTime(21, 0, 0), MustClockTime(23, 0, 0)}}}
}

func firstOpenCalendar(t *testing.T, tab SessionTable, suspended []int32) *Calendar {
	t.Helper()
	c, err := NewCalendar([]types.TradingDay{20260907, 20260908, 20260911, 20260914}, []SessionTable{tab}, suspended)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFirstOpenKnownSessions(t *testing.T) {
	c := testCalendar(t)
	for _, row := range []struct {
		day           types.TradingDay
		ex            types.Exchange
		product, want string
	}{
		{20260908, types.SHFE, "rb", "2026-09-07 21:00:00"},
		{20260914, types.SHFE, "rb", "2026-09-11 21:00:00"},
		{20260914, types.SHFE, "cu", "2026-09-11 21:00:00"},
		{20260907, types.CZCE, "AP", "2026-09-07 09:00:00"},
	} {
		got, err := c.FirstOpen(row.day, row.ex, row.product)
		if err != nil || !got.Equal(at(t, row.want)) || got.Location() != CNZone() {
			t.Fatalf("首开时刻或北京时间错误 day=%v product=%s got=%v err=%v", row.day, row.product, got, err)
		}
	}
}

func TestFirstOpenExplicitSuspensionAndOrder(t *testing.T) {
	tab := firstOpenTable()
	c := firstOpenCalendar(t, tab, []int32{20260911})
	got, err := c.FirstOpen(20260914, types.SHFE, "rb")
	if err != nil || !got.Equal(at(t, "2026-09-14 09:00:00")) {
		t.Fatalf("停夜盘未切到最早日盘 got=%v err=%v", got, err)
	}
	tab.Night = []Session{{MustClockTime(22, 0, 0), MustClockTime(23, 0, 0)}, {MustClockTime(20, 0, 0), MustClockTime(21, 0, 0)}}
	c = firstOpenCalendar(t, tab, []int32{})
	before := append([]Session(nil), c.tables[productKey(types.SHFE, "rb")].Night...)
	got, err = c.FirstOpen(20260908, types.SHFE, "rb")
	if err != nil || !got.Equal(at(t, "2026-09-07 20:00:00")) {
		t.Fatalf("夜盘最早起点依赖数组顺序 got=%v err=%v", got, err)
	}
	if !reflect.DeepEqual(before, c.tables[productKey(types.SHFE, "rb")].Night) {
		t.Fatal("只读首开查询修改时段表")
	}
	tab.Day = []Session{{MustClockTime(13, 20, 0), MustClockTime(14, 0, 0)}}
	tab.Night = nil
	c = firstOpenCalendar(t, tab, nil)
	got, err = c.FirstOpen(20260907, types.SHFE, "rb")
	if err != nil || !got.Equal(at(t, "2026-09-07 13:20:00")) {
		t.Fatalf("无夜盘品种被补造九点 got=%v err=%v", got, err)
	}
}

func TestFirstOpenUnknownNeverBecomesDayOpen(t *testing.T) {
	tab := firstOpenTable()
	c := firstOpenCalendar(t, tab, nil)
	got, err := c.FirstOpen(20260908, types.SHFE, "rb")
	if err == nil || !got.IsZero() || errors.Is(err, ErrOutsideSession) {
		t.Fatalf("未知夜盘被默认日盘或已知休市 got=%v err=%v", got, err)
	}
	if _, err = c.SessionAt(at(t, "2026-09-08 09:00:00"), types.SHFE, "rb"); err != nil {
		t.Fatal("首开查询改变已知日盘行为", err)
	}
	for _, coverage := range [][]NightCoverage{
		{{Exchange: types.SHFE, First: 20260908, Last: 20260908, Source: "synthetic-gap"}},
		{{Exchange: types.SHFE, First: 20260907, Last: 20260907, Source: "synthetic-other-exchange"}},
	} {
		dce := tab
		dce.Exchange = types.DCE
		dce.Product = "m"
		cc, e := NewCalendarWithNightCoverage([]types.TradingDay{20260907, 20260908}, []SessionTable{tab, dce}, coverage)
		if e != nil {
			t.Fatal(e)
		}
		ex, p := types.SHFE, "rb"
		if coverage[0].First == 20260907 {
			ex, p = types.DCE, "m"
		}
		got, e = cc.FirstOpen(20260908, ex, p)
		if e == nil || !got.IsZero() {
			t.Fatalf("夜盘覆盖跨锚日或跨所外推 got=%v err=%v", got, e)
		}
	}
}

func TestFirstOpenMissingAndContradictoryInputs(t *testing.T) {
	c := firstOpenCalendar(t, firstOpenTable(), []int32{})
	for _, row := range []struct {
		c       *Calendar
		day     types.TradingDay
		product string
	}{
		{nil, 20260908, "rb"}, {c, 20260230, "rb"}, {c, 20260906, "rb"}, {c, 20260912, "rb"}, {c, 20260915, "rb"}, {c, 20260907, "rb"}, {c, 20260908, "missing"},
	} {
		got, e := row.c.FirstOpen(row.day, types.SHFE, row.product)
		if e == nil || !got.IsZero() {
			t.Fatalf("缺锚/缺表/无效日期仍给首开 day=%v got=%v err=%v", row.day, got, e)
		}
	}
	tab := firstOpenTable()
	tab.Day = nil
	c = firstOpenCalendar(t, tab, []int32{20260907})
	if got, e := c.FirstOpen(20260908, types.SHFE, "rb"); e == nil || !got.IsZero() {
		t.Fatalf("停夜盘且缺日盘仍补造首开 got=%v err=%v", got, e)
	}
	tab = firstOpenTable()
	tab.Day = []Session{{MustClockTime(23, 0, 0), MustClockTime(1, 0, 0)}}
	c = firstOpenCalendar(t, tab, []int32{})
	if got, e := c.FirstOpen(20260908, types.SHFE, "rb"); e == nil || !got.IsZero() {
		t.Fatalf("跨午夜日盘仍给首开 got=%v err=%v", got, e)
	}
	tab = firstOpenTable()
	tab.Day = []Session{{MustClockTime(20, 0, 0), MustClockTime(22, 0, 0)}}
	c = firstOpenCalendar(t, tab, []int32{})
	if got, e := c.FirstOpen(20260908, types.SHFE, "rb"); e == nil || !got.IsZero() {
		t.Fatalf("候选日夜盘归属矛盾仍给首开 got=%v err=%v", got, e)
	}
}

func TestFirstOpenConcurrentReadOnly(t *testing.T) {
	c := testCalendar(t)
	want := at(t, "2026-09-11 21:00:00")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				got, e := c.FirstOpen(20260914, types.SHFE, "rb")
				if e != nil || !got.Equal(want) {
					t.Errorf("并发首开结果不一致 got=%v err=%v", got, e)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestFirstOpenNilReturnsError(t *testing.T) {
	defer func() {
		if recover() != nil {
			t.Fatal("nil日历未正常返回错误")
		}
	}()
	var c *Calendar
	got, err := c.FirstOpen(20260908, types.SHFE, "rb")
	if err == nil || !got.IsZero() {
		t.Fatal("nil日历未正常返回错误")
	}
}
