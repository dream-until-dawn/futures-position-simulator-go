package fixture

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/shopspring/decimal"
)

// MeasuredRules 是**只能靠实测拿到**的那批规则数据。
//
// ⚠️ 它与合约字典的快照（specs-*.json）刻意分成两份文件：
//
//	specs-*.json           来自**上游字典** —— 乘数、最小变动价位、到期日
//	measured-rules-*.json  来自**柜台行为** —— 保证金率、手续费率、PositionDateType
//
// 合成一份会让「查过的」与「量出来的」在同一张表里分不开，
// 而两者的可信度、更新方式、以及**出错时该去做什么**都不同：
// 前者重跑一次同步即可，后者要重新设计一次实验。
//
// ⚠️ 2026-09-15（F7a）起它只有这一个家：此前 `cmd/oracle` 读 json，而对拍测试另有 marginRates / positionDates / feeRates 三个变量，
// 两份从没比过，手续费率还只在测试变量里 —— 于是 `cmd/oracle -carry` 拿不到手续费率（design.md「门面的形状」§11）。
type MeasuredRules struct {
	// MarginByProduct 是按**品种**的保证金率（kq_facts 2）。
	MarginByProduct map[string]decimal.Decimal
	// CommissionByProduct 是按**品种**的手续费率（probes.md §10.2）。
	CommissionByProduct map[string]MeasuredCommission
	// PositionDate 是按**合约**的今昨仓类型（kq_facts 24）。
	//
	// ⚠️ 逐合约，不是逐交易所。按交易所推在绝大多数合约上都对，
	// 于是错的那几个不会被任何测试抓到。
	PositionDate map[string]refdata.PositionDateType
}

// MeasuredCommission 是一个品种的实测手续费率。
type MeasuredCommission struct {
	// Rates 开仓 / 平昨 / 平今三档同费率（快期模拟实测，kq_facts 18）—— 本口子的事实，不是规则。
	Rates refdata.CommissionRates
	// Classified 报告按额 / 按手是不是**跨月份判出来的**。
	//
	// ⚠️ 为假时只有一个月份：按额与按手在单点上给同一个数，这一行只在标定那个昨结算价上成立，换了昨结算价就是猜。
	Classified bool
	// CalibratedOn 是标定用的合约；对它自己的预测是循环的。
	CalibratedOn string
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
		Commission []struct {
			Product      string `json:"product"`
			ByMoney      string `json:"by_money"`
			ByVolume     string `json:"by_volume"`
			Classified   *bool  `json:"classified"`
			CalibratedOn string `json:"calibrated_on"`
			Evidence     string `json:"evidence"`
		} `json:"commission_rate_by_product"`
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
		MarginByProduct:     map[string]decimal.Decimal{},
		CommissionByProduct: map[string]MeasuredCommission{},
		PositionDate:        map[string]refdata.PositionDateType{},
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
		if _, dup := out.MarginByProduct[m.Product]; dup {
			return MeasuredRules{}, fmt.Errorf("品种 %s 的保证金率出现两次", m.Product)
		}
		out.MarginByProduct[m.Product] = d
	}
	for _, c := range raw.Commission {
		if strings.TrimSpace(c.Evidence) == "" {
			return MeasuredRules{}, fmt.Errorf("品种 %s 的手续费率没有 evidence", c.Product)
		}
		if strings.TrimSpace(c.CalibratedOn) == "" {
			return MeasuredRules{}, fmt.Errorf("品种 %s 的手续费率没写标定用的合约 —— 不写就分不出哪些预测是循环的", c.Product)
		}
		// ⚠️ classified 必须显式写：缺省成 false 与缺省成 true 各有各的错法，而缺省不会有任何动静
		if c.Classified == nil {
			return MeasuredRules{}, fmt.Errorf("品种 %s 的手续费率没写 classified（按额按手是不是跨月份判出来的）", c.Product)
		}
		money, err1 := decimal.NewFromString(c.ByMoney)
		volume, err2 := decimal.NewFromString(c.ByVolume)
		if err1 != nil || err2 != nil || money.IsNegative() || volume.IsNegative() {
			return MeasuredRules{}, fmt.Errorf("品种 %s 的手续费率 按额 %q / 按手 %q 读不成非负数", c.Product, c.ByMoney, c.ByVolume)
		}
		// ⚠️ 分类是二选一的：两者都非零或都为零，说明这条记录没被真正分类过
		if money.IsZero() == volume.IsZero() {
			return MeasuredRules{}, fmt.Errorf("⚠️ 品种 %s 的按额 %s 与按手 %s 同时为零或同时非零 —— 分类是二选一的，这条记录没被真正分类过",
				c.Product, money, volume)
		}
		if _, dup := out.CommissionByProduct[c.Product]; dup {
			return MeasuredRules{}, fmt.Errorf("品种 %s 的手续费率出现两次", c.Product)
		}
		out.CommissionByProduct[c.Product] = MeasuredCommission{
			Rates: refdata.CommissionRates{
				OpenByMoney: money, OpenByVolume: volume,
				CloseByMoney: money, CloseByVolume: volume,
				CloseTodayByMoney: money, CloseTodayByVolume: volume,
			},
			Classified: *c.Classified, CalibratedOn: c.CalibratedOn,
		}
	}
	for _, p := range raw.PosDate {
		if strings.TrimSpace(p.Evidence) == "" {
			return MeasuredRules{}, fmt.Errorf("合约 %s 的 PositionDateType 没有 evidence",
				p.Instrument)
		}
		if _, dup := out.PositionDate[p.Instrument]; dup {
			return MeasuredRules{}, fmt.Errorf("合约 %s 的 PositionDateType 出现两次", p.Instrument)
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
	if len(out.MarginByProduct) == 0 || len(out.CommissionByProduct) == 0 || len(out.PositionDate) == 0 {
		return MeasuredRules{}, fmt.Errorf("⚠️ 实测规则文件里保证金率 %d 条、手续费率 %d 条、"+
			"PositionDateType %d 条 —— 空的那一类会让对拍在那一层上静默跳过全部合约",
			len(out.MarginByProduct), len(out.CommissionByProduct), len(out.PositionDate))
	}
	return out, nil
}
