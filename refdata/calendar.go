package refdata

import (
	"fmt"
	"sort"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// cnZone 是中国期货市场的固定时区偏移。
//
// ⚠️ 刻意用固定 +08:00，不用 time.LoadLocation("Asia/Shanghai")：
//
//	不引入 time/tzdata（stdlib，但给二进制多带约 450 KB），
//	也不依赖宿主机装了系统时区库 —— Windows 上默认没有
//	行为不随宿主机环境变化 —— time.Local 在不同机器上是不同的东西，
//	而「在开发机上对、在服务器上错」的日期换算正是本项目在防的那类静默失败
//
// ⚠️ 已知边界写在这里：中国 1991 年前实行过夏令时，那段时期的时刻会算错。
// 期货电子交易数据不覆盖那段时期，所以代价是零 —— 但它是**已知边界，不是疏忽**。
var cnZone = time.FixedZone("CST", 8*3600)

// CNZone 返回本库统一使用的时区。调用方要把外部时间换算到这里再传进来。
func CNZone() *time.Location { return cnZone }

// ClockTime 是一天内的墙钟秒数，0 ≤ t < 86400。
//
// ⚠️ 它是**一天内的偏移**，不带日期 —— 时段表描述的是「每天的几点到几点」，
// 把日期编进去会让同一张表在每个交易日都需要一份副本。
type ClockTime int32

// NewClockTime 由时分秒构造，越界报错而不是取模。
//
// ⚠️ 取模会把 25:00 悄悄变成 01:00 —— 一个笔误因此变成一个看起来合法的时段。
func NewClockTime(h, m, s int) (ClockTime, error) {
	if h < 0 || h > 23 || m < 0 || m > 59 || s < 0 || s > 59 {
		return 0, fmt.Errorf("时刻 %02d:%02d:%02d 越界（时 0-23，分秒 0-59）", h, m, s)
	}
	return ClockTime(h*3600 + m*60 + s), nil
}

// MustClockTime 供表驱动的静态数据用；越界直接 panic。
func MustClockTime(h, m, s int) ClockTime {
	c, err := NewClockTime(h, m, s)
	if err != nil {
		panic(err)
	}
	return c
}

func (c ClockTime) String() string {
	return fmt.Sprintf("%02d:%02d:%02d", int(c)/3600, int(c)/60%60, int(c)%60)
}

// clockOf 取一个时刻在中国时区下的一天内秒数。
func clockOf(t time.Time) ClockTime {
	t = t.In(cnZone)
	return ClockTime(t.Hour()*3600 + t.Minute()*60 + t.Second())
}

// naturalDayOf 取一个时刻在中国时区下的自然日，编码同 TradingDay（yyyymmdd）。
//
// ⚠️ 返回类型刻意**不是** types.TradingDay：自然日不是交易日，
// 而这两者在本项目里已经制造过两次真实错误。用 int32 逼调用方显式转换。
func naturalDayOf(t time.Time) int32 {
	t = t.In(cnZone)
	return int32(t.Year()*10000 + int(t.Month())*100 + t.Day())
}

// Session 是一个连续的交易时段，用一天内的墙钟表示。
//
// ⚠️ 跨零点的时段（如 21:00 → 01:00）用 End < Start 表示，
// 判定时必须分两段。写成「End 加 86400」看起来更简单，
// 但那样 End 就不再是一个合法的 ClockTime，越界检查随之失效。
type Session struct {
	Start ClockTime
	End   ClockTime
}

// CrossesMidnight 报告该时段是否跨零点。
func (s Session) CrossesMidnight() bool { return s.End < s.Start }

// Contains 报告一个一天内的时刻是否落在本时段内（左闭右开）。
//
// ⚠️ 右开是刻意的：23:00:00 属于**收盘之后**，不属于 21:00–23:00 这一段。
// 左闭右开让相邻时段不重叠，于是「同一时刻落在两个时段里」这种状态不可能出现。
func (s Session) Contains(c ClockTime) bool {
	if s.Start == s.End {
		return false // 零长时段不含任何时刻
	}
	if s.CrossesMidnight() {
		return c >= s.Start || c < s.End
	}
	return c >= s.Start && c < s.End
}

// Validate 校验时段本身合法。
func (s Session) Validate() error {
	if s.Start < 0 || s.Start >= 86400 || s.End < 0 || s.End >= 86400 {
		return fmt.Errorf("时段 %s→%s 的端点越界", s.Start, s.End)
	}
	if s.Start == s.End {
		return fmt.Errorf("时段 %s→%s 长度为零", s.Start, s.End)
	}
	return nil
}

// SessionTable 是一个品种的时段表。
//
// ⚠️ Day 与 Night 分开存，不是为了好看：**归属规则不同** ——
// 日盘时刻属于当天，夜盘时刻属于**下一个交易日**。
// 合并成一张表之后，这条区别就只能靠「时间早晚」去猜，而那正是要避免的推算。
type SessionTable struct {
	Exchange types.Exchange
	Product  string
	Day      []Session
	Night    []Session // 可为空：该品种无夜盘
}

// Validate 校验时段表：端点合法、同类时段之间不重叠。
func (t SessionTable) Validate() error {
	if t.Exchange == "" || t.Product == "" {
		return fmt.Errorf("时段表缺交易所或品种（%q / %q）", t.Exchange, t.Product)
	}
	for _, group := range []struct {
		name string
		ss   []Session
	}{{"日盘", t.Day}, {"夜盘", t.Night}} {
		for i, s := range group.ss {
			if err := s.Validate(); err != nil {
				return fmt.Errorf("%s.%s %s第 %d 段：%w", t.Exchange, t.Product, group.name, i+1, err)
			}
			// ⚠️ 重叠检查：两段重叠时「一个时刻落在哪一段」有两个答案，
			// 而后续逻辑只会取第一个 —— 那是一个不会报错的歧义。
			for j := 0; j < i; j++ {
				if sessionsOverlap(group.ss[j], s) {
					return fmt.Errorf("%s.%s %s第 %d 段(%s→%s)与第 %d 段(%s→%s)重叠",
						t.Exchange, t.Product, group.name, j+1, group.ss[j].Start, group.ss[j].End,
						i+1, s.Start, s.End)
				}
			}
		}
	}
	if len(t.Day) == 0 && len(t.Night) == 0 {
		return fmt.Errorf("%s.%s 一个时段都没有", t.Exchange, t.Product)
	}
	return nil
}

func sessionsOverlap(a, b Session) bool {
	// 逐秒判太慢，按端点判：只要一方的起点落在另一方内，就重叠。
	return a.Contains(b.Start) || b.Contains(a.Start)
}

// Calendar 把墙钟时刻映射到交易日。
//
// ⚠️ 它是**查表**，不是推算。交易日列表来自交易所公告 / 上游数据层；
// 本库不内置「工作日近似」，因为那会在每个长假前后错一次，
// 而错的那几天恰好是保证金上调、风险最高的时候。
type Calendar struct {
	days   []types.TradingDay // 升序
	dayset map[types.TradingDay]bool
	tables map[string]SessionTable

	// noNight 是**当晚夜盘不开**的自然日集合（yyyymmdd）。
	//
	// ⚠️ 这个字段的存在是因为一条时段级的例外：**长假前的夜盘不开**。
	// 只靠交易日列表表达不了它 —— 列表只说哪天是交易日，
	// 不说那天晚上有没有夜盘。缺了它，节前 21:30 会被算成节后那个交易日，
	// 而那是一个**看起来完全合理的答案**。
	noNight map[int32]bool
}

// NewCalendar 由数据构造日历。days 必须非空且严格升序。
func NewCalendar(days []types.TradingDay, tables []SessionTable, nightSuspended []int32) (*Calendar, error) {
	if len(days) == 0 {
		return nil, fmt.Errorf("交易日列表为空 —— 本库不推算交易日，没有数据就答不了")
	}
	c := &Calendar{
		days:    make([]types.TradingDay, len(days)),
		dayset:  make(map[types.TradingDay]bool, len(days)),
		tables:  map[string]SessionTable{},
		noNight: map[int32]bool{},
	}
	copy(c.days, days)
	for i, d := range c.days {
		if i > 0 && d <= c.days[i-1] {
			return nil, fmt.Errorf("交易日列表未严格升序：第 %d 项 %d 不大于前一项 %d",
				i+1, d, c.days[i-1])
		}
		if c.dayset[d] {
			return nil, fmt.Errorf("交易日 %d 重复", d)
		}
		c.dayset[d] = true
	}
	for _, t := range tables {
		if err := t.Validate(); err != nil {
			return nil, err
		}
		k := productKey(t.Exchange, t.Product)
		if _, dup := c.tables[k]; dup {
			return nil, fmt.Errorf("品种 %s 的时段表重复登记", k)
		}
		c.tables[k] = t
	}
	if len(c.tables) == 0 {
		return nil, fmt.Errorf("一张时段表都没有 —— 没有时段就判不出任何时刻的归属")
	}
	for _, d := range nightSuspended {
		c.noNight[d] = true
	}
	return c, nil
}

// IsTradingDay 报告某个交易日在不在列表里。
func (c *Calendar) IsTradingDay(d types.TradingDay) bool { return c.dayset[d] }

// Range 返回日历覆盖的闭区间。超出这个区间的问题一律报错，不外推。
func (c *Calendar) Range() (first, last types.TradingDay) {
	return c.days[0], c.days[len(c.days)-1]
}

// NextTradingDay 返回严格大于 d 的第一个交易日。
//
// ⚠️ d 本身不必是交易日（自然日也能问），但结果必须落在日历覆盖范围内，
// 否则返回 false —— **外推一个交易日出来，比答不上来更坏**。
func (c *Calendar) NextTradingDay(d types.TradingDay) (types.TradingDay, bool) {
	i := sort.Search(len(c.days), func(i int) bool { return c.days[i] > d })
	if i >= len(c.days) {
		return 0, false
	}
	return c.days[i], true
}

// TradingDayAt 答一个墙钟时刻属于哪个交易日。
//
// 判定两条：
//
//	落在**日盘**时段 → 交易日 = 该自然日
//	落在**夜盘**时段 → 交易日 = 该自然日**之后的下一个交易日**
//
// ⚠️ 落在任何时段之外时**报错，不猜**。收盘到夜盘开盘之间的时刻不属于任何交易日，
// 这是一个事实，不是一个需要填充的空缺。
func (c *Calendar) TradingDayAt(t time.Time, ex types.Exchange, product string) (types.TradingDay, error) {
	tab, ok := c.tables[productKey(ex, product)]
	if !ok {
		return 0, fmt.Errorf("没有 %s.%s 的时段表 —— 不知道它什么时候开盘，就答不了归属",
			ex, product)
	}
	clock := clockOf(t)
	natural := naturalDayOf(t)

	// —— 日盘 ——
	for _, s := range tab.Day {
		if !s.Contains(clock) {
			continue
		}
		// ⚠️ 跨零点的日盘不存在；若数据里出现，那是数据错，不是要处理的情形。
		if s.CrossesMidnight() {
			return 0, fmt.Errorf("%s.%s 的日盘时段 %s→%s 跨零点 —— 这是时段表的数据错误",
				ex, product, s.Start, s.End)
		}
		d := types.TradingDay(natural)
		if !c.dayset[d] {
			return 0, fmt.Errorf("%s 落在 %s.%s 的日盘时段内，但自然日 %d 不在交易日列表里 —— "+
				"时段表与交易日历互相矛盾，**不猜**",
				t.In(cnZone).Format("2006-01-02 15:04:05"), ex, product, natural)
		}
		return d, nil
	}

	// —— 夜盘 ——
	for _, s := range tab.Night {
		if !s.Contains(clock) {
			continue
		}
		// ⚠️ 跨零点时段的**后半段**（零点到收盘）锚在**前一个自然日**上：
		// 21:00 开的那场盘，凌晨 00:30 仍是同一场。
		// 不减这一天，凌晨那段会被算成再下一个交易日 —— 整整错一天。
		anchor := natural
		if s.CrossesMidnight() && clock < s.End {
			anchor = naturalDayOf(t.In(cnZone).AddDate(0, 0, -1))
		}
		if c.noNight[anchor] {
			return 0, fmt.Errorf("自然日 %d 当晚**夜盘不开**（长假前等），"+
				"而 %s 落在夜盘时段内 —— 数据与时刻矛盾，**不猜**",
				anchor, t.In(cnZone).Format("2006-01-02 15:04:05"))
		}
		if !c.dayset[types.TradingDay(anchor)] {
			return 0, fmt.Errorf("夜盘锚在自然日 %d，但它不在交易日列表里 —— "+
				"没有日盘的那天不会有夜盘，**不猜**", anchor)
		}
		next, ok := c.NextTradingDay(types.TradingDay(anchor))
		if !ok {
			first, last := c.Range()
			return 0, fmt.Errorf("自然日 %d 之后没有已知的交易日（日历覆盖 %d–%d）—— "+
				"**不外推**", anchor, first, last)
		}
		return next, nil
	}

	return 0, fmt.Errorf("%s 不落在 %s.%s 的任何交易时段内 —— "+
		"收盘到夜盘开盘之间不属于任何交易日，这是事实，不是待填的空缺",
		t.In(cnZone).Format("2006-01-02 15:04:05"), ex, product)
}
