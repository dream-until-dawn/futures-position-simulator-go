package fixture

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Replay 把一个合约的成交重放成本库的持仓。
//
// ⚠️ 重放有一个**未知输入**：平仓从哪一笔明细消耗。
// 那是 cn-futures-rules.md §13 的第 4 条，至今未实测。
// 所以本函数不挑一个顺序偷偷用，而是**三种候选各跑一遍**：
//
//	三者结果相同  重放无歧义 —— 这次样本对消耗顺序不敏感，可以拿去对拍
//	三者结果不同  ⚠️ 重放**有歧义**，报错。此时任何一个结果都是猜的，
//	              而猜出来的持仓会与柜台比出一堆看起来像真差异的差异
//
// 换句话说：把一个待实测项当成已知，代价不是「可能错」，
// 是「错了之后所有别的字段的对拍结论都不可信」。
//
// ⚠️ 反过来说，三者相同**不等于**消耗顺序不重要，只等于**这次样本分不开它们**。
// 那正是实验 4 要造的样本：让三者分开。
func Replay(inst types.InstrumentID, hedge types.HedgeFlag, dateType refdata.PositionDateType,
	day types.TradingDay, trades []Trade) (*position.Position, error) {
	return ReplayFrom(nil, inst, hedge, dateType, day, trades)
}

// ReplayFrom 在一个**已有持仓**上重放当日成交。
//
// ⚠️ 它不是 Replay 的可选增强，是**跨交易日对拍的必需品**，理由很具体：
//
//	柜台的成交截面按交易日重置。今晚的持仓截面里会有昨仓，
//	而今晚的成交里**没有任何一笔能解释它** —— 那几手是昨天开的。
//
// 也就是说 Replay(空仓, 今日成交) 会漏掉全部昨仓，
// 而漏掉之后重放出来的持仓仍然是一个**看起来完全正常**的持仓，
// 只是手数少了几手。它会与柜台比出一堆看起来像真差异的差异。
//
// 正确的链条是：上一日的夹具 → 按当日结算价结算 → 得到昨仓 → 在其上重放今日成交。
// start 就是那个「结算之后的昨仓」，由调用方按这条链子备好。
//
// ⚠️ 另记一条：Replay 的「三种消耗顺序一致」检查在 start 为 nil 时
// **结构上不可能触发** —— 全是今仓，三种顺序消耗的是同一批。
// 它只有在这里、start 带着昨仓时才真正开始工作。
func ReplayFrom(start *position.Position, inst types.InstrumentID, hedge types.HedgeFlag, dateType refdata.PositionDateType,
	day types.TradingDay, trades []Trade) (*position.Position, error) {

	orders := []position.CloseOrder{position.YesterdayFirst, position.TodayFirst, position.FIFO}
	var first *position.Position
	var firstSig string
	for i, ord := range orders {
		p, err := replayWith(start, inst, hedge, dateType, day, trades, ord, nil)
		if err != nil {
			return nil, fmt.Errorf("按「%v」重放失败：%w", ord, err)
		}
		sig := signature(p)
		if i == 0 {
			first, firstSig = p, sig
			continue
		}
		if sig != firstSig {
			return nil, fmt.Errorf("⚠️ 重放有歧义：按「%v」得到 %s，按「%v」得到 %s —— "+
				"平仓消耗顺序是待实测第 4 条，本次样本把它们分开了。"+
				"此时任何一个结果都是猜的，不拿去对拍",
				orders[0], firstSig, ord, sig)
		}
	}
	return first, nil
}

// Realized 是一次平仓的已实现结果。
//
// ⚠️ 它必须带着**被消耗的明细片段**，而不只是手数与均价：
// 两套平仓盈亏口径都要逐片算 —— 逐笔对冲用片的 OpenPrice，
// 逐日盯市用片的 Basis。压成均价之后就只剩一套了。
type Realized struct {
	TradeID    string
	Instrument types.InstrumentID
	// Direction 是**被平掉的持仓**的方向，不是下单方向。
	Direction  types.Direction
	Offset     types.Offset
	ClosePrice decimal.Decimal
	Consumed   []position.Lot
}

// ReplayRealized 在重放的同时收集全部平仓的已实现片段。
//
// ⚠️ 它固定用 position.YesterdayFirst，**而这在本批样本上无所谓**：
// ReplayFrom 已经断言过三种消耗顺序给出同一个结果，
// 顺序有分歧时它会报错而不是返回一个猜的。
// 这里再挑一次顺序不是第二个判断，是复用那个已经被检查过的结论。
func ReplayRealized(start *position.Position, inst types.InstrumentID, hedge types.HedgeFlag, dateType refdata.PositionDateType,
	day types.TradingDay, trades []Trade) (*position.Position, []Realized, error) {
	// 先走一遍歧义检查 —— 有歧义就整个不给结果。
	if _, err := ReplayFrom(start, inst, hedge, dateType, day, trades); err != nil {
		return nil, nil, err
	}
	var out []Realized
	p, err := replayWith(start, inst, hedge, dateType, day, trades, position.YesterdayFirst, &out)
	if err != nil {
		return nil, nil, err
	}
	return p, out, nil
}

