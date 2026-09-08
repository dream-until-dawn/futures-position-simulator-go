package conformance

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance/fixture"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/shopspring/decimal"
)

// MeasuredRules 是**只能靠实测拿到**的那批规则数据。
//
// ⚠️ 它与合约字典的快照（specs-*.json）刻意分成两份文件：
//
//	specs-*.json           来自**上游字典** —— 乘数、最小变动价位、到期日
//	measured-rules-*.json  来自**柜台行为** —— 保证金率、PositionDateType
//
// 合成一份会让「查过的」与「量出来的」在同一张表里分不开，
// 而两者的可信度、更新方式、以及**出错时该去做什么**都不同：
// 前者重跑一次同步即可，后者要重新设计一次实验。
type MeasuredRules struct {
	// MarginByProduct 是按**品种**的保证金率（kq_facts 2）。
	MarginByProduct map[string]decimal.Decimal
	// PositionDate 是按**合约**的今昨仓类型（kq_facts 24）。
	//
	// ⚠️ 逐合约，不是逐交易所。按交易所推在绝大多数合约上都对，
	// 于是错的那几个不会被任何测试抓到。
	PositionDate map[string]refdata.PositionDateType
}

// LoadMeasuredRules 读实测规则文件。
func LoadMeasuredRules(r io.Reader) (MeasuredRules, error) {
	var raw struct {
		Source     string `json:"source"`
		TradingDay string `json:"trading_day"`
		Note       string `json:"note"`
		Margin     []struct {
			Product  string `json:"product"`
			Rate     string `json:"rate"`
			Evidence string `json:"evidence"`
		} `json:"margin_rate_by_product"`
		PosDate []struct {
			Instrument string `json:"instrument"`
			Type       string `json:"type"`
			Evidence   string `json:"evidence"`
		} `json:"position_date_type"`
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return MeasuredRules{}, fmt.Errorf("读实测规则失败：%w", err)
	}
	out := MeasuredRules{
		MarginByProduct: map[string]decimal.Decimal{},
		PositionDate:    map[string]refdata.PositionDateType{},
	}
	for _, m := range raw.Margin {
		// ⚠️ 每一项都要带证据出处。没有出处的一行与「随手填的」分不开，
		// 而这份文件的全部价值就是「这些数是量出来的」。
		if strings.TrimSpace(m.Evidence) == "" {
			return MeasuredRules{}, fmt.Errorf("品种 %s 的保证金率没有 evidence —— "+
				"⚠️ 这份文件里的每一项都必须指得出实测出处，"+
				"没有出处的一行与随手填的分不开", m.Product)
		}
		d, err := decimal.NewFromString(m.Rate)
		if err != nil || !d.IsPositive() {
			return MeasuredRules{}, fmt.Errorf("品种 %s 的保证金率 %q 不是正数 —— "+
				"⚠️ 率为零会让保证金变成 0，而 0 看起来完全合理", m.Product, m.Rate)
		}
		out.MarginByProduct[m.Product] = d
	}
	for _, p := range raw.PosDate {
		if strings.TrimSpace(p.Evidence) == "" {
			return MeasuredRules{}, fmt.Errorf("合约 %s 的 PositionDateType 没有 evidence",
				p.Instrument)
		}
		switch p.Type {
		case "use_history":
			out.PositionDate[p.Instrument] = refdata.UseHistory
		case "no_use_history":
			out.PositionDate[p.Instrument] = refdata.NoUseHistory
		default:
			return MeasuredRules{}, fmt.Errorf("合约 %s 的 PositionDateType 令牌 %q 不认识"+
				"（认 use_history / no_use_history）—— "+
				"⚠️ 未知令牌不许落成零值：那会变成一份能加载成功、使用时才报错的规则",
				p.Instrument, p.Type)
		}
	}
	if len(out.MarginByProduct) == 0 || len(out.PositionDate) == 0 {
		return MeasuredRules{}, fmt.Errorf("⚠️ 实测规则文件里保证金率 %d 条、"+
			"PositionDateType %d 条 —— 空的那一类会让对拍在那一层上静默跳过全部合约",
			len(out.MarginByProduct), len(out.PositionDate))
	}
	return out, nil
}

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
