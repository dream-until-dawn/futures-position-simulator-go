// Package margin 计算保证金占用。
//
// # 两个待实测项都落在这里
//
//	判别实验 1  保证金按哪个价算            → PriceBasis
//	判别实验 3  单向大边按品种还是按合约合并  → SideScope
//
// 两者都**没有默认值**：零值报错。理由不同但同样硬：
//
//   - 实验 1 错了，可用资金曲线整体错位 → 追保时点错 → 整个风控链路的结论不可信
//   - 实验 3 错了，跨期套利的保证金**可能差一倍**，
//     而 ⚠️ **单合约样本上两种解释给出同一个数**，测不出来
//
// # 两层口径
//
//	交易所保证金 = Σ 持仓量 × 价格 × 乘数 × 交易所率
//	公司保证金   = Σ 持仓量 × 价格 × 乘数 × (交易所率 + 加收)
//
// ⚠️ 风险度、可用资金、追保、强平**全部用公司口径**。加收由期货公司设定，
// 回测里通常拿不到，默认 0 —— 而默认 0 会**低估**保证金占用，
// 这个方向写在文档里，不在代码里悄悄发生。
package margin

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// PriceBasis 是保证金按哪个价算 —— 判别实验 1，尚未收敛。
type PriceBasis uint8

const (
	// PriceBasisUnmeasured 是零值：实验 1 未收敛，使用即报错。
	PriceBasisUnmeasured PriceBasis = iota
	// OpenTodayPreSettleHistory 候选 1：今仓用开仓价，昨仓用昨结算价。
	//
	// 最符合逐日盯市的内在逻辑（结算基线一致），也是本库在实测之前的建模假设——
	// ⚠️ 但「建模假设」不是「结论」，所以它仍然要被显式指定。
	OpenTodayPreSettleHistory
	// PreSettleAll 候选 2：今昨都用昨结算价。
	PreSettleAll
	// LastAll 候选 3：全部用最新成交价，连续重估（加密货币的形态）。
	//
	// ⚠️ 候选 1/2 下盘中保证金基本是**静态的**（只随开平仓变），候选 3 下它随行情
	// 连续变动。两种形态下的可用资金曲线完全不同，进而决定策略何时被追保。
	LastAll
	// SettlementAll 候选 4：全部用今结算价。只有日终成立，盘中拿不到。
	SettlementAll
)

// check 报告计价基准是否已指定。
//
// ⚠️ 它被**前置调用**（在处理任何 leg 之前）与**逐 leg 调用**两处共用，
// 共用是为了让「什么算已指定」只有一份定义 —— 两处各写一遍会分岔，
// 而分岔时没有任何东西会报警。
func (b PriceBasis) check() error {
	if b == PriceBasisUnmeasured {
		return fmt.Errorf("保证金的计价基准未指定：判别实验 1 尚未收敛，"+
			"本库不提供默认值。见 docs/state.md 的 rules_pending。"+
			"（候选：%v / %v / %v / %v）",
			OpenTodayPreSettleHistory, PreSettleAll, LastAll, SettlementAll)
	}
	return nil
}

func (b PriceBasis) String() string {
	switch b {
	case OpenTodayPreSettleHistory:
		return "今仓开仓价/昨仓昨结算价"
	case PreSettleAll:
		return "全部昨结算价"
	case LastAll:
		return "全部最新价"
	case SettlementAll:
		return "全部今结算价"
	}
	return "未实测"
}

// SideScope 是单向大边的合并范围 —— 判别实验 3，尚未收敛。
type SideScope uint8

const (
	// SideScopeUnmeasured 是零值：实验 3 未收敛，使用即报错。
	SideScopeUnmeasured SideScope = iota
	// ByProduct 按**品种**合并：同一品种的全部月份合约一起算大边。
	//
	// 交易所规则是这一种；但 MaxMarginSideAlgorithm 挂在**合约**上，
	// 所以「规则说品种、字段挂合约」这件事本身就是实验 3 要澄清的。
	ByProduct
	// ByInstrument 按**合约**合并：只在同一合约内算大边。
	ByInstrument
	// NoNetting 不走大边：多空各自占用。
	NoNetting
)

