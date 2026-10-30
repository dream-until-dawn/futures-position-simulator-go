package main

import (
	"fmt"
	"time"
)

// 私有登记值仅由准确源码固定，不提供CLI/环境变量覆盖或自动顺延。
const (
	restingQuoteStart     = 1030
	restingQuoteEnd       = 1125
	restingCheckStart     = 1050
	restingCheckEnd       = 1055
	restingOpeningStart   = 1055
	restingOpeningEnd     = 1115
	restingObservationEnd = 1120
)

// parseRestingQuoteClock 区分自然日与交易日；日盘同值不等于可以混用。
// 双日期用于纯解析与独立夜盘反例，不构成任意日期的执行入口。
func parseRestingQuoteClock(naturalDate, tradingDay, clock string) (time.Time, error) {
	if _, err := time.Parse("20060102", tradingDay); err != nil {
		return time.Time{}, fmt.Errorf("行情交易日格式无效")
	}
	return time.ParseInLocation("20060102 15:04:05", naturalDate+" "+clock, probeShanghai)
}

// validateRestingOpeningWindow 每次新开仓前独立检查，不能借用较晚的收尾窗口。
func validateRestingOpeningWindow(now time.Time) error {
	local := now.In(probeShanghai)
	hm := local.Hour()*100 + local.Minute()
	if local.Format("20060102") != restingProbeDate || hm < restingOpeningStart || hm >= restingOpeningEnd {
		return fmt.Errorf("已超出本次登记的新开仓窗口")
	}
	return nil
}