func replayWith(start *position.Position, inst types.InstrumentID, hedge types.HedgeFlag, dateType refdata.PositionDateType,
	day types.TradingDay, trades []Trade, ord position.CloseOrder,
	realized *[]Realized) (*position.Position, error) {

	p, err := position.New(inst, hedge, day, dateType)
	if err != nil {
		return nil, err
	}
	if start != nil {
		// ⚠️ 必须**逐笔**拷贝起始持仓，不能只搬手数与均价：
		// 均价是有损压缩，而三种消耗顺序的分别恰恰只在明细上显形。
		// 从均价重建的起始持仓，会让下面的歧义检查永远判「一致」。
		if err := copyLots(p, start); err != nil {
			return nil, err
		}
	}
	for _, t := range trades {
		switch {
		case t.Offset == types.Open:
			if err := p.Open(t.Direction, day, t.Price, t.Volume); err != nil {
				return nil, fmt.Errorf("成交 %s：%w", t.TradeID, err)
			}
		case t.Offset.IsClose() || t.Offset == types.Close:
			// ⚠️ 平仓成交的 direction 是**下单方向**，不是被平持仓的方向：
			// SELL/CLOSETODAY 平的是**多头**。反过来用会把多头平成空头，
			// 而在双向持仓的样本上它不会报错 —— 两边都有仓可平。
			closing := opposite(t.Direction)
			res, err := p.Close(closing, closeOffsetOf(t.Offset, dateType), day, t.Volume, ord)
			if err != nil {
				return nil, fmt.Errorf("成交 %s（%v/%v %d 手）：%w",
					t.TradeID, t.Direction, t.Offset, t.Volume, err)
			}
			if realized != nil {
				*realized = append(*realized, Realized{
					TradeID: t.TradeID, Instrument: inst, Direction: closing,
					Offset: t.Offset, ClosePrice: t.Price, Consumed: res.Consumed,
				})
			}
		default:
			return nil, fmt.Errorf("成交 %s 的开平标志 %v 不认识", t.TradeID, t.Offset)
		}
	}
	return p, nil
}

func opposite(d types.Direction) types.Direction {
	if d == types.Buy {
		return types.Sell
	}
	return types.Buy
}

// signature 是持仓的可比较摘要：两个方向的逐笔明细。
//
// ⚠️ 摘要必须含**逐笔**而不只是均价：三种消耗顺序完全可能给出同一个均价
// 而留下不同的明细，而明细的差别会在下一次平仓或结算时才显形。
// 用均价做摘要，等于把「暂时看不出来」当成「相同」。
func signature(p *position.Position) string {
	s := ""
	for _, d := range []types.Direction{types.Buy, types.Sell} {
		side, err := p.Side(d)
		if err != nil {
			return "错误:" + err.Error()
		}
		s += fmt.Sprintf("|%v:", d)
		for _, l := range side.Lots() {
			s += fmt.Sprintf("(%s×%d,基线%s,昨仓%t)", l.OpenPrice, l.Volume, l.Basis, l.Settled)
		}
	}
	return s
}

// copyLots 把起始持仓的逐笔明细拷进目标持仓。
//
// ⚠️ 用 Side.Append 而不是重新 Open：Open 会把这一笔当成**今仓**
// （Basis = 开仓价、Settled = false），而起始持仓里的昨仓恰恰
// Basis = 昨结算价、Settled = true。走 Open 会把昨仓悄悄变成今仓，
// 而变完之后平今平昨的判定、手续费、保证金基线全部错位且不报错
// —— 那是 silent-risks.md 的第 1 条。
func copyLots(dst, src *position.Position) error {
	for _, d := range []types.Direction{types.Buy, types.Sell} {
		from, err := src.Side(d)
		if err != nil {
			return err
		}
		to, err := dst.Side(d)
		if err != nil {
			return err
		}
		for _, l := range from.Lots() {
			to.Append(l)
		}
	}
	return nil
}

// closeOffsetOf 把柜台成交里的开平标志翻成**本库的**平仓语义。
//
// # ⚠️ 这不是「宽松处理」，是把一条实测结论用上
//
// 柜台的裸 `CLOSE` 在 `UseHistory` 合约上**就是平昨**，两条独立证据：
//
//	冻结字段  20260909 在 今1/昨3 的同一截面上，CLOSE 冻的是
//	          volume_long_frozen_his（CLOSETODAY 冻 _today）
//	持仓截面  同日那笔平昨成交前后：今1/昨3 → **今1/昨2**。
//	          若吃的是今仓会变成 今0/昨3
//
// （另有 2026-09-07 的拒因原话「平昨手数超过昨仓持仓量」，见 cn-futures-rules.md）
//
// 不翻的话，重放会把 `types.Close` 交给 `CloseOrder` 去挑一边，
// 于是「先平昨」与「先平今」给出不同结果、被判成**歧义**而整份样本作废 ——
// 而柜台那一侧根本没有歧义，它是确定的。
// ⚠️ 把一个**已经量出来**的确定行为当成未知，代价是丢掉整份样本。
//
// # ⚠️ 这与「本库拒收裸 CLOSE」不矛盾
//
// 那条讲的是**报单语义**：用户发一笔裸 `CLOSE` 时本库报错，
// 因为证据来自快期模拟、simnow_pending#1 未裁决（cn-futures-rules.md）。
// 这里讲的是**重放柜台已经成交的记录**：那笔单柜台已经按平昨执行了，
// 要复现它就得照它执行。两件事一个是「该不该接受」，一个是「发生了什么」。
//
// ⚠️ `PositionDateUnknown` 时**不翻**：不知道合约属于哪一型就不猜，
// 让歧义检查去拦。翻错的后果是平错一边，而那不会报错。
func closeOffsetOf(offset types.Offset, dateType refdata.PositionDateType) types.Offset {
	if offset == types.Close && dateType == refdata.UseHistory {
		return types.CloseYesterday
	}
	return offset
}