// check 报告合并范围是否已指定。
func (s SideScope) check() error {
	if s == SideScopeUnmeasured {
		return fmt.Errorf("单向大边的合并范围未指定：判别实验 3 尚未收敛，"+
			"本库不提供默认值。见 docs/state.md 的 rules_pending。"+
			"（候选：%v / %v / %v）", ByProduct, ByInstrument, NoNetting)
	}
	return nil
}

func (s SideScope) String() string {
	switch s {
	case ByProduct:
		return "按品种合并"
	case ByInstrument:
		return "按合约合并"
	case NoNetting:
		return "不走大边"
	}
	return "未实测"
}

// Rates 是保证金率，**类型住在 refdata**（理由同 fee.Rates）。
type Rates = refdata.MarginRates

// validateRates 检查费率形态。
func validateRates(r Rates) error {
	for _, f := range []struct {
		name string
		v    decimal.Decimal
	}{
		{"多头按金额", r.LongByMoney}, {"多头按手数", r.LongByVolume},
		{"空头按金额", r.ShortByMoney}, {"空头按手数", r.ShortByVolume},
		{"公司加收", r.CompanyAddOn},
	} {
		if f.v.IsNegative() {
			return fmt.Errorf("保证金率「%s」为负（%s）—— 加收为负不成立，"+
				"静默接受会让风险度偏低，而偏低正是最危险的方向", f.name, f.v)
		}
	}
	return nil
}

// Leg 是参与保证金计算的一段持仓。
type Leg struct {
	Instrument types.InstrumentID
	Direction  types.Direction
	Volume     int
	Multiplier decimal.Decimal
	Rates      Rates

	// IsHistory 报告这段是不是昨仓。判据是「有没有经历过结算」，不是开仓日。
	IsHistory bool

	// MaxMarginSide 是该合约是否启用单向大边。
	//
	// ⚠️ 它来自规则数据（CTP 的 MaxMarginSideAlgorithm），**逐合约**，
	// 不是逐交易所的常识。按交易所硬编码在绝大多数合约上都对，
	// 于是错的那几个不会被任何测试抓到。
	MaxMarginSide bool

	OpenPrice        decimal.Decimal
	PreSettlement    decimal.Decimal
	HasPreSettlement bool
	Last             decimal.Decimal
	HasLast          bool
	Settlement       decimal.Decimal
	HasSettlement    bool
}

// price 按基准取该段的计价价。
//
// ⚠️ 缺价时**报错并说明缺的是哪一个**，不拿 0 顶替，也不退回开仓价。
func (l Leg) price(b PriceBasis) (decimal.Decimal, error) {
	need := func(v decimal.Decimal, has bool, name string) (decimal.Decimal, error) {
		if !has {
			return decimal.Zero, fmt.Errorf("%s 要用%s算保证金，但没有这个价 —— "+
				"这是「没有」不是「零」", l.Instrument.Native(), name)
		}
		return v, nil
	}
	if err := b.check(); err != nil {
		return decimal.Zero, err
	}
	switch b {
	case OpenTodayPreSettleHistory:
		if l.IsHistory {
			return need(l.PreSettlement, l.HasPreSettlement, "昨结算价")
		}
		if !l.OpenPrice.IsPositive() {
			return decimal.Zero, fmt.Errorf("%s 的开仓价缺失", l.Instrument.Native())
		}
		return l.OpenPrice, nil
	case PreSettleAll:
		return need(l.PreSettlement, l.HasPreSettlement, "昨结算价")
	case LastAll:
		return need(l.Last, l.HasLast, "最新价")
	case SettlementAll:
		return need(l.Settlement, l.HasSettlement, "今结算价")
	}
	return decimal.Zero, fmt.Errorf("未知的计价基准 %d", b)
}

