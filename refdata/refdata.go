// Package refdata 是规则数据：合约规格、保证金率、手续费率、品种索引。
//
// # 键必须是规范形式
//
// ⚠️ **规则数据不能按交易所线格式做键。** 郑商所三位年月十年一轮回：
// `TA701` 既是 2027 年 1 月也是 2037 年 1 月。
//
//	持仓 / 委托 / 成交   按线格式做键安全 —— 相隔十年的两个合约不可能同时持有
//	规则数据             ⚠️ 不安全 —— 一份覆盖十年以上的快照里两者会撞
//
// 这条只在跨十年的历史回测上才显形；十年以内的样本上两种做法给出同一个数
// ——又一个「在最常见的样本上，错误答案等于正确答案」。
//
// # 费率结构体住在这里
//
// 它们是**规则数据**，不是计算逻辑。放在 fee / margin 里会让 refdata
// 反过来 import 它们，而 docs/design.md 的依赖图是 `refdata ← {fee, margin, pnl}`
// ——那样就成环了。
//
// # 缺数据报错，不返回零值
//
// ⚠️ 一个乘数为 0 的合约规格会让所有金额变成 0，而 0 看起来完全合理。
// 所有查询在缺失时**返回错误**。
package refdata

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// PositionDateType 是该合约区不区分今昨仓。
//
// ⚠️ 它是**逐合约**的规则数据（CTP 的 PositionDateType），
// **不是逐交易所的常识**。按交易所硬编码（「上期所区分今昨、其余不区分」）
// 在绝大多数合约上都对，于是**错的那几个不会被任何测试抓到**。
type PositionDateType uint8

const (
	// PositionDateUnknown 是零值：规则数据缺失，使用即报错。
	PositionDateUnknown PositionDateType = iota
	// UseHistory 区分今昨仓：报单必须显式声明平今或平昨。
	UseHistory
	// NoUseHistory 不区分今昨仓：报普通平仓即可。
	//
	// ⚠️ 但**手续费仍可能区分平今**。「持仓不分今昨」不等于「费用不分今昨」，
	// 这两件事各由一个字段控制。
	NoUseHistory
)

func (p PositionDateType) String() string {
	switch p {
	case UseHistory:
		return "区分今昨仓"
	case NoUseHistory:
		return "不区分今昨仓"
	}
	return "未知"
}

// MarginRates 是一个合约的保证金率。多空可以不等，两种形态并存。
type MarginRates struct {
	LongByMoney   decimal.Decimal
	LongByVolume  decimal.Decimal
	ShortByMoney  decimal.Decimal
	ShortByVolume decimal.Decimal

	// CompanyAddOn 是期货公司在交易所率之上的加收（绝对比例）。
	//
	// ⚠️ 默认 0 意味着公司口径 = 交易所口径，这会**低估**保证金占用。
	// 回测里通常拿不到真实加收，所以默认如此；但低估的方向必须被知道。
	CompanyAddOn decimal.Decimal
}

// CommissionRates 是一个合约的六个手续费率。
//
// ⚠️ 天勤的 `quotes.{symbol}.commission` 是「每手手续费」的**单一数值**，
// 不是这六个率。用它填这里会**丢掉平今这个维度**——而日内策略的手续费
// 几乎全部落在平今上。真值来自柜台（CTP 的 InstrumentCommissionRate）。
type CommissionRates struct {
	OpenByMoney        decimal.Decimal
	OpenByVolume       decimal.Decimal
	CloseByMoney       decimal.Decimal
	CloseByVolume      decimal.Decimal
	CloseTodayByMoney  decimal.Decimal
	CloseTodayByVolume decimal.Decimal
}

// Instrument 是合约规格。
type Instrument struct {
	ID types.InstrumentID

	VolumeMultiple decimal.Decimal // 合约乘数
	PriceTick      decimal.Decimal // 最小变动价位

	PositionDateType PositionDateType
	MaxMarginSide    bool // 是否启用单向大边；逐合约，见 PositionDateType 的说明

	ExpireDate types.TradingDay // 最后交易日
	IsTrading  bool

	MinLimitOrderVolume int
	MaxLimitOrderVolume int

	// PriceLimitRatio 是涨跌幅比例，用于从昨结算价推涨跌停。
	//
	// ⚠️ 与 Has 成对：交易所会临时调整（连续涨跌停扩板、节假日前上调），
	// 而「比例是零」与「不知道比例」必须分开——前者意味着不许波动，
	// 后者意味着本库**跳过涨跌停校验并给出原因**。
	PriceLimitRatio    decimal.Decimal
	HasPriceLimitRatio bool
}

