// Package order 是报单校验：八项检查，按拒绝优先级。
//
// # ⚠️ 本包最要紧的一条：**跑不了的校验不许当成通过**
//
// 八项里有几项需要本库拿不到的输入（手数上下限、限仓、交易时段）。
// 一个「拿不到就跳过」的校验器，在缺输入时会返回「通过」——
// 而那个「通过」与真的全部查过长得**一模一样**。
//
// 所以 `Check` 返回的不是 `error`，是一份 [Result]：
//
//	Rejected   哪一项拒了、为什么
//	Unchecked  哪几项**没能查**、缺什么
//
// 调用方拿到 `Rejected == nil` 时**不能**直接读成「这笔单没问题」，
// 它只意味着「查过的那几项都过了」。⚠️ 这个区分不是学究气：
// 「可用资金够不够」查不了的时候，回测会开出实际开不出的仓，
// 而那是 silent-risks.md 第 7 条点名的形状。
//
// # 校验顺序照文档，不照方便
//
// cn-futures-rules.md §9 给了一张**文档**上的拒绝优先级表。顺序要紧是因为
// 柜台只回一个拒因：同一笔单可能同时违反两项，而报哪一个决定了
// 使用者去改哪里。乱序会让本库与柜台在「拒因是什么」上分岔，
// 而两边都判「拒绝」，差异不会以失败的形式出现。
//
// 20260909 头三对有了实测（快期模拟，SHFE.ag2702 与 DCE.i2701）：
// 最小变动价位 > 涨跌停 > 可平量，与文档表一致。
//
// ⚠️ 那一轮我先得出过**相反**的结论并真的把两项对调了。翻案的原因不是
// 又量了一次，是发现**对照组根本不成立**：构造「偏离整数倍」用的零头是
// 0.7 个 tick，而快期**不把它当偏离**（kq_facts 45：零头 ≥ 半个 tick 照单全收）。
// 那笔单只违反了一项。⚠️ 更该记住的是零层守卫为什么没拦住：
// 它用**本库的**判据核对「违规成立了吗」，而分歧恰恰在判据本身。
//
// ⚠️ 边界要一起记住：两个合约、两家交易所、一个口子；
// 八项两两有 28 对，实测只覆盖 3 对，剩下 25 对至今没有任何证据 ——
// 顺序在那 25 对上是**猜的**。
package order

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Check 是八项校验之一。
type Check uint8

// ⚠️ 取值顺序**就是拒绝优先级**（cn-futures-rules.md §9），别重排。
const (
	CheckUnknown Check = iota
	// CheckTradable 合约是否可交易。
	CheckTradable
	// CheckSession 是否在交易时段内。⚠️ 本库目前查不了，见 Result.Unchecked。
	//
	// ⚠️ 还有第二层，与「查不了」不是一回事：**快期模拟自己也不查**。
	// 20260909 从已有语料横着读：313 笔从未被拒过的委托里有 217 笔
	// 落在该品种的任何时段之外，`DCE.i2701` 甚至在夜盘收盘后近四小时被收下
	// （kq_facts 48，守卫 TestOrdersAcceptedOutsideSession）。
	// 于是在这个口子上，**一个查时段的实现与一个不查的实现对拍结果相同** ——
	// 那是一条盲区，不是一处待办。
	CheckSession
	// CheckPriceTick 价格是否为最小变动价位的整数倍。
	//
	// ⚠️ 它排在 CheckPriceLimit **前面**，与 cn-futures-rules.md §9 的文档表一致，
	// 且 20260909 有了实测支撑：同时越涨停又偏离整数倍的单，
	// 快期模拟答「下单价格不是价格单位的整倍数」。
	//
	// ⚠️ **我曾据一次实测把这两项对调过，那次是错的**，理由值得留在这里：
	// 那一笔的价格零头是 0.7 个 tick，而快期**根本没把它当成偏离**
	// （见 kq_facts 45：零头 ≥ 半个 tick 的价格柜台照单全收）。
	// 于是那笔单只违反了涨跌停一项，「同时违反两项」从一开始就不成立。
	// 详见 docs/probes.md §14。
	CheckPriceTick
	// CheckPriceLimit 价格是否在涨跌停之内。
	CheckPriceLimit
	// CheckVolumeRange 手数是否在上下限内。
	CheckVolumeRange
	// CheckClosable 平仓量是否超过可平量。⚠️ **今昨分别校验**。
	CheckClosable
	// CheckFunds 可用资金是否够（保证金 + 手续费）。
	CheckFunds
	// CheckPositionLimit 限仓。
	CheckPositionLimit
)

