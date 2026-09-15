package conformance

import (
	"io"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance/fixture"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/shopspring/decimal"
)

// MeasuredRules 是实测规则数据；定义与读取在 conformance/fixture（F7a：实测规则只有一个家，对拍测试与本工具读同一份）。
type MeasuredRules = fixture.MeasuredRules

// LoadMeasuredRules 读实测规则文件，见 fixture.LoadMeasuredRules。
func LoadMeasuredRules(r io.Reader) (MeasuredRules, error) { return fixture.LoadMeasuredRules(r) }

// BuildSpecs 把「字典规格」与「实测规则」合成对拍要的 Spec。
//
// ⚠️ 缺任何一项都**不填默认值**，而是不给这个合约的 Spec ——
// 调用方会把它记进「跳过（没登记规则数据）」并报出来。
// 填一个差不多的值会让每一个金额都错，而错出来的数看起来完全正常。
func BuildSpecs(specs map[string]fixture.ContractSpec, rules MeasuredRules) map[string]Spec {
	out := map[string]Spec{}
	for sym, cs := range specs {
		pd, ok := rules.PositionDate[sym]
		if !ok {
			continue
		}
		rate, ok := rules.MarginByProduct[productOf(cs)]
		if !ok {
			continue
		}
		comm, ok := rules.CommissionByProduct[productOf(cs)]
		if !ok {
			continue
		}
		if !cs.VolumeMultiple.IsPositive() {
			continue
		}
		out[sym] = Spec{
			Multiplier: cs.VolumeMultiple,
			Margin: refdata.MarginRates{
				LongByMoney: rate, ShortByMoney: rate,
				LongByVolume: decimal.Zero, ShortByVolume: decimal.Zero,
				CompanyAddOn: decimal.Zero,
			},
			Commission:   comm.Rates,
			PositionDate: pd,
		}
	}
	return out
}

// productOf 取品种代码。
//
// ⚠️ 用字典给的 Product 字段，不从合约代码切尾巴：
// `sym[:len(sym)-4]` 在 `DCE.m2701` 上给 `DCE.`，
// 而那种错法会一路走到「品种没登记」，把原因指向错的地方。
func productOf(cs fixture.ContractSpec) string { return cs.Product }
