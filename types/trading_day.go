package types

import (
	"fmt"
	"time"
)

// TradingDay 是**交易日**，形如 20260907。
//
// ⚠️ 它不是自然日，而且这个区分是本库最容易出事的地方之一：
//
//   - 夜盘 21:00 之后属于**下一个**交易日
//   - 周五夜盘属于**下周一**
//   - 一个交易日可以横跨三个自然日（周五夜盘 → 周六凌晨 → 下周一日盘）
//   - 长假前后的映射必须查**交易日历**，不能用「下一个工作日」推
//
// 定成具名类型而不是 int32 别名，防的正是它和自然日在函数签名上混用——
// 混用不会有编译错误，只会在每个长假前后错一次，而那正是保证金上调、
// 风险最高的时候。
//
// ⚠️ **本库不推算交易日。** 它由上游数据带进来（那是落盘字段，不是现算的），
// 或由日历数据给出。本类型只提供比较、格式化与**自然日的换算**，
// 而换算出的自然日**不得**反过来当交易日用——见 CalendarDate 的说明。
type TradingDay int32

// ParseTradingDay 从 yyyymmdd 形式的字符串解析。
//
// ⚠️ DIFF 的业务截面里 trading_day 是**字符串**（如 "20260907"），
// 这是线格式，不是内部表示。
func ParseTradingDay(s string) (TradingDay, error) {
	if len(s) != 8 {
		return 0, fmt.Errorf("交易日 %q 应为 8 位 yyyymmdd", s)
	}
	for i := 0; i < 8; i++ {
		if !isDigit(s[i]) {
			return 0, fmt.Errorf("交易日 %q 含非数字", s)
		}
	}
	d := TradingDay(atoi(s))
	if err := d.Validate(); err != nil {
		return 0, err
	}
	return d, nil
}

// NewTradingDay 从年月日构造。
func NewTradingDay(year, month, day int) TradingDay {
	return TradingDay(year*10000 + month*100 + day)
}

// Validate 检查 d 是不是一个形态合法的 yyyymmdd。
//
// ⚠️ 它只验形态，**不验那天是不是交易日**——后者要查日历，本库不推算。
func (d TradingDay) Validate() error {
	if d <= 0 {
		return fmt.Errorf("交易日 %d 必须为正", d)
	}
	y, m, dd := d.Year(), d.Month(), d.Day()
	if y < 1900 || y > 9999 {
		return fmt.Errorf("交易日 %d 的年份 %d 不合理", d, y)
	}
	if m < 1 || m > 12 {
		return fmt.Errorf("交易日 %d 的月份 %d 应在 1..12", d, m)
	}
	if dd < 1 || dd > 31 {
		return fmt.Errorf("交易日 %d 的日 %d 应在 1..31", d, dd)
	}
	// 再用日历核一遍，挡住 2 月 30 日这类形态合法但不存在的日子。
	t := time.Date(y, time.Month(m), dd, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != dd {
		return fmt.Errorf("交易日 %d 不是一个真实存在的日期", d)
	}
	return nil
}

func (d TradingDay) Year() int  { return int(d) / 10000 }
func (d TradingDay) Month() int { return int(d) / 100 % 100 }
func (d TradingDay) Day() int   { return int(d) % 100 }

// String 返回 yyyymmdd，与 DIFF / CTP 的线格式一致。
func (d TradingDay) String() string { return fmt.Sprintf("%08d", int(d)) }

// CalendarDate 把交易日按字面换算成一个自然日。
//
// ⚠️ **这个换算只用于展示与排序，不承载任何语义。**
// 交易日 20260908 的日盘在 9 月 8 日，而它的夜盘在 9 月 7 日晚上——
// 「交易日对应哪些自然日」是**日历数据**，不是这个函数能回答的。
// 用它去判断「这根 K 线属于哪个交易日」，会在每个夜盘上错一次。
func (d TradingDay) CalendarDate() time.Time {
	return time.Date(d.Year(), time.Month(d.Month()), d.Day(), 0, 0, 0, 0, time.UTC)
}

// Before 报告 d 是否早于 o。
func (d TradingDay) Before(o TradingDay) bool { return d < o }

// After 报告 d 是否晚于 o。
func (d TradingDay) After(o TradingDay) bool { return d > o }
