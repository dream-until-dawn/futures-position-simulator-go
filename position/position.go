package position

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// CloseOrder 是「平仓消耗今昨仓的顺序」。
//
// ⚠️ **它是判别实验 4 的产物，而实验 4 尚未收敛。**
// 见 docs/cn-futures-rules.md §13 与 docs/state.md 的 rules_pending。
//
// 零值是「未实测」，用它会**报错**——不是回退到某个「合理的默认」。
// 三个候选在「平满」的样本上给出同一个结果，所以一个错的默认值可以长期不被发现；
// 而它错了，平仓盈亏的基线（平昨用昨结算价 / 平今用开仓价）跟着错，
// 当日盈亏与结存链条一并受影响。
type CloseOrder uint8

const (
	// CloseOrderUnmeasured 是零值：实验 4 未收敛，使用即报错。
	CloseOrderUnmeasured CloseOrder = iota
	// YesterdayFirst 先平昨、后平今。
	YesterdayFirst
	// TodayFirst 先平今、后平昨。
	TodayFirst
	// FIFO 先开先平。
	//
	// ⚠️ 在「昨仓都比今仓早开」的样本上，它与 YesterdayFirst **给出同一个结果**。
	// 要把两者分开，需要两笔开仓时间不同的昨仓，即连续两个交易日各建一次种子。
	FIFO
)

func (c CloseOrder) String() string {
	switch c {
	case YesterdayFirst:
		return "先平昨"
	case TodayFirst:
		return "先平今"
	case FIFO:
		return "先开先平"
	}
	return "未实测"
}

// Position 是一个 (合约, 投机套保标志) 上的多空两个方向。
//
// 国内商品期货绝大多数是综合持仓：同一合约可同时持有多头与空头，
// 两边各自占用保证金。**双向持仓不会自动对冲**。
type Position struct {
	Instrument types.InstrumentID
	Hedge      types.HedgeFlag

	// Day 是本持仓当前所处的**交易日**。
	//
	// ⚠️ 在交易日 D 上操作一个还停在 D-1 的持仓会**报错**。
	// 「忘了结算」是静默风险清单第 1 条，把它变成 error 是这条守卫的全部意义。
	Day types.TradingDay

	long  Side
	short Side
}

// New 建一个空持仓。
func New(inst types.InstrumentID, hedge types.HedgeFlag, day types.TradingDay) (*Position, error) {
	if err := day.Validate(); err != nil {
		return nil, fmt.Errorf("建仓失败: %w", err)
	}
	if hedge == types.HedgeUnknown {
		// ⚠️ 投机套保标志决定保证金率，不能默认。
		return nil, fmt.Errorf("投机套保标志未指定 —— 它决定保证金率，不是标签")
	}
	return &Position{Instrument: inst, Hedge: hedge, Day: day}, nil
}

// Side 取某个方向的只读视图。
func (p *Position) Side(dir types.Direction) (*Side, error) {
	switch dir {
	case types.Buy:
		return &p.long, nil
	case types.Sell:
		return &p.short, nil
	}
	return nil, fmt.Errorf("未知买卖方向 %v", dir)
}

// checkDay 是「忘了结算」的守卫。
func (p *Position) checkDay(day types.TradingDay) error {
	if day == p.Day {
		return nil
	}
	if day.After(p.Day) {
		return fmt.Errorf("持仓停在交易日 %d，却收到交易日 %d 的操作 —— "+
			"⚠️ 本交易日尚未结算。跨交易日前必须先 Settle，"+
			"否则今仓不会滚成昨仓，而账面全程看不出异常", p.Day, day)
	}
	return fmt.Errorf("持仓已在交易日 %d，却收到更早的交易日 %d 的操作 —— 时间倒流", p.Day, day)
}