func (c Check) String() string {
	switch c {
	case CheckTradable:
		return "合约可交易"
	case CheckSession:
		return "交易时段"
	case CheckPriceTick:
		return "最小变动价位"
	case CheckPriceLimit:
		return "涨跌停"
	case CheckVolumeRange:
		return "手数上下限"
	case CheckClosable:
		return "可平量"
	case CheckFunds:
		return "可用资金"
	case CheckPositionLimit:
		return "限仓"
	}
	return "未知校验"
}

// allChecks 是八项，**按拒绝优先级**。
//
// ⚠️ 它同时是「一共有几项」的唯一来源：`Result` 用它算「查过几项」，
// 加了一项而忘了加进这里，那一项就永远不在分母里 ——
// 而覆盖率看起来只会更好。
var allChecks = []Check{
	CheckTradable, CheckSession, CheckPriceTick, CheckPriceLimit,
	CheckVolumeRange, CheckClosable, CheckFunds, CheckPositionLimit,
}

// Request 是一笔报单。
type Request struct {
	Instrument types.InstrumentID
	Direction  types.Direction
	Offset     types.Offset
	Hedge      types.HedgeFlag
	// Price 是限价。⚠️ 本包只处理限价单：市价单的成交价由盘口决定，
	// 而本库没有盘口 —— 假装能校验它，等于假装知道会成交在哪。
	Price  decimal.Decimal
	Volume int
}

// Facts 是校验要用到的**外部事实**。
//
// ⚠️ 每一项都配一个 Has*：缺席与零值必须分开。
// 「可用资金是 0」与「不知道可用资金」在这里后果相反 ——
// 前者应当拒单，后者应当报「查不了」。
type Facts struct {
	Instrument    refdata.Instrument
	HasInstrument bool

	PreSettlement    decimal.Decimal
	HasPreSettlement bool
	// Rounding 是涨跌停对齐 tick 的方向。⚠️ 零值即「没指定」，
	// 那时涨跌停查不了 —— 两家交易所方向不同，挑一个会静默错。
	Rounding refdata.TickRounding

	// Position 是该合约当前的持仓。nil 表示**不知道**，不是「空仓」。
	Position *position.Position

	// Available 是可用资金；Need 是这笔单要占用的（保证金 + 手续费）。
	//
	// ⚠️ Need 由调用方算好传进来，本包不算：它要费率与保证金率，
	// 那是 fee / margin 的职责，在这里重算一遍就有了第二份实现。
	Available    decimal.Decimal
	HasAvailable bool
	Need         decimal.Decimal
	HasNeed      bool

	// PositionLimit 是交易所限仓；HasPositionLimit 为假表示不知道。
	PositionLimit    int
	HasPositionLimit bool

	// InSession 报告此刻在不在交易时段内；HasSession 为假表示查不了。
	//
	// ⚠️ 本库的 Calendar 比柜台保守（kq_facts 23）：时段之外它拒答。
	// 那不是缺陷，但意味着「不在时段内」与「不知道在不在」要分开。
	InSession  bool
	HasSession bool
}

// Rejection 是一次拒绝。
type Rejection struct {
	Check  Check
	Reason string
}

// Unchecked 是一项**没能查**的校验。
type Unchecked struct {
	Check Check
	// Missing 是缺了什么。⚠️ 必须说得出缺什么，不能只说「跳过了」：
	// 「跳过了」不指向任何行动，而「缺昨结算价」指向去取行情。
	Missing string
}

// Result 是一次校验的结果。
//
// ⚠️ `Rejected == nil` **不等于**「这笔单没问题」，只等于
// 「查过的那几项都过了」。要判后者得同时看 `Unchecked` 是不是空的。
// 提供 [Result.FullyChecked] 就是为了让这个区分在调用处必须被写出来。
type Result struct {
	// Rejected 是第一个拒绝（按优先级）。nil 表示查过的都过了。
	Rejected *Rejection
	// Unchecked 是没能查的那几项，按优先级排列。
	Unchecked []Unchecked
}

// FullyChecked 报告八项**是不是全都真的查过了**。
func (r Result) FullyChecked() bool { return len(r.Unchecked) == 0 }

// OK 报告这笔单是否应当被接受。
//
// ⚠️ 它**要求全部查过**：有任何一项没查成，一律返回 false。
// 理由是回测的用途 —— 一笔「因为查不了所以放过」的单，
// 会让回测开出实际开不出的仓（silent-risks.md 第 7 条）。
// 调用方明知故犯时请自己读 Rejected，那时它是一个**显式**的选择。
func (r Result) OK() bool { return r.Rejected == nil && r.FullyChecked() }

