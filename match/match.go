// Package match 是内置撮合：一笔**通过八项校验**的限价单，按**自己的报价立刻 100% 全量成交**。
//
// # ⚠️ 这是一条裁决，不是一条被实测证实的规则
//
// 2026-09-09 使用者裁决：「不做盘口，文档要说明好，默认下单就是模拟 100% 全量成交。」
// 于是本包对一笔可接受的单只做一件事：价 = 报价，量 = 全部，时刻 = 立刻。
//
// 它与实盘的偏离有**两个维度，方向相反** —— 只记住其中一个的人，会按那一个去调整自己的判断：
//
//	价格维度      可成交的限价单在实盘上成交在**对手价**（kq_facts 43：报涨停 3321、成交 3166）；
//	              本库按自己的报价成交 ⇒ 买贵了、卖便宜了 ⇒ **保守**
//	成交与否维度  本库假定立刻全量成交；而挂在远离市场的限价单实盘上**可能永远不成交**
//	              ⇒ **乐观** —— 这是危险的那一头：回测会看到它实际拿不到的成交，
//	              而账面数字全程合理、不报错（silent-risks.md 第 9 条）
//
// ⚠️ 这两段话**在任何对拍上都不会红**：夹具不留盘口，成交价逻辑写成什么样都不会被观测否掉。
// 所以它们写在导出面上，并由 TestPackageDocStatesBothDeviations 钉住不许删。
//
// # 查不了的校验不当成通过
//
// [Fill] 自己调 [order.Validate]，并且要求八项**全部查过**（[order.Result.OK]）：
// 一笔因为缺可用资金而没查成的开仓，若照样成交，就会开出实际开不出的仓（silent-risks.md 第 7 条）。
//
// # 不做的
//
// 不改持仓与资金（[Trade] 是成交记录，应用它是门面的事）；不撮合市价单（[order.Request] 只有限价）；
// 不做部分成交与 FAK/FOK；不判成交角色（本库的手续费结构里没有这一维）。
// 每一项的理由见 docs/design.md「match 的形状」。
package match

import (
	"fmt"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/ctperr"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Trade 是一笔成交。
//
// ⚠️ Price 是**报价**，不是实盘会成交的价；Volume 是**全部**报单手数，不是实盘会成交的量。
// 两者的偏离方向相反，见包文档。
type Trade struct {
	Instrument types.InstrumentID
	Direction  types.Direction
	Offset     types.Offset
	Hedge      types.HedgeFlag
	Price      decimal.Decimal
	Volume     int
}

// RejectedError 是「这笔单被拒了，所以没有成交」。
type RejectedError struct {
	Rejection order.Rejection
	// Exchange 是报单所在的交易所 —— 查码要它：同一拒因的码随交易所变。
	Exchange types.Exchange
}

// Code 是柜台对这次拒绝会给的码（ctperr 实测表）。第二个返回值为 false 表示**没有观测**：
// 拒因不在语料粒度内（Rejection.Kind 为 ReasonUnknown），或这个交易所没拍过 —— 调用方不许拿零值当码。
func (e *RejectedError) Code() (ctperr.Code, bool) {
	return ctperr.Lookup(e.Exchange, e.Rejection.Kind)
}

func (e *RejectedError) Error() string {
	return fmt.Sprintf("不成交：报单被拒（%s）：%s", e.Rejection.Check, e.Rejection.Reason)
}

// UncheckedError 是「这笔单有校验没查成，所以不成交」。
//
// ⚠️ 它与 RejectedError 分成两个类型，因为调用方该做的事相反：
// 被拒要改单；没查成要**补事实**（取行情、查资金……），单本身可能没问题。
type UncheckedError struct {
	Unchecked []order.Unchecked
}

func (e *UncheckedError) Error() string {
	parts := make([]string, len(e.Unchecked))
	for i, u := range e.Unchecked {
		parts[i] = fmt.Sprintf("%s（缺 %s）", u.Check, u.Missing)
	}
	return fmt.Sprintf("不成交：有 %d 项校验**没能查**，不当成通过 —— %s",
		len(e.Unchecked), strings.Join(parts, "；"))
}

// Fill 撮合一笔限价单：跑八项校验，全部查过且没被拒 ⇒ 按报价立刻全量成交。
//
// ⚠️ 入参是 facts 而不是一份现成的 order.Result：Result 与它对应的是哪一笔 Request，
// 在类型上没有任何绑定 —— 拿 A 单的「通过」去撮合 B 单，编译器与测试都不会响。
// 本函数自己校验，于是「查的那一笔」与「成交的那一笔」必然是同一笔。
//
// ⚠️ 同一个理由延伸到合约：order.Validate **不核对** req.Instrument 与 facts 里的合约规格、
// 持仓是不是同一个合约（它按 facts 查、按 req 报）。本函数在校验之前先核对 ——
// 否则一笔 rb2701 的单会拿着 ag2702 的最小变动价位与涨跌停通过校验，然后以 rb2701 成交。
func Fill(req order.Request, facts order.Facts) (Trade, error) {
	if err := sameInstrument(req, facts); err != nil {
		return Trade{}, err
	}
	res := order.Validate(req, facts)
	if res.Rejected != nil {
		return Trade{}, &RejectedError{Rejection: *res.Rejected, Exchange: req.Instrument.Exchange}
	}
	if !res.FullyChecked() {
		return Trade{}, &UncheckedError{Unchecked: res.Unchecked}
	}
	// ⚠️ 下面这一行就是整条裁决：价 = 报价、量 = 全部。
	// 它**不可验证**（没有盘口），而它的两个偏离方向写在包文档里。
	return Trade{
		Instrument: req.Instrument,
		Direction:  req.Direction,
		Offset:     req.Offset,
		Hedge:      req.Hedge,
		Price:      req.Price,
		Volume:     req.Volume,
	}, nil
}

// sameInstrument 核对报单、合约规格、持仓三处的合约是同一个。
func sameInstrument(req order.Request, f order.Facts) error {
	if f.HasInstrument && f.Instrument.ID != req.Instrument {
		return fmt.Errorf("不成交：报单是 %s，而校验用的合约规格是 %s —— "+
			"会拿别的合约的最小变动价位与涨跌停去查这一笔", req.Instrument, f.Instrument.ID)
	}
	if f.Position != nil && f.Position.Instrument != req.Instrument {
		return fmt.Errorf("不成交：报单是 %s，而校验用的持仓是 %s —— "+
			"可平量与限仓会按别的合约的持仓去查", req.Instrument, f.Position.Instrument)
	}
	return nil
}