// Open 开仓。
func (p *Position) Open(dir types.Direction, day types.TradingDay, price decimal.Decimal, volume int) error {
	if err := p.checkDay(day); err != nil {
		return err
	}
	if volume <= 0 {
		return fmt.Errorf("开仓手数必须为正，得到 %d", volume)
	}
	if !price.IsPositive() {
		return fmt.Errorf("开仓价必须为正，得到 %s", price)
	}
	side, err := p.Side(dir)
	if err != nil {
		return err
	}
	side.Append(Lot{
		OpenDay:   day,
		OpenPrice: price,
		Volume:    volume,
		Basis:     price, // 今仓的逐日盯市基线就是开仓价
		Settled:   false,
	})
	return nil
}

// CloseResult 是一次平仓的结果。
type CloseResult struct {
	// Consumed 是被消耗掉的明细片段，按消耗顺序排列。
	//
	// 它带着每一片的 OpenPrice 与 Basis，因此**两套平仓盈亏口径都能从它算出来**：
	//
	//	逐笔对冲 = Σ (平仓价 − 片.OpenPrice) × 片.手数 × 乘数 × 方向
	//	逐日盯市 = Σ (平仓价 − 片.Basis)     × 片.手数 × 乘数 × 方向
	Consumed []Lot
	// VolumeToday / VolumeHistory 是本次分别从今仓与昨仓消耗掉的手数。
	VolumeToday   int
	VolumeHistory int
}

// Close 平仓。
//
// offset 决定从哪一边消耗：
//
//	CloseToday      只平今仓
//	CloseYesterday  只平昨仓
//	Close           由 order 决定顺序 —— ⚠️ 见 CloseOrder，实验 4 未收敛
//
// ⚠️ **超量平仓一律报错，绝不反手。** 中国期货不存在「平过头就变成反向仓位」这回事。
// 参照仓库在这里栽过：拿 10 张去平 4 张多头得到 6 张多头，
// 「想平仓，仓位反而变大、方向还没变」，全程不报错。
func (p *Position) Close(
	dir types.Direction, offset types.Offset, day types.TradingDay,
	volume int, order CloseOrder,
) (CloseResult, error) {
	var res CloseResult
	if err := p.checkDay(day); err != nil {
		return res, err
	}
	if volume <= 0 {
		return res, fmt.Errorf("平仓手数必须为正，得到 %d", volume)
	}
	if !offset.IsClose() {
		return res, fmt.Errorf("%v 不是平仓标志", offset)
	}
	side, err := p.Side(dir)
	if err != nil {
		return res, err
	}

	today, history := side.VolumeToday(), side.VolumeHistory()

	// ⚠️ 可平量必须**按今昨分别校验**。昨仓 3 手、今仓 5 手时，
	// 「平昨 4 手」应当被拒，即使总持仓有 8 手 —— 合并校验会放过它，
	// 然后在结算时算出一个不存在的持仓。
	switch offset {
	case types.CloseToday:
		if volume > today {
			return res, fmt.Errorf("平今 %d 手超过今仓 %d 手（昨仓另有 %d 手，不可用于平今）",
				volume, today, history)
		}
		res.Consumed = side.consume(volume, func(l Lot) bool { return !l.Settled })

	case types.CloseYesterday:
		if volume > history {
			return res, fmt.Errorf("平昨 %d 手超过昨仓 %d 手（今仓另有 %d 手，不可用于平昨）",
				volume, history, today)
		}
		res.Consumed = side.consume(volume, func(l Lot) bool { return l.Settled })

	default: // Close 与三种强平：不区分今昨，按 order 消耗
		if volume > today+history {
			return res, fmt.Errorf("平仓 %d 手超过总持仓 %d 手（今 %d / 昨 %d）",
				volume, today+history, today, history)
		}
		consumed, err := side.consumeByOrder(volume, order)
		if err != nil {
			return res, err
		}
		res.Consumed = consumed
	}

	for _, l := range res.Consumed {
		if l.Settled {
			res.VolumeHistory += l.Volume
		} else {
			res.VolumeToday += l.Volume
		}
	}
	side.dropEmpty()
	return res, nil
}