// Validate 检查合约规格的形态。
func (i Instrument) Validate() error {
	if !i.VolumeMultiple.IsPositive() {
		// ⚠️ 乘数为 0 会让所有金额变成 0，而 0 看起来完全合理。
		return fmt.Errorf("%s 的合约乘数是 %s，必须为正", i.ID.Native(), i.VolumeMultiple)
	}
	if !i.PriceTick.IsPositive() {
		return fmt.Errorf("%s 的最小变动价位是 %s，必须为正", i.ID.Native(), i.PriceTick)
	}
	if i.PositionDateType == PositionDateUnknown {
		return fmt.Errorf("%s 的 PositionDateType 未知 —— 它决定报单要不要显式声明平今平昨，"+
			"不能默认；按交易所硬编码在绝大多数合约上都对，而错的那几个不会被测出来",
			i.ID.Native())
	}
	if i.MinLimitOrderVolume < 0 || i.MaxLimitOrderVolume < 0 {
		return fmt.Errorf("%s 的手数上下限为负", i.ID.Native())
	}
	if i.MaxLimitOrderVolume > 0 && i.MinLimitOrderVolume > i.MaxLimitOrderVolume {
		return fmt.Errorf("%s 的手数下限 %d 大于上限 %d",
			i.ID.Native(), i.MinLimitOrderVolume, i.MaxLimitOrderVolume)
	}
	if i.HasPriceLimitRatio && i.PriceLimitRatio.IsNegative() {
		return fmt.Errorf("%s 的涨跌幅比例 %s 为负", i.ID.Native(), i.PriceLimitRatio)
	}
	return nil
}

// TickRounding 是把理论涨跌停价对齐到最小变动价位的方式。
//
// ⚠️ 零值是 TickRoundingUnknown，使用即报错。
//
// 一个默认落进某一种的零值会静默算错，而错的量级正好是**不到一个 tick** ——
// 那是最不容易被看见的错法：数字看起来完全正常，只是报单会被拒。
type TickRounding uint8

const (
	// TickRoundingUnknown 是零值：没指定。⚠️ 使用即报错。
	TickRoundingUnknown TickRounding = iota
	// TickFloor 向下取整到最小变动价位。⚠️ 实测上期所是这一种（probes.md §12）。
	TickFloor
	// TickCeil 向上取整。
	TickCeil
	// TickHalfUp 四舍五入。⚠️ 实测大商所是这一种（probes.md §12）。
	TickHalfUp
	// TickNone 不取整，返回理论值。
	//
	// ⚠️ 它**不是**「默认」，是一个要显式选的选项：
	// 不取整的涨跌停价不是一个合法价格（3315.9 不是 tick 的整数倍），
	// 拿它去下单会被拒。选它只应当出于「我要看理论值」这一个理由。
	TickNone
)

func (r TickRounding) String() string {
	switch r {
	case TickFloor:
		return "向下取整"
	case TickCeil:
		return "向上取整"
	case TickHalfUp:
		return "四舍五入"
	case TickNone:
		return "不取整"
	}
	return "未指定"
}

