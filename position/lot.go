// Package position 是持仓状态机：今昨仓、逐笔明细、开平校验。
//
// # 逐笔明细是一等公民
//
// 中国期货有两套盈亏口径同时存在（cn-futures-rules.md §5）：
//
//	逐日盯市 ByDate    昨仓以昨结算价为基线，今仓以开仓价为基线，每日结算重置
//	逐笔对冲 ByTrade   以原始成交价为基线，跨结算日不重置
//
// 后者要求知道「这一手是哪一笔开的」，而**均价是有损压缩**：
//
//	(2 手 @100, 1 手 @130) 与 (3 手 @110) 均价相同
//	平掉 1 手 @120 时：逐笔对冲 = +20，按均价 = +10
//	⚠️ 两个结果都不会报错
//
// 所以本包存明细，**均价是从明细推出来的视图，不是存储形态**。
// 这是 docs/design.md 的决策 10，必须第一天定——事后补明细等于把历史丢了。
//
// # 忘了结算必须报错
//
// Position 记着自己所处的交易日。在交易日 D 上操作一个还停在 D-1 的持仓，
// **直接报错**，不静默继续。
//
// ⚠️ 「今昨仓不滚动」是 docs/silent-risks.md 的第 1 条：账永远是平的，
// 只是平今费率一直按平昨收、保证金基线停在开仓日，**全程没有任何动静**。
// 把它变成一个 error，是这条守卫存在的全部意义。
package position

import (
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Lot 是一笔开仓明细。
//
// ⚠️ 它同时携带两条基线，因为两套盈亏口径同时存在：
//
//	OpenPrice  原始成交价 —— 逐笔对冲的基线，**永不改变**
//	Basis      逐日盯市的基线 —— 今仓等于开仓价，昨仓等于上一交易日结算价
//
// 结算只推进 Basis，绝不碰 OpenPrice。把两者合并成一个字段，
// 就再也算不回逐笔对冲口径了。
type Lot struct {
	OpenDay   types.TradingDay // 开仓交易日
	OpenPrice decimal.Decimal  // 原始成交价，逐笔对冲基线，永不改变
	Volume    int              // 剩余手数（部分平仓后递减）
	Basis     decimal.Decimal  // 逐日盯市基线
	Settled   bool             // 是否经历过至少一次结算，即是否为昨仓
}

// IsHistory 报告这笔是不是昨仓。
//
// ⚠️ 判据是「有没有经历过结算」，**不是「开仓日是不是今天」**。
// 夜盘 21:00 之后属于下一个交易日，但中间**没有结算**——
// 周五夜盘开的仓，到周一日盘仍然是**今仓**。用开仓日推会在每个夜盘上错一次。
func (l Lot) IsHistory() bool { return l.Settled }

// Side 是一个方向上的全部明细，按开仓先后排列。
type Side struct {
	lots []Lot
}

// Lots 返回明细的副本。
func (s *Side) Lots() []Lot {
	out := make([]Lot, len(s.lots))
	copy(out, s.lots)
	return out
}

// Volume 返回该方向的总手数。
func (s *Side) Volume() int { return s.volumeWhere(func(Lot) bool { return true }) }

// VolumeToday 返回今仓手数。
func (s *Side) VolumeToday() int { return s.volumeWhere(func(l Lot) bool { return !l.Settled }) }

// VolumeHistory 返回昨仓手数。
func (s *Side) VolumeHistory() int { return s.volumeWhere(func(l Lot) bool { return l.Settled }) }

func (s *Side) volumeWhere(pred func(Lot) bool) int {
	n := 0
	for _, l := range s.lots {
		if pred(l) {
			n += l.Volume
		}
	}
	return n
}

// AvgOpenPrice 返回开仓均价。
//
// ⚠️ 它是**从明细推出来的视图**，不是存储形态。见包文档。
// 无持仓时返回零值与 false —— 调用方必须区分「均价是零」与「没有持仓」。
func (s *Side) AvgOpenPrice() (decimal.Decimal, bool) {
	return s.weightedAvg(func(l Lot) decimal.Decimal { return l.OpenPrice })
}

// AvgBasis 返回逐日盯市基线的加权均价（今仓用开仓价、昨仓用昨结算价）。
func (s *Side) AvgBasis() (decimal.Decimal, bool) {
	return s.weightedAvg(func(l Lot) decimal.Decimal { return l.Basis })
}

func (s *Side) weightedAvg(pick func(Lot) decimal.Decimal) (decimal.Decimal, bool) {
	total := 0
	sum := decimal.Zero
	for _, l := range s.lots {
		total += l.Volume
		sum = sum.Add(pick(l).Mul(decimal.NewFromInt(int64(l.Volume))))
	}
	if total == 0 {
		return decimal.Zero, false
	}
	return sum.Div(decimal.NewFromInt(int64(total))), true
}

// SettleAll 把该方向的全部明细推进到新的结算基线，**并标成昨仓**。
//
// ⚠️ 它是**唯一**把「今」变成「昨」的地方。
// 漏掉或重复执行，后续每一天的平今/平昨判定、手续费、保证金基线全部错位，
// 而且不会报错。
//
// ⚠️ 只有 `UseHistory` 的合约走这里。`NoUseHistory` 走 RebaseAll ——
// 那类合约的持仓**不变成昨仓**，实测见 state.md 的 kq_facts 24。
// 由 Position.Settle 按合约的 PositionDateType 分派，此处不判。
func (s *Side) SettleAll(settlementPrice decimal.Decimal) {
	for i := range s.lots {
		s.lots[i].Basis = settlementPrice
		s.lots[i].Settled = true
	}
}

// RebaseAll 只把基线推进到结算价，**不**把明细标成昨仓。
//
// 这是 `NoUseHistory` 合约（DCE / CZCE）的结算形态：
// 那类合约不区分今昨，持仓永远记在今仓上（kq_facts 24 实测），
// 但**逐日盯市照样发生** —— 结算是资金层面的事，与今昨划分是两回事。
//
// ⚠️ **声明的盲区**：「NoUseHistory 上基线到底推没推进」在现有样本上
// **分不开**。唯一的 NoUseHistory 样本 `DCE.m2701` 的开仓均价 3415
// 恰好**等于**昨结算价 3415（2 手 @3411 + 1 手 @3423），
// 于是「推进了」与「没推进」给出同一个数。
// 这里按规则实现（逐日盯市对所有合约成立），而不是按样本 ——
// 因为样本在这一点上什么都没说。要一个开仓价 ≠ 结算价的 NoUseHistory 样本。
func (s *Side) RebaseAll(settlementPrice decimal.Decimal) {
	for i := range s.lots {
		s.lots[i].Basis = settlementPrice
	}
}

// Append 追加一笔开仓明细。
func (s *Side) Append(l Lot) {
	if l.Volume <= 0 {
		return
	}
	s.lots = append(s.lots, l)
}

// dropEmpty 移除手数归零的明细。
func (s *Side) dropEmpty() {
	kept := s.lots[:0]
	for _, l := range s.lots {
		if l.Volume > 0 {
			kept = append(kept, l)
		}
	}
	s.lots = kept
}