// consume 按明细顺序消耗满足 pred 的手数，返回被消耗的片段。
func (s *Side) consume(volume int, pred func(Lot) bool) []Lot {
	var out []Lot
	left := volume
	for i := range s.lots {
		if left == 0 {
			break
		}
		l := &s.lots[i]
		if !pred(*l) || l.Volume == 0 {
			continue
		}
		take := l.Volume
		if take > left {
			take = left
		}
		piece := *l
		piece.Volume = take
		out = append(out, piece)
		l.Volume -= take
		left -= take
	}
	return out
}

// consumeByOrder 按指定顺序消耗今昨仓。
func (s *Side) consumeByOrder(volume int, order CloseOrder) ([]Lot, error) {
	switch order {
	case CloseOrderUnmeasured:
		// ⚠️ 不回退到任何「合理的默认」。三个候选在平满的样本上同值，
		// 一个错的默认可以长期不被发现。
		return nil, fmt.Errorf("平仓消耗顺序未指定：判别实验 4 尚未收敛，"+
			"本库不提供默认值。见 docs/state.md 的 rules_pending。"+
			"（候选：%v / %v / %v）", YesterdayFirst, TodayFirst, FIFO)

	case YesterdayFirst:
		out := s.consume(volume, func(l Lot) bool { return l.Settled })
		return append(out, s.consume(volume-count(out), func(l Lot) bool { return !l.Settled })...), nil

	case TodayFirst:
		out := s.consume(volume, func(l Lot) bool { return !l.Settled })
		return append(out, s.consume(volume-count(out), func(l Lot) bool { return l.Settled })...), nil

	case FIFO:
		return s.consume(volume, func(Lot) bool { return true }), nil
	}
	return nil, fmt.Errorf("未知的平仓消耗顺序 %d", order)
}

func count(lots []Lot) int {
	n := 0
	for _, l := range lots {
		n += l.Volume
	}
	return n
}

// Settle 执行日终结算：把两个方向的基线推进到今结算价，并把今仓滚成昨仓。
//
// nextDay 是**下一个交易日**，由日历给出。
//
// ⚠️ 本库不推算交易日：夜盘属于下一交易日、一个交易日可横跨三个自然日、
// 长假前后的映射必须查日历。用「下一个工作日」近似会在每个长假前后错一次，
// 而那正是保证金上调、风险最高的时候。
//
// ⚠️ 结算价为零**报错**。没有任何品种的结算价会是零，`0` 只可能是缺失的伪装
// ——免费日线源上就有 7.7%～98.6% 的结算价字段是 0，见 design.md §6.5。
func (p *Position) Settle(day types.TradingDay, settlementPrice decimal.Decimal, nextDay types.TradingDay) error {
	if day != p.Day {
		return fmt.Errorf("结算的是交易日 %d，而持仓停在 %d —— 结算不能跳过交易日，也不能重复执行", day, p.Day)
	}
	if settlementPrice.IsZero() {
		return fmt.Errorf("交易日 %d 的结算价是 0 —— 没有任何品种的结算价会是零，"+
			"0 只可能是缺失的伪装；缺失请显式报告缺失", day)
	}
	if !settlementPrice.IsPositive() {
		return fmt.Errorf("交易日 %d 的结算价是 %s，必须为正", day, settlementPrice)
	}
	if !nextDay.After(day) {
		return fmt.Errorf("下一交易日 %d 不晚于当前交易日 %d", nextDay, day)
	}
	if err := nextDay.Validate(); err != nil {
		return fmt.Errorf("下一交易日不合法: %w", err)
	}
	p.long.SettleAll(settlementPrice)
	p.short.SettleAll(settlementPrice)
	p.Day = nextDay
	return nil
}

// VolumeToday 返回某方向的今仓手数。
func (p *Position) VolumeToday(dir types.Direction) int {
	s, err := p.Side(dir)
	if err != nil {
		return 0
	}
	return s.VolumeToday()
}

// VolumeHistory 返回某方向的昨仓手数。
func (p *Position) VolumeHistory(dir types.Direction) int {
	s, err := p.Side(dir)
	if err != nil {
		return 0
	}
	return s.VolumeHistory()
}

// IsFlat 报告是否已无持仓。
func (p *Position) IsFlat() bool { return p.long.Volume() == 0 && p.short.Volume() == 0 }
