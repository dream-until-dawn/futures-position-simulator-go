package refdata

import (
	"fmt"
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
