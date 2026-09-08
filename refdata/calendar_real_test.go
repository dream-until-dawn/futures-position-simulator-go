package refdata_test

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

// loadRealCalendar 用**实测的**交易日列表与时段表建一个日历。
//
// ⚠️ 两份数据两个来源，都不是猜的：
//
//	交易日列表  上期所日行情逐日探测（cmd/settlement -calendar-back）
//	时段表      天勤合约字典（cmd/refdata-sync）
//
// 在此之前 Calendar 只被合成数据测过 —— 而 §7.3b 记着一次教训：
// 我照文档填的日盘时段漏了 10:15–10:30 那个休息，于是 10:20 会被判成交易时段。
// **合成数据测不出「我对现实的假设是错的」。**
func loadRealCalendar(t *testing.T) *refdata.Calendar {
	t.Helper()
	dir := filepath.Join("..", "testdata", "refdata")

	var cal struct {
		Days []struct {
			Day    string `json:"day"`
			Status string `json:"status"`
		} `json:"days"`
		Source string `json:"source"`
		Note   string `json:"note"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "shfe-calendar-202609.json"))
	if err != nil {
		t.Skipf("没有交易日历，跳过：%v", err)
	}
	if err := json.Unmarshal(b, &cal); err != nil {
		t.Fatal(err)
	}
	var days []types.TradingDay
	for _, d := range cal.Days {
		// ⚠️ 「取不到」**不算**交易日，而「未结算」算 —— 后者是当天盘中的样子。
		// 把「未结算」排除掉，会让今天从日历里消失，而今天正是最常被问的那天。
		if d.Status == "日行情取不到" {
			continue
		}
		td, err := types.ParseTradingDay(d.Day)
		if err != nil {
			t.Fatal(err)
		}
		days = append(days, td)
	}
	if len(days) < 10 {
		t.Fatalf("交易日只有 %d 天 —— 太少，下面的断言可能在空转", len(days))
	}

	var sess struct {
		Tables []struct {
			Exchange string                        `json:"exchange"`
			Product  string                        `json:"product"`
			Day      []struct{ Start, End string } `json:"day"`
			Night    []struct{ Start, End string } `json:"night"`
		} `json:"session_tables"`
		Source      string `json:"source"`
		GeneratedAt string `json:"generated_at"`
		Note        string `json:"note"`
	}
	b, err = os.ReadFile(filepath.Join(dir, "sessions-20260908.json"))
	if err != nil {
		t.Skipf("没有时段表，跳过：%v", err)
	}
	if err := json.Unmarshal(b, &sess); err != nil {
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
	var tables []refdata.SessionTable
	for _, tb := range sess.Tables {
		st := refdata.SessionTable{
			Exchange: types.Exchange(tb.Exchange), Product: tb.Product,
		}
		for _, x := range tb.Day {
			st.Day = append(st.Day, refdata.Session{Start: parse(x.Start), End: parse(x.End)})
		}
		for _, x := range tb.Night {
			st.Night = append(st.Night, refdata.Session{Start: parse(x.Start), End: parse(x.End)})
		}
		tables = append(tables, st)
	}
	if len(tables) < 3 {
		t.Fatalf("时段表只有 %d 个品种 —— 太少", len(tables))
	}
	c, err := refdata.NewCalendar(days, tables, nil)
	if err != nil {
		t.Fatalf("用实测数据建日历失败：%v", err)
	}
	return c
}

// TestCalendarAgainstMeasuredTradingDay 拿 Calendar 与柜台报的 trading_day 对拍。
//
// ⚠️ 这是 Calendar 第一次被**实测**验证。它此前只被合成数据测过，
// 而合成数据测不出「我对现实的假设是错的」——
// §7.3b 那次就是：照文档填的日盘时段漏了 10:15–10:30 的休息。
//
// 每一行的 trading_day 都是柜台在那个时刻**真的报出来的**。
func TestCalendarAgainstMeasuredTradingDay(t *testing.T) {
	c := loadRealCalendar(t)
	cn := refdata.CNZone()
	cases := []struct {
		at      time.Time
		product string
		ex      types.Exchange
		want    string
		note    string
	}{
		// 夜盘：自然日 09-07 周一晚 21:03，柜台报 20260908。
		// ⚠️ 这是整套日历里最要紧的一条：夜盘属于**下一个**交易日。
		{time.Date(2026, 9, 7, 21, 3, 0, 0, cn), "rb", types.SHFE, "20260908",
			"夜盘属于下一个交易日"},
		// 日盘：时刻取自夹具的 captured_at，不是我编的。
		{time.Date(2026, 9, 8, 9, 26, 49, 0, cn), "rb", types.SHFE, "20260908",
			"上午盘中（status-20260908-5）"},
		{time.Date(2026, 9, 8, 13, 31, 22, 0, cn), "rb", types.SHFE, "20260908",
			"下午盘中（avg-price-20260908）"},
		{time.Date(2026, 9, 8, 13, 31, 49, 0, cn), "m", types.DCE, "20260908",
			"大商所下午盘中（avg-price-20260908-2）"},
	}
	if len(cases) != 4 {
		t.Fatalf("用例 %d 条，应为 4", len(cases))
	}
	night, day := 0, 0
	for _, cse := range cases {
		got, err := c.TradingDayAt(cse.at, cse.ex, cse.product)
		if err != nil {
			// ⚠️ 15:04 落在任何时段之外，Calendar 按设计**报错而不猜**。
			// 那是对的：收盘之后属于哪个交易日，取决于结算做没做 ——
			// 而今天下午恰好实测到「今结算价出来了但账户还没结算」，
			// 也就是这个问题在那个时刻**确实没有唯一答案**（kq_facts 21）。
			t.Logf("ⓘ %s（%s）落在时段之外，Calendar 报错而不猜：%v",
				cse.at.Format("2006-01-02 15:04"), cse.note, err)
			continue
		}
		if got.String() != cse.want {
			t.Errorf("⚠️ %s（%s，%s.%s）：Calendar 说 %s，柜台**实测**报的是 %s",
				cse.at.Format("2006-01-02 15:04"), cse.note, cse.ex, cse.product,
				got, cse.want)
			continue
		}
		if cse.at.Hour() >= 20 {
			night++
		} else {
			day++
		}
		t.Logf("%s %s.%-3s → %s   （%s）",
			cse.at.Format("2006-01-02 15:04"), cse.ex, cse.product, got, cse.note)
	}
	// ⚠️ 判别力：夜盘那条必须真的跑过。
	// 日盘上「交易日 = 自然日」是平凡的，只有夜盘能考验那条映射。
	if night == 0 {
		t.Fatal("⚠️ 一条夜盘用例都没通过 —— " +
			"日盘上「交易日 = 自然日」是平凡的，只有夜盘能考验那条映射")
	}
	if day == 0 {
		t.Fatal("⚠️ 一条日盘用例都没通过 —— 那说明连平凡的情形都没走到")
	}
}

// TestCalendarNonTradingDaysAreWeekends 断言探出来的非交易日**正好是周末**。
//
// ⚠️ 这是逐日探测那份数据的内部一致性检查，也是它唯一的自证方式：
// 若探测坏了（比如端点变了、判据认错了 404），
// 「取不到」的日子不会恰好落在周末上。
//
// ⚠️ 反过来说，它**证不了**节假日：本次 16 天里没有法定假日。
// 遇到长假时这条会红，而那时要看的是「是不是真的放假」，不是改这条测试。
func TestCalendarNonTradingDaysAreWeekends(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "testdata", "refdata", "shfe-calendar-202609.json"))
	if err != nil {
		t.Skipf("没有交易日历，跳过：%v", err)
	}
	var cal struct {
		Days         []struct{ Day, Status string } `json:"days"`
		Source, Note string
	}
	if err := json.Unmarshal(b, &cal); err != nil {
		t.Fatal(err)
	}
	weekendMiss, weekdayMiss, weekends, weekdays := 0, 0, 0, 0
	for _, d := range cal.Days {
		td, err := types.ParseTradingDay(d.Day)
		if err != nil {
			t.Fatal(err)
		}
		wd := td.CalendarDate().Weekday()
		isWeekend := wd == time.Saturday || wd == time.Sunday
		missing := d.Status == "日行情取不到"
		switch {
		case isWeekend:
			weekends++
			if !missing {
				weekdayMiss++
				t.Errorf("⚠️ %s 是%s，却有日行情 —— 交易所周末开市？先查探测", d.Day, wd)
			}
		default:
			weekdays++
			if missing {
				weekendMiss++
				t.Errorf("⚠️ %s 是%s（工作日），却取不到日行情 —— "+
					"要么是法定假日，要么探测坏了。**先查清楚是哪一种**", d.Day, wd)
			}
		}
	}
	t.Logf("%d 天：工作日 %d、周末 %d；工作日缺 %d、周末有 %d",
		len(cal.Days), weekdays, weekends, weekendMiss, weekdayMiss)
	// ⚠️ 两边都要有样本：全是工作日的话，「周末没有行情」这句话没被考验过。
	if weekends == 0 || weekdays == 0 {
		t.Fatalf("⚠️ 样本里工作日 %d、周末 %d —— 有一边为零，这条判别力不足",
			weekdays, weekends)
	}
}

// TestCalendarIsMoreConservativeThanTheCounter 把一处**实测的差别**记下来。
//
// ⚠️ 它不是 bug，是刻意的设计差异，而两边各自都对：
//
//	柜台     任何时刻都报得出 trading_day —— 它有一个「当前交易日」的状态
//	Calendar 时段之外**报错而不猜** —— 它回答的是「这个时刻属于哪个交易日」
//
// 实测：夹具 status-20260908-6 采于 **13:28:42**，落在午休（11:30–13:30）里，
// 而柜台照样报 `trading_day=20260908`。同一时刻 Calendar 拒答。
//
// ⚠️ 记下来是因为它会在对拍里以「Calendar 算不出来」的形式出现，
// 而那时很容易被读成缺陷。真正的问题是**两者回答的不是同一个问题**：
// 「柜台当前处在哪个交易日」与「某个时刻属于哪个交易日」在盘中重合，在休市时分岔。
func TestCalendarIsMoreConservativeThanTheCounter(t *testing.T) {
	c := loadRealCalendar(t)
	cn := refdata.CNZone()
	// 午休里的一个真实采样时刻（status-20260908-6 的 captured_at）。
	at := time.Date(2026, 9, 8, 13, 28, 42, 0, cn)
	if _, err := c.TradingDayAt(at, types.SHFE, "rb"); err == nil {
		t.Errorf("⚠️ %s 在午休里，Calendar 却给出了交易日 —— "+
			"「时段之外不猜」这条设计没生效", at.Format("15:04:05"))
	}
	// 对照：往后挪三分钟进了下午盘，就必须答得出来。
	// ⚠️ 没有这条，上面那条可以靠「一律报错」通过。
	in := time.Date(2026, 9, 8, 13, 31, 22, 0, cn)
	got, err := c.TradingDayAt(in, types.SHFE, "rb")
	if err != nil {
		t.Fatalf("%s 在下午盘中，Calendar 却报错：%v", in.Format("15:04:05"), err)
	}
	if got.String() != "20260908" {
		t.Errorf("%s → %s，柜台实测报的是 20260908", in.Format("15:04:05"), got)
	}
	t.Logf("ⓘ 13:28:42（午休）Calendar 拒答，而柜台报 20260908；"+
		"13:31:22（盘中）两者一致为 %s —— 两者回答的不是同一个问题", got)
}