// PriceLimits 从昨结算价推出涨跌停价，并对齐到最小变动价位。
//
// ⚠️ 基线是**昨结算价**，不是昨收盘价。
//
// ⚠️ rounding 必须显式给，零值报错。理由是实测：
// **两家交易所的取整方向不同** —— 上期所向下取整、大商所四舍五入
// （probes.md §12，各两个品种）。默认挑一种会在另一家上静默错，
// 而错的量级不到一个 tick：数字看起来完全正常，只是那个价报不出去。
//
// ⚠️ 而「按交易所硬编码」同样不行 —— 那正是 PositionDateType 上栽过的形状：
// 在绝大多数合约上都对，于是错的那几个不会被测出来。
// 取整方向应当随规则数据来，本结构体尚未承载它，所以由调用方传。
//
// ok 为 false 表示**推不出来**（没有涨跌幅比例、没有昨结算价、或没指定取整）。
// 调用方必须把它当成「跳过涨跌停校验并给出原因」，而**不是**「没有涨跌停限制」。
func (i Instrument) PriceLimits(preSettlement decimal.Decimal, hasPreSettlement bool,
	rounding TickRounding) (upper, lower decimal.Decimal, ok bool) {

	if !i.HasPriceLimitRatio || !hasPreSettlement || !preSettlement.IsPositive() {
		return decimal.Zero, decimal.Zero, false
	}
	if rounding == TickRoundingUnknown {
		return decimal.Zero, decimal.Zero, false
	}
	one := decimal.NewFromInt(1)
	up := preSettlement.Mul(one.Add(i.PriceLimitRatio))
	lo := preSettlement.Mul(one.Sub(i.PriceLimitRatio))
	if rounding == TickNone {
		return up, lo, true
	}
	if !i.PriceTick.IsPositive() {
		// ⚠️ 要取整却没有最小变动价位 —— 那是「推不出来」，不是「不用取整」。
		return decimal.Zero, decimal.Zero, false
	}
	return snapToTick(up, i.PriceTick, rounding),
		snapToTick(lo, i.PriceTick, rounding), true
}

// snapToTick 把价格对齐到最小变动价位。
//
// ⚠️ 上下两边用**同一个**方向，不是「上取上、下取下」。
// 实测支持这一点：上期所 rb2701 昨结 3158、5%，理论 3315.9 / 3000.1，
// 柜台给 3315 / 3000 —— **两边都是向下**（probes.md §12）。
// 「上下各取一边」是个很自然的猜测，而它在这个样本上是错的。
func snapToTick(px, tick decimal.Decimal, r TickRounding) decimal.Decimal {
	n := px.Div(tick)
	switch r {
	case TickFloor:
		n = n.Floor()
	case TickCeil:
		n = n.Ceil()
	case TickHalfUp:
		n = n.Round(0)
	}
	return n.Mul(tick)
}

// Provider 提供规则数据查询。
//
// 实现有两类，服务于两种截然不同的需求：
//
//   - Snapshot 是**不可变快照**，Version 恒定。⚠️ 回测必须用它——
//     规则一旦在回测途中改变，结果就不再可复现，而且不会有任何报错。
//   - 运行时拉取的实现（v0.1.0 之后）Version 随刷新推进。
//
// 所有查询在缺失时**返回错误**，不返回零值。
type Provider interface {
	// Instrument 返回合约规格。
	Instrument(id types.InstrumentID) (Instrument, error)
	// MarginRates 返回保证金率。hedge 决定档位——套保通常低于投机。
	MarginRates(id types.InstrumentID, hedge types.HedgeFlag) (MarginRates, error)
	// CommissionRates 返回手续费率。
	CommissionRates(id types.InstrumentID, hedge types.HedgeFlag) (CommissionRates, error)
	// ProductInstruments 枚举同一品种下的全部合约，按到期日排序。
	//
	// ⚠️ 建这个索引是因为它**很便宜**——解析郑商所的三位年月本来就要拆出品种
	// ——**不是因为判别实验 3 有了结论**。若实验 3 真跑出「大边按品种合并」，
	// 这个索引会被读成「当初就验过了」，而那是一个未经验证的假设变成既成事实。
	ProductInstruments(exchange types.Exchange, product string) []types.InstrumentID
	// Version 是本份规则数据的生成时刻（毫秒）。
	//
	// 它是判断规则是否已变更的唯一依据：值改变即意味着底层数据被刷新过。
	Version() int64
}

// key 是规则数据的存储键：**规范形式**，不是线格式。
func key(id types.InstrumentID) string { return id.Canonical() }

func productKey(ex types.Exchange, product string) string {
	return string(ex) + "." + strings.ToUpper(product)
}

// rateSet 是一个合约按投机套保标志分档的费率。
type rateSet struct {
	margin     map[types.HedgeFlag]MarginRates
	commission map[types.HedgeFlag]CommissionRates
}