// one 计算单段的交易所口径与公司口径保证金。
func (l Leg) one(b PriceBasis) (exchange, company decimal.Decimal, err error) {
	if err := validateRates(l.Rates); err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	if l.Volume <= 0 {
		return decimal.Zero, decimal.Zero, fmt.Errorf("手数必须为正，得到 %d", l.Volume)
	}
	if !l.Multiplier.IsPositive() {
		return decimal.Zero, decimal.Zero, fmt.Errorf("合约乘数必须为正，得到 %s", l.Multiplier)
	}
	px, err := l.price(b)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}

	var byMoney, byVolume decimal.Decimal
	switch l.Direction {
	case types.Buy:
		byMoney, byVolume = l.Rates.LongByMoney, l.Rates.LongByVolume
	case types.Sell:
		byMoney, byVolume = l.Rates.ShortByMoney, l.Rates.ShortByVolume
	default:
		return decimal.Zero, decimal.Zero, fmt.Errorf("买卖方向未指定 —— 多空保证金率可以不等，不能默认")
	}

	v := decimal.NewFromInt(int64(l.Volume))
	notional := v.Mul(px).Mul(l.Multiplier)
	// ⚠️ 两项都算并相加，哪怕按手数那一项恒为零。
	exchange = notional.Mul(byMoney).Add(v.Mul(byVolume))
	company = notional.Mul(byMoney.Add(l.Rates.CompanyAddOn)).Add(v.Mul(byVolume))
	return exchange, company, nil
}

// GroupResult 是一个合并组的结果，保留多空两边便于对拍与调试。
type GroupResult struct {
	Key           string // 合并键：品种或合约的线格式
	MaxMarginSide bool   // 该组是否启用单向大边
	LongExchange  decimal.Decimal
	LongCompany   decimal.Decimal
	ShortExchange decimal.Decimal
	ShortCompany  decimal.Decimal
	Exchange      decimal.Decimal // 合并后
	Company       decimal.Decimal
}

// Discriminating 报告本组能否分开「大边」与「不走大边」。
//
// ⚠️ 多空有一边为零时，max(多,空) 与 多+空 **给出同一个数**——
// 这一组对大边没有判别力。拿它去「验证大边实现了」验不出任何东西。
func (g GroupResult) Discriminating() bool {
	return g.LongCompany.IsPositive() && g.ShortCompany.IsPositive()
}

// Result 是一次计算的合计与分解。
type Result struct {
	Exchange decimal.Decimal
	Company  decimal.Decimal
	Groups   []GroupResult
}

