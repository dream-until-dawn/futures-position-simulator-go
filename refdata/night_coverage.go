package refdata

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// NightCoverage 声明某交易所在自然日闭区间内的停夜盘名单已经完整提供。
// Suspended为空表示明确确认区间内没有停夜盘；Source是来源说明，不是本库认证的证据。
type NightCoverage struct {
	Exchange    types.Exchange
	First, Last int32
	Suspended   []int32
	Source      string
}

// NewCalendarWithNightCoverage 按交易所构造夜盘覆盖；没有声明的交易所或日期查询夜盘时明确失败。
// 不同交易所不能共用推测名单。覆盖为调用者声明，真实性仍由调用者和原始资料保证。
func NewCalendarWithNightCoverage(days []types.TradingDay, tables []SessionTable, coverage []NightCoverage) (*Calendar, error) {
	c, err := NewCalendar(days, tables, nil)
	if err != nil {
		return nil, err
	}
	known := map[types.Exchange]bool{}
	for _, tab := range tables {
		known[tab.Exchange] = true
	}
	for _, entry := range coverage {
		if !entry.Exchange.Valid() || !known[entry.Exchange] || strings.TrimSpace(entry.Source) == "" {
			return nil, fmt.Errorf("夜盘覆盖缺交易所时段表或来源")
		}
		if types.TradingDay(entry.First).Validate() != nil || types.TradingDay(entry.Last).Validate() != nil || entry.First > entry.Last || entry.First < int32(days[0]) || entry.Last > int32(days[len(days)-1]) {
			return nil, fmt.Errorf("夜盘覆盖日期无效或超出日历范围")
		}
		for _, previous := range c.nights {
			if previous.Exchange == entry.Exchange && entry.First <= previous.Last && previous.First <= entry.Last {
				return nil, fmt.Errorf("同一交易所夜盘覆盖区间重叠")
			}
		}
		seen := map[int32]bool{}
		for _, day := range entry.Suspended {
			if types.TradingDay(day).Validate() != nil || day < entry.First || day > entry.Last || seen[day] {
				return nil, fmt.Errorf("覆盖内停夜盘日期无效、重复或越界")
			}
			seen[day] = true
		}
		entry.Suspended = append([]int32{}, entry.Suspended...)
		sort.Slice(entry.Suspended, func(i, j int) bool { return entry.Suspended[i] < entry.Suspended[j] })
		c.nights = append(c.nights, entry)
	}
	sort.Slice(c.nights, func(i, j int) bool {
		if c.nights[i].Exchange != c.nights[j].Exchange {
			return c.nights[i].Exchange < c.nights[j].Exchange
		}
		return c.nights[i].First < c.nights[j].First
	})
	return c, nil
}

// nightStatus 使用开盘锚日查覆盖。未知是资料错误，不是ErrOutsideSession。
func (c *Calendar) nightStatus(exchange types.Exchange, anchor int32) (bool, error) {
	for _, entry := range c.nights {
		if entry.Exchange == exchange && anchor >= entry.First && anchor <= entry.Last {
			for _, day := range entry.Suspended {
				if day == anchor {
					return true, nil
				}
			}
			return false, nil
		}
	}
	if c.nightKnown {
		return c.noNight[anchor], nil
	}
	return false, fmt.Errorf("夜盘覆盖未知：%s 自然日%d，必须提供完整名单或明确空名单", exchange, anchor)
}