// Snapshot 是不可变的规则数据快照。
//
// 构造完成后不再改变，Version 恒定。并发读安全。
type Snapshot struct {
	version     int64
	instruments map[string]Instrument
	rates       map[string]*rateSet
	byProduct   map[string][]types.InstrumentID
}

// 编译期断言：Snapshot 必须满足 Provider。
var _ Provider = (*Snapshot)(nil)

// Builder 构造 Snapshot。
type Builder struct {
	version     int64
	instruments map[string]Instrument
	rates       map[string]*rateSet
	errs        []string
}

// NewBuilder 建一个构造器。version 是数据的生成时刻（毫秒）。
func NewBuilder(version int64) *Builder {
	return &Builder{
		version:     version,
		instruments: map[string]Instrument{},
		rates:       map[string]*rateSet{},
	}
}

// AddInstrument 加入一个合约规格。
func (b *Builder) AddInstrument(inst Instrument) *Builder {
	if err := inst.Validate(); err != nil {
		b.errs = append(b.errs, err.Error())
		return b
	}
	k := key(inst.ID)
	if _, dup := b.instruments[k]; dup {
		// ⚠️ 规范形式撞键意味着同一个合约被加了两次，或键的构造出了问题。
		// 静默覆盖会让后一份悄悄赢，而两份规格不同时没有任何提示。
		b.errs = append(b.errs, fmt.Sprintf("合约 %s 重复加入", k))
		return b
	}
	b.instruments[k] = inst
	return b
}

// AddMarginRates 加入某合约某投机套保档的保证金率。
func (b *Builder) AddMarginRates(id types.InstrumentID, hedge types.HedgeFlag, r MarginRates) *Builder {
	if hedge == types.HedgeUnknown {
		b.errs = append(b.errs, fmt.Sprintf("%s 的保证金率未指定投机套保标志 —— 它决定档位", key(id)))
		return b
	}
	if err := validateMargin(r, key(id)); err != nil {
		b.errs = append(b.errs, err.Error())
		return b
	}
	b.rateSetOf(id).margin[hedge] = r
	return b
}

// AddCommissionRates 加入某合约某投机套保档的手续费率。
func (b *Builder) AddCommissionRates(id types.InstrumentID, hedge types.HedgeFlag, r CommissionRates) *Builder {
	if hedge == types.HedgeUnknown {
		b.errs = append(b.errs, fmt.Sprintf("%s 的手续费率未指定投机套保标志", key(id)))
		return b
	}
	if err := validateCommission(r, key(id)); err != nil {
		b.errs = append(b.errs, err.Error())
		return b
	}
	b.rateSetOf(id).commission[hedge] = r
	return b
}

func (b *Builder) rateSetOf(id types.InstrumentID) *rateSet {
	k := key(id)
	rs := b.rates[k]
	if rs == nil {
		rs = &rateSet{
			margin:     map[types.HedgeFlag]MarginRates{},
			commission: map[types.HedgeFlag]CommissionRates{},
		}
		b.rates[k] = rs
	}
	return rs
}

// Build 生成快照。任何一处输入不合法都会在这里一次性报出来。
func (b *Builder) Build() (*Snapshot, error) {
	if b.version <= 0 {
		b.errs = append(b.errs, "版本戳必须为正 —— 它是判断规则是否变更的唯一依据")
	}
	// ⚠️ 费率挂在一个不存在的合约上，说明两份数据来自不同的来源或不同的时点。
	// 静默忽略会让那份费率永远查不到，而查不到时的报错指向合约缺失，
	// 把排查引到别处去。
	for k := range b.rates {
		if _, ok := b.instruments[k]; !ok {
			b.errs = append(b.errs, fmt.Sprintf("费率挂在不存在的合约 %s 上", k))
		}
	}
	if len(b.errs) > 0 {
		sort.Strings(b.errs)
		return nil, fmt.Errorf("规则数据有 %d 处问题：\n  %s",
			len(b.errs), strings.Join(b.errs, "\n  "))
	}

	byProduct := map[string][]types.InstrumentID{}
	for _, inst := range b.instruments {
		pk := productKey(inst.ID.Exchange, inst.ID.Product)
		byProduct[pk] = append(byProduct[pk], inst.ID)
	}
	for pk := range byProduct {
		ids := byProduct[pk]
		sort.Slice(ids, func(i, j int) bool { return ids[i].YearMonth() < ids[j].YearMonth() })
	}
	return &Snapshot{
		version:     b.version,
		instruments: b.instruments,
		rates:       b.rates,
		byProduct:   byProduct,
	}, nil
}