// Compute 计算一组持仓的保证金占用。
func Compute(legs []Leg, basis PriceBasis, scope SideScope) (Result, error) {
	var res Result

	// ⚠️ 两个未实测参数都必须**前置**检查，在 len(legs)==0 的早返回**之前**。
	//
	// 这一处曾经不对称：scope 是前置的，basis 只在逐 leg 循环里才被消费，
	// 于是 Compute(nil, PriceBasisUnmeasured, ByProduct) 静默返回零值。
	//
	// 零值报错的设计意图**不是算对数，是告诉调用方它没配这个参数**。
	// 空仓时放行意味着：一个引擎在空仓状态下初始化并跑通第一步会拿到绿灯，
	// 等它第一次开仓错误才出现——**配置错误与它的报告点被错开在不同时间、
	// 不同位置**。而 basis 恰好是「错了 → 可用资金曲线整体错位 → 追保时点错」
	// 的那个参数。
	//
	// 形状上它是「零值假通过」的同族：**守卫通过，是因为什么都没被执行。**
	if err := basis.check(); err != nil {
		return res, err
	}
	if err := scope.check(); err != nil {
		return res, err
	}
	if len(legs) == 0 {
		return res, nil
	}

	type acc struct {
		g     GroupResult
		flags map[bool]bool
	}
	order := []string{}
	groups := map[string]*acc{}

	for _, l := range legs {
		key, err := groupKey(l.Instrument, scope)
		if err != nil {
			return res, err
		}
		ex, co, err := l.one(basis)
		if err != nil {
			return res, err
		}
		a := groups[key]
		if a == nil {
			a = &acc{g: GroupResult{Key: key}, flags: map[bool]bool{}}
			groups[key] = a
			order = append(order, key)
		}
		a.flags[l.MaxMarginSide] = true
		switch l.Direction {
		case types.Buy:
			a.g.LongExchange = a.g.LongExchange.Add(ex)
			a.g.LongCompany = a.g.LongCompany.Add(co)
		case types.Sell:
			a.g.ShortExchange = a.g.ShortExchange.Add(ex)
			a.g.ShortCompany = a.g.ShortCompany.Add(co)
		}
	}

	for _, key := range order {
		a := groups[key]
		if len(a.flags) > 1 {
			// ⚠️ 同一合并组内 MaxMarginSide 不一致，本库**拒绝猜**：
			// 大边到底按不按，取决于交易所对该品种的规定，
			// 而组内不一致意味着规则数据本身有矛盾。
			return res, fmt.Errorf("合并组 %s 内的 MaxMarginSideAlgorithm 不一致 —— "+
				"规则数据有矛盾，本库不猜该按哪个", key)
		}
		a.g.MaxMarginSide = a.flags[true]

		if a.g.MaxMarginSide && scope != NoNetting {
			a.g.Exchange = maxOf(a.g.LongExchange, a.g.ShortExchange)
			a.g.Company = maxOf(a.g.LongCompany, a.g.ShortCompany)
		} else {
			a.g.Exchange = a.g.LongExchange.Add(a.g.ShortExchange)
			a.g.Company = a.g.LongCompany.Add(a.g.ShortCompany)
		}
		res.Exchange = res.Exchange.Add(a.g.Exchange)
		res.Company = res.Company.Add(a.g.Company)
		res.Groups = append(res.Groups, a.g)
	}
	return res, nil
}

func groupKey(inst types.InstrumentID, scope SideScope) (string, error) {
	switch scope {
	case ByProduct:
		return string(inst.Exchange) + "." + inst.Product, nil
	case ByInstrument, NoNetting:
		return inst.Native(), nil
	}
	return "", fmt.Errorf("未知的合并范围 %d", scope)
}

func maxOf(a, b decimal.Decimal) decimal.Decimal {
	if a.GreaterThan(b) {
		return a
	}
	return b
}

// CanDiscriminateScope 报告一组持仓能否把「按品种合并」与「按合约合并」分开。
//
// ⚠️ 需要**同一品种、两个不同合约、方向相反**的持仓。
// 单合约双向持仓时两种解释给出同一个数——这正是实验 3 的样本守卫要卡的东西，
// 而它在真实的跨期套利里却是常态。
//
// 这个判据写在这里，是为了让「样本有没有判别力」可以被**程序**回答，
// 而不是靠谁记得。
func CanDiscriminateScope(legs []Leg) bool {
	type k struct {
		product string
		dir     types.Direction
	}
	seen := map[k]map[string]bool{}
	for _, l := range legs {
		if l.Volume <= 0 {
			continue
		}
		key := k{string(l.Instrument.Exchange) + "." + l.Instrument.Product, l.Direction}
		if seen[key] == nil {
			seen[key] = map[string]bool{}
		}
		seen[key][l.Instrument.Native()] = true
	}
	for key, insts := range seen {
		opp := types.Sell
		if key.dir == types.Sell {
			opp = types.Buy
		}
		other := seen[k{key.product, opp}]
		if len(other) == 0 {
			continue
		}
		// 需要多空落在**不同的合约**上。
		for a := range insts {
			for b := range other {
				if a != b {
					return true
				}
			}
		}
	}
	return false
}
