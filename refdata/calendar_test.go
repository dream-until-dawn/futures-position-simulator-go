package refdata

import (
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// 一份小日历：2026-09-07 周一 ~ 09-11 周五，跳过周末，下一个交易日是 09-14 周一。
func testCalendar(t *testing.T) *Calendar {
	t.Helper()
	days := []types.TradingDay{20260907, 20260908, 20260909, 20260910, 20260911, 20260914}
	rb := SessionTable{
		Exchange: types.SHFE, Product: "rb",
		Day: []Session{
			{MustClockTime(9, 0, 0), MustClockTime(11, 30, 0)},
			{MustClockTime(13, 30, 0), MustClockTime(15, 0, 0)},
		},
		Night: []Session{{MustClockTime(21, 0, 0), MustClockTime(23, 0, 0)}},
	}
	cu := SessionTable{ // 跨零点：21:00 → 01:00
		Exchange: types.SHFE, Product: "cu",
		Day: []Session{
			{MustClockTime(9, 0, 0), MustClockTime(11, 30, 0)},
			{MustClockTime(13, 30, 0), MustClockTime(15, 0, 0)},
		},
		Night: []Session{{MustClockTime(21, 0, 0), MustClockTime(1, 0, 0)}},
	}
	ap := SessionTable{ // 无夜盘
		Exchange: types.CZCE, Product: "AP",
		Day: []Session{{MustClockTime(9, 0, 0), MustClockTime(15, 0, 0)}},
	}
	c, err := NewCalendar(days, []SessionTable{rb, cu, ap}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.ParseInLocation("2006-01-02 15:04:05", s, CNZone())
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

// TestTradingDayAt 逐条钉住归属规则。
func TestTradingDayAt(t *testing.T) {
	c := testCalendar(t)
	cases := []struct {
		name    string
		when    string
		product string
		want    types.TradingDay
	}{
		{"日盘上午", "2026-09-08 10:00:00", "rb", 20260908},
		{"日盘下午", "2026-09-08 14:00:00", "rb", 20260908},
		{"夜盘开盘那一秒", "2026-09-08 21:00:00", "rb", 20260909},
		{"夜盘中段", "2026-09-08 22:30:00", "rb", 20260909},
		// ⚠️ 周五夜盘属于**下周一** —— 中间隔着周末，没有结算。
		{"周五夜盘", "2026-09-11 21:30:00", "rb", 20260914},
		// ⚠️ 跨零点时段的后半段，锚在**前一个自然日**上。
		// 这一条有判别力：不减那一天会得到 20260910，整整错一天。
		{"跨零点·凌晨", "2026-09-09 00:30:00", "cu", 20260909},
		{"跨零点·零点前", "2026-09-08 23:30:00", "cu", 20260909},
		{"无夜盘品种·日盘", "2026-09-08 10:00:00", "AP", 20260908},
	}
	if len(cases) != 8 {
		t.Fatalf("用例 %d 条，应为 8 —— 增删了就同步改这个数", len(cases))
	}
	for _, cs := range cases {
		ex := types.SHFE
		if cs.product == "AP" {
			ex = types.CZCE
		}
		got, err := c.TradingDayAt(at(t, cs.when), ex, cs.product)
		if err != nil {
			t.Errorf("%s：%v", cs.name, err)
			continue
		}
		if got != cs.want {
			t.Errorf("%s：%s %s 得到交易日 %d，应为 %d", cs.name, cs.when, cs.product, got, cs.want)
		}
	}
}

// TestTradingDayAtRefusesToGuess 断言**答不上来时报错，而不是给一个看起来合理的数**。
//
// ⚠️ 这条比上面那条重要：错误答案在下游没有任何动静，
// 而「收盘后属于哪个交易日」这种问题，返回「最近的那个」看起来最友好，
// 也最能让「引擎在非交易时段推进了一根 K 线」这件事悄无声息。
func TestTradingDayAtRefusesToGuess(t *testing.T) {
	c := testCalendar(t)
	cases := []struct {
		name    string
		when    string
		ex      types.Exchange
		product string
		msgHas  string
	}{
		{"收盘后、夜盘前", "2026-09-08 19:00:00", types.SHFE, "rb", "不落在"},
		{"夜盘收盘那一秒", "2026-09-08 23:00:00", types.SHFE, "rb", "不落在"},
		{"无夜盘品种的夜里", "2026-09-08 21:30:00", types.CZCE, "AP", "不落在"},
		{"周六日盘时刻", "2026-09-12 10:00:00", types.SHFE, "rb", "不在交易日列表里"},
		{"日历右端之外的夜盘", "2026-09-14 21:30:00", types.SHFE, "rb", "没有已知的交易日"},
		{"没有时段表的品种", "2026-09-08 10:00:00", types.DCE, "m", "没有"},
	}
	if len(cases) != 6 {
		t.Fatalf("用例 %d 条，应为 6", len(cases))
	}
	for _, cs := range cases {
		got, err := c.TradingDayAt(at(t, cs.when), cs.ex, cs.product)
		if err == nil {
			t.Errorf("⚠️ %s：%s 竟然给出了交易日 %d —— 这一格本该是错误，"+
				"而一个看起来合理的数在下游不会有任何动静", cs.name, cs.when, got)
			continue
		}
		if !strings.Contains(err.Error(), cs.msgHas) {
			t.Errorf("%s：错误信息里没有 %q：%v", cs.name, cs.msgHas, err)
		}
	}
}

// TestNightSuspendedBeforeHoliday 断言「长假前夜盘不开」被表达出来，而不是被算成节后。
func TestNightSuspendedBeforeHoliday(t *testing.T) {
	days := []types.TradingDay{20260930, 20261009}
	rb := SessionTable{
		Exchange: types.SHFE, Product: "rb",
		Day:   []Session{{MustClockTime(9, 0, 0), MustClockTime(15, 0, 0)}},
		Night: []Session{{MustClockTime(21, 0, 0), MustClockTime(23, 0, 0)}},
	}
	// 不声明停夜盘时：09-30 晚 21:30 会被算成节后的 20261009。
	loose, err := NewCalendar(days, []SessionTable{rb}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d, err := loose.TradingDayAt(at(t, "2026-09-30 21:30:00"), types.SHFE, "rb"); err != nil || d != 20261009 {
		t.Fatalf("前提变了：不声明停夜盘时应得到 20261009，实为 %d / %v", d, err)
	}

	// 声明之后必须报错 —— ⚠️ 那一夜根本没有盘，答任何交易日都是错的。
	strict, err := NewCalendar(days, []SessionTable{rb}, []int32{20260930})
	if err != nil {
		t.Fatal(err)
	}
	d, err := strict.TradingDayAt(at(t, "2026-09-30 21:30:00"), types.SHFE, "rb")
	if err == nil {
		t.Errorf("⚠️ 声明了 09-30 停夜盘，却仍给出交易日 %d —— "+
			"节前 21:30 被算成节后那个交易日，是一个**看起来完全合理的答案**", d)
	}
	// 白天不受影响。
	if d, err := strict.TradingDayAt(at(t, "2026-09-30 10:00:00"), types.SHFE, "rb"); err != nil || d != 20260930 {
		t.Errorf("停夜盘不该影响当天日盘：得到 %d / %v", d, err)
	}
}

// TestTimezoneIsFixedNotLocal 断言换算用固定 +08:00。
//
// ⚠️ **先说这条测试抓不到什么。** 本机时区就是 +08:00，
// 所以把 cnZone 换成 time.Local 时，下面几条断言**全都照样通过** ——
// 在这台机器上两者完全同值。它能在 +08:00 之外的宿主机上抓到那个替换，
// 但那正是「在开发机上对、在服务器上错」的反面：**这里的绿是环境给的，不是判据给的。**
//
// 所以另加了一条与宿主机无关的判别，见 TestZoneIsFixedNotIANA。
func TestTimezoneIsFixedNotLocal(t *testing.T) {
	c := testCalendar(t)
	// 同一个瞬间的三种写法，必须给出同一个交易日。
	utc := time.Date(2026, 9, 8, 13, 30, 0, 0, time.UTC)                      // = 21:30 +08:00
	cn := at(t, "2026-09-08 21:30:00")                                        // 直接写 +08:00
	other := time.Date(2026, 9, 8, 22, 30, 0, 0, time.FixedZone("X", 9*3600)) // = 21:30 +08:00
	var got []types.TradingDay
	for _, tm := range []time.Time{utc, cn, other} {
		d, err := c.TradingDayAt(tm, types.SHFE, "rb")
		if err != nil {
			t.Fatalf("%v：%v", tm, err)
		}
		got = append(got, d)
	}
	if got[0] != got[1] || got[1] != got[2] {
		t.Errorf("⚠️ 同一瞬间的三种时区写法给出了不同交易日 %v —— "+
			"换算依赖了输入的时区表示，而不是那个瞬间本身", got)
	}
	if got[0] != 20260909 {
		t.Errorf("21:30 +08:00 应属交易日 20260909，得到 %d", got[0])
	}
}

// TestClockTimeRefusesOutOfRange 断言越界报错而不取模。
func TestClockTimeRefusesOutOfRange(t *testing.T) {
	for _, c := range [][3]int{{24, 0, 0}, {25, 0, 0}, {-1, 0, 0}, {9, 60, 0}, {9, 0, 60}} {
		if _, err := NewClockTime(c[0], c[1], c[2]); err == nil {
			t.Errorf("⚠️ %02d:%02d:%02d 被接受了 —— 取模会把 25:00 悄悄变成 01:00，"+
				"于是一个笔误变成一个看起来合法的时段", c[0], c[1], c[2])
		}
	}
	if _, err := NewClockTime(23, 59, 59); err != nil {
		t.Errorf("23:59:59 应当合法：%v", err)
	}
}

// TestSessionTableRejectsOverlap 断言重叠时段被拒。
//
// ⚠️ 重叠时「一个时刻落在哪一段」有两个答案，而实现只会取第一个 —— 那是不会报错的歧义。
func TestSessionTableRejectsOverlap(t *testing.T) {
	tab := SessionTable{
		Exchange: types.SHFE, Product: "rb",
		Day: []Session{
			{MustClockTime(9, 0, 0), MustClockTime(11, 30, 0)},
			{MustClockTime(11, 0, 0), MustClockTime(15, 0, 0)}, // 与上一段重叠
		},
	}
	if err := tab.Validate(); err == nil {
		t.Error("⚠️ 重叠的日盘时段被接受了")
	}
	tab.Day[1] = Session{MustClockTime(13, 30, 0), MustClockTime(15, 0, 0)}
	if err := tab.Validate(); err != nil {
		t.Errorf("不重叠的时段被拒：%v", err)
	}
}

// TestCalendarRefusesBadInput 断言构造期的守卫。
func TestCalendarRefusesBadInput(t *testing.T) {
	rb := SessionTable{Exchange: types.SHFE, Product: "rb",
		Day: []Session{{MustClockTime(9, 0, 0), MustClockTime(15, 0, 0)}}}
	for _, cs := range []struct {
		name   string
		days   []types.TradingDay
		tabs   []SessionTable
		msgHas string
	}{
		{"空交易日列表", nil, []SessionTable{rb}, "为空"},
		{"未升序", []types.TradingDay{20260908, 20260907}, []SessionTable{rb}, "升序"},
		{"重复交易日", []types.TradingDay{20260907, 20260907}, []SessionTable{rb}, "升序"},
		{"没有时段表", []types.TradingDay{20260907}, nil, "一张时段表都没有"},
	} {
		if _, err := NewCalendar(cs.days, cs.tabs, nil); err == nil {
			t.Errorf("⚠️ %s 被接受了", cs.name)
		} else if !strings.Contains(err.Error(), cs.msgHas) {
			t.Errorf("%s：错误信息里没有 %q：%v", cs.name, cs.msgHas, err)
		}
	}
}

// TestZoneIsFixedNotIANA 把「用固定偏移而不是 IANA 时区」这个决定钉成断言。
//
// 判别点是 1988 年：中国当时实行夏令时，IANA 的 Asia/Shanghai 在那年夏天是 +09:00，
// 而固定偏移恒为 +08:00。于是这一条能把两种实现分开，**且不依赖宿主机时区**。
//
// ⚠️ 它断言的是**已知边界本身**：design.md 里写着「1991 年前的时刻会算错，
// 期货电子交易数据不覆盖那段时期，所以代价是零 —— 但它是已知边界，不是疏忽」。
// 把边界写进测试，是为了将来有人「顺手改成 LoadLocation」时，
// 这条会红并把那段说明顶出来，而不是让二进制悄悄多带 450 KB 时区库、
// 行为悄悄依赖宿主机装没装系统时区数据库。
func TestZoneIsFixedNotIANA(t *testing.T) {
	for _, when := range []time.Time{
		time.Date(1988, 7, 1, 12, 0, 0, 0, time.UTC),  // 中国当年夏令时，IANA 为 +09:00
		time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), // 现代冬季
		time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC), // 现代夏季
	} {
		_, off := when.In(CNZone()).Zone()
		if off != 8*3600 {
			t.Errorf("⚠️ %s 在本库时区下的偏移是 %+d 秒，应恒为 +28800 —— "+
				"若这里出现 +32400，说明换成了 IANA 时区（1988 年中国有夏令时），"+
				"那会引入 time/tzdata 或依赖宿主机时区库，两者都是 design.md 明确排除的",
				when.Format("2006-01-02"), off)
		}
	}
}