// Version 返回本份规则数据的生成时刻（毫秒）。
func (s *Snapshot) Version() int64 { return s.version }

// Instrument 返回合约规格。
func (s *Snapshot) Instrument(id types.InstrumentID) (Instrument, error) {
	inst, ok := s.instruments[key(id)]
	if !ok {
		return Instrument{}, fmt.Errorf("规则数据里没有合约 %s（规范形式 %s）—— "+
			"⚠️ 这是「没有」不是「零」，不要拿零值规格继续算",
			id.Native(), id.Canonical())
	}
	return inst, nil
}

// MarginRates 返回保证金率。
func (s *Snapshot) MarginRates(id types.InstrumentID, hedge types.HedgeFlag) (MarginRates, error) {
	rs := s.rates[key(id)]
	if rs == nil {
		return MarginRates{}, fmt.Errorf("规则数据里没有 %s 的费率", id.Native())
	}
	r, ok := rs.margin[hedge]
	if !ok {
		return MarginRates{}, fmt.Errorf("规则数据里没有 %s 在「%v」档的保证金率 —— "+
			"⚠️ 投机套保标志决定保证金率，不能退回别的档", id.Native(), hedge)
	}
	return r, nil
}

// CommissionRates 返回手续费率。
func (s *Snapshot) CommissionRates(id types.InstrumentID, hedge types.HedgeFlag) (CommissionRates, error) {
	rs := s.rates[key(id)]
	if rs == nil {
		return CommissionRates{}, fmt.Errorf("规则数据里没有 %s 的费率", id.Native())
	}
	r, ok := rs.commission[hedge]
	if !ok {
		return CommissionRates{}, fmt.Errorf("规则数据里没有 %s 在「%v」档的手续费率", id.Native(), hedge)
	}
	return r, nil
}

// ProductInstruments 枚举同一品种下的全部合约，按到期年月排序。
//
// ⚠️ 见 Provider 接口上的说明：建它是因为它便宜，不是因为实验 3 有了结论。
func (s *Snapshot) ProductInstruments(ex types.Exchange, product string) []types.InstrumentID {
	src := s.byProduct[productKey(ex, product)]
	out := make([]types.InstrumentID, len(src))
	copy(out, src)
	return out
}

// Count 返回快照里的合约数，用于完整性核对。
//
// ⚠️ 一份被截断的规则数据若恰好在两条记录之间断开，**解析不一定报错**，
// 它只是静默地少几个合约。落盘前对得上数，是发现这件事的唯一机会。
func (s *Snapshot) Count() int { return len(s.instruments) }

func validateMargin(r MarginRates, who string) error {
	for _, f := range []struct {
		name string
		v    decimal.Decimal
	}{
		{"多头按金额", r.LongByMoney}, {"多头按手数", r.LongByVolume},
		{"空头按金额", r.ShortByMoney}, {"空头按手数", r.ShortByVolume},
		{"公司加收", r.CompanyAddOn},
	} {
		if f.v.IsNegative() {
			return fmt.Errorf("%s 的保证金率「%s」为负（%s）—— 加收为负不成立，"+
				"静默接受会让风险度偏低，而偏低正是最危险的方向", who, f.name, f.v)
		}
	}
	return nil
}

func validateCommission(r CommissionRates, who string) error {
	for _, f := range []struct {
		name string
		v    decimal.Decimal
	}{
		{"开仓按金额", r.OpenByMoney}, {"开仓按手数", r.OpenByVolume},
		{"平昨按金额", r.CloseByMoney}, {"平昨按手数", r.CloseByVolume},
		{"平今按金额", r.CloseTodayByMoney}, {"平今按手数", r.CloseTodayByVolume},
	} {
		if f.v.IsNegative() {
			return fmt.Errorf("%s 的手续费率「%s」为负（%s）—— 返佣不在本库范围内",
				who, f.name, f.v)
		}
	}
	return nil
}