func (r Result) String() string {
	var b strings.Builder
	if r.Rejected != nil {
		fmt.Fprintf(&b, "拒绝（%s）：%s\n", r.Rejected.Check, r.Rejected.Reason)
	} else {
		fmt.Fprintf(&b, "查过的 %d 项都通过\n", len(allChecks)-len(r.Unchecked))
	}
	for _, u := range r.Unchecked {
		fmt.Fprintf(&b, "  ⚠️ %s：**没能查** —— 缺 %s\n", u.Check, u.Missing)
	}
	if len(r.Unchecked) > 0 {
		b.WriteString("  ⚠️ 以上各项没查成，**不要把「没有拒绝」读成「这笔单没问题」**\n")
	}
	return b.String()
}

// Validate 跑八项校验。
//
// ⚠️ 它**跑完全部八项**再返回，不在第一个拒绝处短路。
// 短路会让「还有几项没查成」这件事取决于第一个拒绝出现在第几位 ——
// 而那意味着同一笔单在不同的输入下会报出不同数量的「没能查」，
// 使用者无从判断自己到底覆盖了多少。
// 拒因仍然只报**优先级最高的那一个**，与柜台一致。
func Validate(req Request, f Facts) Result {
	var res Result
	var rejections []Rejection
	add := func(c Check, format string, a ...any) {
		rejections = append(rejections, Rejection{c, fmt.Sprintf(format, a...)})
	}
	skip := func(c Check, missing string) {
		res.Unchecked = append(res.Unchecked, Unchecked{c, missing})
	}

	if req.Volume <= 0 {
		// ⚠️ 这一条不属于八项，它是**输入本身不合法**。
		// 混进手数上下限那一项会让「本库拒了」与「交易所会拒」分不开。
		return Result{Rejected: &Rejection{CheckVolumeRange,
			fmt.Sprintf("报单手数必须为正，得到 %d —— 这不是交易所的限制，是输入不合法", req.Volume)}}
	}

	// —— 1 合约可交易 ——
	if !f.HasInstrument {
		skip(CheckTradable, "合约规格（refdata.Instrument）")
	} else if !f.Instrument.IsTrading {
		add(CheckTradable, "合约 %s 不可交易（IsTrading=false）", req.Instrument.Canonical())
	}

	// —— 2 交易时段 ——
	if !f.HasSession {
		skip(CheckSession, "「此刻在不在交易时段内」—— 本库的 Calendar 时段之外拒答（kq_facts 23）")
	} else if !f.InSession {
		add(CheckSession, "不在交易时段内")
	}

	// —— 3 最小变动价位 ——
	switch {
	case !f.HasInstrument:
		skip(CheckPriceTick, "合约规格里的 PriceTick")
	case !f.Instrument.PriceTick.IsPositive():
		skip(CheckPriceTick, "PriceTick 非正 —— 那是规则数据缺失，不是「不用校验」")
	case !req.Price.Mod(f.Instrument.PriceTick).IsZero():
		add(CheckPriceTick, "价格 %s 不是最小变动价位 %s 的整数倍",
			req.Price, f.Instrument.PriceTick)
	}

	// —— 4 涨跌停 ——
	up, lo, ok := f.Instrument.PriceLimits(f.PreSettlement, f.HasPreSettlement, f.Rounding)
	switch {
	case !f.HasInstrument:
		skip(CheckPriceLimit, "合约规格")
	case !f.HasPreSettlement:
		skip(CheckPriceLimit, "昨结算价")
	case f.Rounding == refdata.TickRoundingUnknown:
		skip(CheckPriceLimit, "取整方向 —— ⚠️ 两家交易所不同（上期所向下、大商所四舍五入），挑一个会静默错")
	case !ok:
		skip(CheckPriceLimit, "涨跌幅比例（规则数据里没有）")
	case req.Price.GreaterThan(up):
		add(CheckPriceLimit, "价格 %s 高于涨停价 %s", req.Price, up)
	case req.Price.LessThan(lo):
		add(CheckPriceLimit, "价格 %s 低于跌停价 %s", req.Price, lo)
	}

	// —— 5 手数上下限 ——
	switch {
	case !f.HasInstrument:
		skip(CheckVolumeRange, "合约规格里的手数上下限")
	case f.Instrument.MaxLimitOrderVolume <= 0:
		skip(CheckVolumeRange, "手数上限（免费行情不下发，字典也没有）")
	case req.Volume > f.Instrument.MaxLimitOrderVolume:
		add(CheckVolumeRange, "手数 %d 超过上限 %d", req.Volume, f.Instrument.MaxLimitOrderVolume)
	case f.Instrument.MinLimitOrderVolume > 0 && req.Volume < f.Instrument.MinLimitOrderVolume:
		add(CheckVolumeRange, "手数 %d 低于下限 %d", req.Volume, f.Instrument.MinLimitOrderVolume)
	}

	// —— 6 可平量（⚠️ 今昨**分别**校验）——
	if req.Offset.IsClose() {
		if f.Position == nil {
			skip(CheckClosable, "持仓（nil 表示**不知道**，不是「空仓」）")
		} else if r := checkClosable(req, f.Position); r != nil {
			rejections = append(rejections, *r)
		}
	}

	// —— 7 可用资金 ——
	switch {
	case !f.HasAvailable:
		skip(CheckFunds, "可用资金")
	case !f.HasNeed:
		skip(CheckFunds, "这笔单要占用的保证金与手续费（由调用方算好传入）")
	case f.Need.GreaterThan(f.Available):
		add(CheckFunds, "要占用 %s，而可用资金只有 %s", f.Need, f.Available)
	}

	// —— 8 限仓 ——
	switch {
	case !f.HasPositionLimit:
		skip(CheckPositionLimit, "交易所限仓额度")
	case f.Position == nil:
		skip(CheckPositionLimit, "持仓（算不出报单后的总量）")
	case req.Offset == types.Open:
		if after := openVolumeAfter(req, f.Position); after > f.PositionLimit {
			add(CheckPositionLimit, "开仓后该方向共 %d 手，超过限仓 %d",
				after, f.PositionLimit)
		}
	}

	// ⚠️ 拒因只报优先级最高的那一个，与柜台一致（柜台只回一个 last_msg）。
	sort.SliceStable(rejections, func(i, j int) bool {
		return rejections[i].Check < rejections[j].Check
	})
	if len(rejections) > 0 {
		res.Rejected = &rejections[0]
	}
	sort.SliceStable(res.Unchecked, func(i, j int) bool {
		return res.Unchecked[i].Check < res.Unchecked[j].Check
	})
	return res
}

