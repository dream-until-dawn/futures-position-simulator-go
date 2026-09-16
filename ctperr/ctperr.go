// Package ctperr 是与 CTP 柜台对齐的**报单拒绝码**：交易所 × 拒因 → 码（带码空间）。
//
// ⚠️ **来源范围很窄**：整张表只来自 **SimNow 一个柜台、交易日 20260915 一个交易日**。
// 真实期货公司的前置给不给同样的码，**没有观测** —— 用这个包的人手边没有语料，所以写在这里。
//
// # ⚠️ 每一个码都来自探针语料，一个都不是填的
//
// roadmap 的约束是「`ctperr` 的取值一个都不许先填」。本包的表只收 `oracle ctp-reject -out`
// 拍下来的观测（testdata/refdata/ctp-reject-codes.json），并由 TestTableMatchesCorpus **双向**核对：
// 表里每一格语料里都有，语料里每一条单一违反的拒单表里都有。手打进表的一格会让它红。
//
// # 一个码有三个坐标
//
//	交易所   同一情形的码随交易所变：平昨超过昨仓，大商所 30、上期所与能源中心 51
//	拒因     本包自己的枚举，只到语料测到的粒度（没有平今）
//	码空间   RspInfo.ErrorID 与 StatusMsg 前缀是两套数：CTP 50 = 平今仓位不足，前缀 50 = 价格跌破跌停板
//	         ⚠️「CTP 50 = 平今仓位不足」出自 state.md 20260910 的记载，**未进语料，本表不收**；它在这里只用来说明撞号
//
// ⚠️ [SpaceStatusPrefix] **不叫「交易所码」**：价格类检查可能在柜台前置就拒了、没进交易所，
// 而语料目前分不开这两层（cn-futures-rules.md §13 #6）。名字只说码写在哪，不说码出自哪一层。
//
// # 查不到就说查不到
//
// [Lookup] 对没测过的组合返回 false，**不回落**到「最常见的那个码」—— 郑商所、广期所、中金所，
// 以及任何交易所的平今超量，此刻都没有观测。
package ctperr

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// Space 是码写在柜台应答的哪一处。
type Space uint8

const (
	// SpaceUnknown 是零值：谁也不匹配。
	SpaceUnknown Space = iota
	// SpaceCTP 是 RspInfo.ErrorID（柜台错误码）。
	SpaceCTP
	// SpaceStatusPrefix 是委托回报 StatusMsg 文本前缀 `NN:` 里的整数，这时 RspInfo.ErrorID 为 0。
	//
	// ⚠️ 它出自柜台前置还是交易所，语料目前分不开，见包文档。
	SpaceStatusPrefix
)

func (s Space) String() string {
	switch s {
	case SpaceCTP:
		return "CTP 错误码"
	case SpaceStatusPrefix:
		return "状态文本前缀码"
	}
	return "未知码空间"
}

// Code 是一个拒绝码。⚠️ 比较两个码必须连 Space 一起比：只比 Value 会把撞号的两个意思并成一个。
type Code struct {
	Space Space
	Value int
}

func (c Code) String() string { return fmt.Sprintf("%s %d", c.Space, c.Value) }

// Reason 是本包认得的拒因。⚠️ 只到语料测到的粒度。
type Reason uint8

const (
	// ReasonUnknown 是零值。
	ReasonUnknown Reason = iota
	// ReasonPriceTick 价格不是最小变动价位的整数倍。
	ReasonPriceTick
	// ReasonAboveUpperLimit 价格高于涨停板。
	ReasonAboveUpperLimit
	// ReasonBelowLowerLimit 价格低于跌停板。
	ReasonBelowLowerLimit
	// ReasonCloseYesterdayExceeds 平昨手数超过昨仓。
	//
	// ⚠️ 语料里是「账上无仓时平昨」。平今超量、有仓但不够的码**没有观测**，不从这一条推。
	ReasonCloseYesterdayExceeds
	// ReasonOutsideSession 不在交易时段内。
	//
	// ⚠️ 它是 20260916 才有观测的：对应 `order.CheckSession`，而那一项此前在**两个口子上都测不出来** ——
	// 本库标「查不了」，快期那侧根本不查（kq_facts 48）。盘中休息里一笔**除时段外完全合法**的委托被拒，
	// 才把这个码逼出来（`oracle ctp-priority` 的对照组）。
	//
	// ⚠️ 语料里是「盘中休息（10:15–10:30 / 11:30–13:30）」。收盘之后、节假日的码**没有观测**，不从这一条推。
	ReasonOutsideSession
)

func (r Reason) String() string {
	switch r {
	case ReasonPriceTick:
		return "价格非最小变动价位整数倍"
	case ReasonAboveUpperLimit:
		return "价格高于涨停"
	case ReasonBelowLowerLimit:
		return "价格低于跌停"
	case ReasonCloseYesterdayExceeds:
		return "平昨超过昨仓"
	case ReasonOutsideSession:
		return "不在交易时段内"
	}
	return "未知拒因"
}

type key struct {
	exchange types.Exchange
	reason   Reason
}

