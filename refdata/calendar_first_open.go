package refdata

import (
	"fmt"
	"sort"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// FirstOpen返回指定交易日的首个开盘时刻，使用固定北京时间；只读查表，不推算休市。
// 有夜盘时必须有前一已登记交易日及该锚日的已知夜盘状态；未知不回退到日盘。
// 日历、时段和停夜盘声明的完整真实性仍由调用者保证，不能据本查询认证资料来源。
func (c *Calendar) FirstOpen(day types.TradingDay, ex types.Exchange, product string) (time.Time, error) {
	if c == nil {
		return time.Time{}, fmt.Errorf("首开查询缺少日历")
	}
	if err := day.Validate(); err != nil {
		return time.Time{}, fmt.Errorf("首开交易日无效：%w", err)
	}
	index := sort.Search(len(c.days), func(i int) bool { return c.days[i] >= day })
	if index == len(c.days) || c.days[index] != day {
		return time.Time{}, fmt.Errorf("首开交易日%d不在日历中", day)
	}
	tab, ok := c.tables[productKey(ex, product)]
	if !ok {
		return time.Time{}, fmt.Errorf("首开查询缺少%s.%s时段表", ex, product)
	}
	for _, session := range tab.Day {
		if session.CrossesMidnight() {
			return time.Time{}, fmt.Errorf("首开日盘时段跨零点，资料矛盾")
		}
	}

	anchor, sessions, night := day, tab.Day, false
	if len(tab.Night) > 0 {
		if index == 0 {
			return time.Time{}, fmt.Errorf("首开查询缺少前一交易日锚，不能默认日盘")
		}
		previous := c.days[index-1]
		suspended, err := c.nightStatus(ex, int32(previous))
		if err != nil {
			return time.Time{}, fmt.Errorf("首开查询夜盘状态未知：%w", err)
		}
		if !suspended {
			anchor, sessions, night = previous, tab.Night, true
		}
	}
	if len(sessions) == 0 {
		return time.Time{}, fmt.Errorf("首开查询没有可用时段，不能补造开盘时刻")
	}
	start := sessions[0].Start
	for _, session := range sessions[1:] {
		if session.Start < start {
			start = session.Start
		}
	}
	// CalendarDate是UTC展示日期；这里明确按自然日字段构造北京时间，避免偏移八小时。
	opening := time.Date(anchor.Year(), time.Month(anchor.Month()), anchor.Day(), 0, 0, 0, 0, cnZone).Add(time.Duration(start) * time.Second)
	span, err := c.SessionAt(opening, ex, product)
	if err != nil {
		return time.Time{}, fmt.Errorf("首开候选归属校验失败：%w", err)
	}
	if span.TradingDay != day || span.Night != night || span.Anchor != int32(anchor) {
		return time.Time{}, fmt.Errorf("首开候选与日历或日夜盘时段互相矛盾")
	}
	return opening, nil
}