// checkClosable 按**今昨分别**校验可平量。
//
// ⚠️ 合并校验会放过「昨仓 3 手、今仓 5 手时报平昨 4 手」，
// 然后在结算时算出一个不存在的持仓。参照仓库在这里栽过：
// 超量平仓被当成反手，10 张平 4 张多头得到 6 张多头，全程不报错。
// 中国期货不允许反手，所以这里只有一个正确答案：**拒绝**。
func checkClosable(req Request, p *position.Position) *Rejection {
	// 平仓的持仓方向与下单方向**相反**。
	dir := types.Sell
	if req.Direction == types.Sell {
		dir = types.Buy
	}
	s, err := p.Side(dir)
	if err != nil {
		return &Rejection{CheckClosable, err.Error()}
	}
	today, his := s.VolumeToday(), s.VolumeHistory()
	switch req.Offset {
	case types.CloseToday:
		if req.Volume > today {
			return &Rejection{CheckClosable, fmt.Sprintf(
				"平今 %d 手超过今仓 %d 手（昨仓另有 %d 手，**不可用于平今**）",
				req.Volume, today, his)}
		}
	case types.CloseYesterday:
		if req.Volume > his {
			return &Rejection{CheckClosable, fmt.Sprintf(
				"平昨 %d 手超过昨仓 %d 手（今仓另有 %d 手，**不可用于平昨**）",
				req.Volume, his, today)}
		}
	default:
		// 裸 Close：⚠️ 本库按 cn-futures-rules.md §4 **报错而不是猜**。
		// 快期模拟把它解释成平昨（kq_facts 32，两条独立证据），
		// 但那是**一个口子**的行为，simnow_pending#1 未裁决。
		// 猜错的代价是平错一边的仓，而显式声明本来就是要求。
		return &Rejection{CheckClosable,
			"收到裸 CLOSE（未声明平今还是平昨）—— " +
				"⚠️ 本库**拒绝**而不是按平昨处理：那个语义只在快期模拟上实测过" +
				"（kq_facts 32），真实柜台未裁决（simnow_pending#1）。" +
				"显式声明本来就是要求，报错的代价近乎为零，猜错的代价是平错一边的仓"}
	}
	return nil
}

// openVolumeAfter 算这笔开仓成交后该方向的总手数。
func openVolumeAfter(req Request, p *position.Position) int {
	s, err := p.Side(req.Direction)
	if err != nil {
		return req.Volume
	}
	return s.Volume() + req.Volume
}