// measured 是实测的表。⚠️ 改这里之前先去拍语料：TestTableMatchesCorpus 会双向核对。
//
// 出处：oracle ctp-reject -out，SimNow。交易日 20260915（DCE / SHFE / INE）与
// 20260916 日盘（SHFE 重拍 / CZCE / GFEX，每一轮都带对照组与柜台报的 tick）；
// 「不在交易时段内」那五格出自 `oracle ctp-priority` 的对照组（20260916 15:05，收盘之后，五所）。
//
// ⚠️ **两个码空间，撞号**：CTP 的 `50 平今仓位不足` 与交易所的 `50 价格跌破跌停板`
// 意思完全不同 —— 所以 Code 带 Space 这一维。
//
// ⚠️ **五所到齐之后的形状，与「各交易所各有一套」相反**：
//
//	价格类三条  48 / 49 / 50  **五所完全同号**（交易所空间，ErrorID == 0）
//	可平量      51（SHFE / INE）vs 30（DCE / CZCE / GFEX）  二比三，CTP 空间
//
// ⚠️ 20260914 那一版记的是「INE 照抄上期所，**大商所自成一套**」——
// 补拍郑商所与广期所之后那句作废：大商所在**多数**那一侧。
// **两个样本上的「一样」与「不一样」，都还不是一个分组。**
var measured = map[key]Code{
	{types.DCE, ReasonPriceTick}:             {SpaceStatusPrefix, 48},
	{types.DCE, ReasonAboveUpperLimit}:       {SpaceStatusPrefix, 49},
	{types.DCE, ReasonBelowLowerLimit}:       {SpaceStatusPrefix, 50},
	{types.DCE, ReasonCloseYesterdayExceeds}: {SpaceCTP, 30},

	{types.SHFE, ReasonPriceTick}:             {SpaceStatusPrefix, 48},
	{types.SHFE, ReasonAboveUpperLimit}:       {SpaceStatusPrefix, 49},
	{types.SHFE, ReasonBelowLowerLimit}:       {SpaceStatusPrefix, 50},
	{types.SHFE, ReasonCloseYesterdayExceeds}: {SpaceCTP, 51},

	{types.INE, ReasonPriceTick}:             {SpaceStatusPrefix, 48},
	{types.INE, ReasonAboveUpperLimit}:       {SpaceStatusPrefix, 49},
	{types.INE, ReasonBelowLowerLimit}:       {SpaceStatusPrefix, 50},
	{types.INE, ReasonCloseYesterdayExceeds}: {SpaceCTP, 51},

	{types.CZCE, ReasonPriceTick}:             {SpaceStatusPrefix, 48},
	{types.CZCE, ReasonAboveUpperLimit}:       {SpaceStatusPrefix, 49},
	{types.CZCE, ReasonBelowLowerLimit}:       {SpaceStatusPrefix, 50},
	{types.CZCE, ReasonCloseYesterdayExceeds}: {SpaceCTP, 30},

	{types.GFEX, ReasonPriceTick}:             {SpaceStatusPrefix, 48},
	{types.GFEX, ReasonAboveUpperLimit}:       {SpaceStatusPrefix, 49},
	{types.GFEX, ReasonBelowLowerLimit}:       {SpaceStatusPrefix, 50},
	{types.GFEX, ReasonCloseYesterdayExceeds}: {SpaceCTP, 30},

	// ⚠️ 「不在交易时段内」五所同号，且**盘中休息与收盘之后给的是同一个码**
	// （11:32 与 15:05 两轮，语料里 at 那一栏分得开）。
	// ⚠️ 节假日、以及「合约已到期」的码仍然**没有观测** —— 不从这五格往那边推。
	{types.SHFE, ReasonOutsideSession}: {SpaceStatusPrefix, 26},
	{types.INE, ReasonOutsideSession}:  {SpaceStatusPrefix, 26},
	{types.DCE, ReasonOutsideSession}:  {SpaceStatusPrefix, 26},
	{types.CZCE, ReasonOutsideSession}: {SpaceStatusPrefix, 26},
	{types.GFEX, ReasonOutsideSession}: {SpaceStatusPrefix, 26},
}

// Lookup 查某交易所某拒因的码。第二个返回值为 false 表示**没有观测**，调用方不许拿零值当码用。
func Lookup(ex types.Exchange, r Reason) (Code, bool) {
	c, ok := measured[key{ex, r}]
	return c, ok
}

// Error 是一次带码的报单拒绝。
type Error struct {
	Exchange types.Exchange
	Reason   Reason
	Code     Code
}

func (e *Error) Error() string {
	return fmt.Sprintf("报单被拒（%s，%s）：%s", e.Exchange, e.Reason, e.Code)
}

// New 按实测表造一个 *Error；没有观测时返回 false，**不造一个零码的 Error**。
func New(ex types.Exchange, r Reason) (*Error, bool) {
	c, ok := Lookup(ex, r)
	if !ok {
		return nil, false
	}
	return &Error{Exchange: ex, Reason: r, Code: c}, true
}
