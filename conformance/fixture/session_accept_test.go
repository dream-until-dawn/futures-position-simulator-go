package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// loadSessionsForTest 读实测的品种时段表。
func loadSessionsForTest(t *testing.T) map[string]refdata.SessionTable {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "refdata", "sessions-20260908.json"))
	if err != nil {
		t.Skipf("没有时段表，跳过：%v", err)
	}
	var raw struct {
		Tables []struct {
			Exchange string                        `json:"exchange"`
			Product  string                        `json:"product"`
			Day      []struct{ Start, End string } `json:"day"`
			Night    []struct{ Start, End string } `json:"night"`
		} `json:"session_tables"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	parse := func(s string) refdata.ClockTime {
		var h, m, sec int
		if _, err := fmt.Sscanf(s, "%d:%d:%d", &h, &m, &sec); err != nil {
			t.Fatalf("时刻 %q 解析失败：%v", s, err)
		}
		c, err := refdata.NewClockTime(h, m, sec)
		if err != nil {
			t.Fatalf("时刻 %q：%v", s, err)
		}
		return c
	}
	out := map[string]refdata.SessionTable{}
	for _, tb := range raw.Tables {
		st := refdata.SessionTable{Exchange: types.Exchange(tb.Exchange), Product: tb.Product}
		for _, x := range tb.Day {
			st.Day = append(st.Day, refdata.Session{Start: parse(x.Start), End: parse(x.End)})
		}
		for _, x := range tb.Night {
			st.Night = append(st.Night, refdata.Session{Start: parse(x.Start), End: parse(x.End)})
		}
		out[tb.Exchange+"."+tb.Product] = st
	}
	return out
}

// TestOrdersAcceptedOutsideSession 钉住一条实测：**柜台不拦非交易时段的报单**。
//
// # 判据
//
// 一笔委托的 last_msg 为空 ⇒ 它从来没被拒过 ⇒ 柜台**收下**了它。
// 再把它的下单时刻换算成东八区墙钟，看落不落在该品种的任何一个时段里。
//
//	DCE.i2701  02:56:44 被收下   而铁矿夜盘 21:00–23:00，日盘 09:00 起
//
// 收盘之后**将近四小时**，柜台照收不误。
//
// # 为什么要钉
//
// order 包的 CheckSession 目前落在 Unchecked 里（本库拿不到「现在是不是
// 交易时段」）。⚠️ 而这条实测说的是另一件事：**就算查了，柜台也不查** ——
// 于是在这个口子上，一个查时段的实现与一个不查的实现**对拍结果相同**。
// 那是一条盲区，不是一处待办。两者在清单上长得一样，处理方式相反。
//
// ⚠️ 假设写出来：insert_date_time 是柜台给的纳秒时间戳，这里按**东八区**
// 解释。中国期货的柜台不给别的时区，但这仍然是一个假设 ——
// 它错了的话，下面每一笔的「时段内/外」都会跟着错。
func TestOrdersAcceptedOutsideSession(t *testing.T) {
	tables := loadSessionsForTest(t)
	cn := refdata.CNZone()

	type hit struct {
		where, sym, clock string
	}
	var outside []hit
	accepted, known := 0, 0
	for _, f := range loadAll(t) {
		for id, o := range f.Orders {
			if v, ok := o["last_msg"]; ok && v.IsText && v.Text != "" {
				continue // 被拒过 —— 与「收不收」这个问题无关
			}
			ns, ok := numberOf(o, "insert_date_time")
			if !ok || !ns.IsPositive() {
				continue
			}
			sym, ok := textOf(o, "exchange_id", "instrument_id")
			if !ok {
				continue
			}
			inst, err := types.ParseSymbol(sym, f.TradingDay)
			if err != nil {
				continue
			}
			product, _ := splitProduct(inst.Product)
			st, ok := tables[string(inst.Exchange)+"."+product]
			if !ok {
				continue // 没有这个品种的时段表，判不了
			}
			accepted++
			known++
			at := time.Unix(0, ns.IntPart()).In(cn)
			clock, err := refdata.NewClockTime(at.Hour(), at.Minute(), at.Second())
			if err != nil {
				t.Fatal(err)
			}
			in := false
			for _, ss := range append(append([]refdata.Session{}, st.Day...), st.Night...) {
				if ss.Contains(clock) {
					in = true
					break
				}
			}
			if !in {
				outside = append(outside, hit{f.Path + " " + id, sym, at.Format("15:04:05")})
			}
		}
	}
	t.Logf("被收下且能判时段的委托 %d 笔，其中落在**任何时段之外**的 %d 笔", known, len(outside))
	for i, h := range outside {
		if i >= 5 {
			t.Logf("  …… 另有 %d 笔", len(outside)-5)
			break
		}
		t.Logf("  %s %s @%s", h.where, h.sym, h.clock)
	}
	if known == 0 {
		t.Skip("⚠️ 没有能判时段的「被收下」委托 —— 这条实测暂时没有语料")
	}
	if len(outside) == 0 {
		t.Errorf("⚠️ %d 笔被收下的委托**全都落在交易时段内** —— "+
			"「柜台不校验时段」这条实测（kq_facts）没有语料支撑了。"+
			"要么语料变了，要么柜台改了行为：两种情形处理方式相反，先分清", known)
	}
}
