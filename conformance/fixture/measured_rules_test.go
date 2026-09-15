package fixture

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
)

// measuredRulesPath 是实测规则的唯一出处（F7a）。对拍测试与 `cmd/oracle` 读同一份。
func measuredRulesPath() string {
	return filepath.Join("..", "..", "testdata", "refdata", "measured-rules-20260909.json")
}

// measured 是从实测规则 json 读出来的那一份。
//
// ⚠️ F7a 之前对拍测试自带 marginRates / positionDates / feeRates 三个变量，与 json 两个家、从没比过。
// 读不出来就让整个包的测试一起失败 —— 不给默认值。
var measured = func() MeasuredRules {
	f, err := os.Open(measuredRulesPath())
	if err != nil {
		panic("读实测规则：" + err.Error())
	}
	defer f.Close()
	r, err := LoadMeasuredRules(f)
	if err != nil {
		panic(err.Error())
	}
	return r
}()

// marginRates 是各品种的保证金率（kq_facts 2 / probes.md §7.2：至少三档 rb/m 7%、i/cu 11%、ag 22% ——
// 曾经用三个同为 7% 的样本得出过「全局统一简化费率」这个相反结论：**同值的样本不构成「统一」的证据**）。
var marginRates = func() map[string]string {
	out := map[string]string{}
	for p, d := range measured.MarginByProduct {
		out[p] = d.String()
	}
	return out
}()

// positionDates 是**实测过的** PositionDateType，逐合约（kq_facts 24）。
//
// ⚠️ 刻意不按交易所推：猜对的猜测与查过的事实在结果上长得一模一样，直到某个合约不一样为止。
// 没实测过的合约一个都不填 —— 要用到时报错，而报错正是要的。
var positionDates map[string]refdata.PositionDateType = measured.PositionDate

// feeRates 是各品种的手续费口径（probes.md §10.2），按品种排序。
//
// ⚠️ 分类（按额 / 按手）是跨月份判出来的：rb 按额、m 按手；i / cu / ag 各只有一个月份，predictive=false（json 里 classified=false）。
var feeRates = func() []feeRate {
	products := make([]string, 0, len(measured.CommissionByProduct))
	for p := range measured.CommissionByProduct {
		products = append(products, p)
	}
	sort.Strings(products)
	out := make([]feeRate, 0, len(products))
	for _, p := range products {
		c := measured.CommissionByProduct[p]
		out = append(out, feeRate{product: p, byMoney: c.Rates.OpenByMoney.String(), byVolume: c.Rates.OpenByVolume.String(),
			calibratedOn: c.CalibratedOn, predictive: c.Classified})
	}
	return out
}()
