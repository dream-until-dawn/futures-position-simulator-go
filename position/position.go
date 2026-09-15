package position

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// CloseOrder 是「裸 Close（未声明平今还是平昨）消耗今昨仓的顺序」。
//
// ✅ **判别实验 4 已在 CTP 上收敛（2026-09-14 夜盘，cn-futures-rules.md §13 #4）**：大商所上通用平仓消耗**昨仓**。
// 按 §13 #20 使用者裁决，全部 NoUseHistory 交易所跟 CTP ⇒ 用 [MeasuredCloseOrder] 取值，不要自己挑。
// ⚠️ 观测只覆盖大商所；UseHistory 交易所的裸 Close 在 CTP 上**没测过**（simnow_pending#1），那里仍然没有实测值。
//
// 零值是「未实测」，用它会**报错**——不是回退到某个「合理的默认」。
// 而它错了，平仓盈亏的基线（平昨用昨结算价 / 平今用开仓价）跟着错，当日盈亏与结存链条一并受影响。
type CloseOrder uint8

const (
	// CloseOrderUnmeasured 是零值：实验 4 未收敛，使用即报错。
	CloseOrderUnmeasured CloseOrder = iota
	// YesterdayFirst 先平昨、后平今。
	YesterdayFirst
	// TodayFirst 先平今、后平昨。⚠️ 在大商所 CTP 上被否（§13 #4：今 1 昨 1 通用平仓一手，消耗的是昨仓）。
	TodayFirst
	// FIFO 先开先平。
	//
	// ⚠️ **在本库的明细上它与 YesterdayFirst 恒等价**：昨仓必然比今仓先开（结算把当时的全部明细一起标昨），
	// 而同为昨仓或同为今仓时 YesterdayFirst 也按明细顺序消耗 —— 两者永远消耗同一批。
	// ⚠️ 前提由**调用约定**保证，**类型本身不保证**：明细只经 Open 按时间追加、Settle 一次标全部。
	// `Side.Append` 是导出的、`Lot.Settled` 可以随手设 —— 绕开这两条造出来的明细，两者可能分开（评审 20260915）。
	// 此前这里写「连续两个交易日各建一次种子就能分开」，那不成立。守卫 TestYesterdayFirstEqualsFIFO。
	FIFO
)

// MeasuredCloseOrder 给出某种 PositionDateType 上裸 Close 的**实测**消耗顺序；第二个返回值为 false 表示没有实测。
//
//	NoUseHistory   YesterdayFirst（§13 #4 CTP 实测于大商所；§13 #20 裁决全部 NoUseHistory 跟 CTP，郑商所等为外推）
//	其余           没有实测 —— UseHistory 上裸 Close 在 CTP 的语义未测（simnow_pending#1），本库对它报错
//
// ⚠️ **形状范围**（评审 20260915）：观测只覆盖**今 1 昨 1、通用平 1 手、消耗昨仓**这一个形状 ——
// 平量**未跨过**昨仓。跨过昨仓时「按先平昨延伸到今仓、以总量为上限」是**推得**，没有观测。
//
// ⚠️ 规则只住这一处：order 的可平量校验与将来的门面都从这里取，不各自写一份。
func MeasuredCloseOrder(dt refdata.PositionDateType) (CloseOrder, bool) {
	if dt == refdata.NoUseHistory {
		return YesterdayFirst, true
	}
	return CloseOrderUnmeasured, false
}

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

	dateType refdata.PositionDateType
	long     Side
	short    Side
}

// New 建一个空持仓。
//
// dateType 是该合约区不区分今昨仓（CTP 的 `PositionDateType`）。
//
// ⚠️ 零值 `PositionDateUnknown` **在这里就报错**。只重放当日成交、
// 永不结算的调用方请显式传 `refdata.PositionDateNotNeeded` ——
// **不要编一个值**：编出来的值会被后来的人当成实测值。
//
// # ⚠️ 这里曾经是一次假的取舍
//
// 第一版拦零值，于是 Replay 那批调用方被逼着为一个用不上的参数编值
// （天勤字典不给这个字段，实测过的只有两个合约）。我据此把关口整个撤了，
// 判据只留在 Settle —— 代价是「忘了传」要滑到结算才报。
//
// 评审指出第三条路：**零值同时表示「忘了传」和「不需要」，
// 才是这个取舍看起来非此即彼的全部原因。** 加一个非零的
// `PositionDateNotNeeded` 之后两件事分开了，两道关口各司其职：
//
//	New    拒绝零值，接受 NotNeeded  —— 「忘了传」在构造期就报
//	Settle 两个都拒                  —— 「我不结算」不等于「可以结算」
//
// ⚠️ dateType 放在 New 而不是 Settle 的参数里：合约的性质在持仓的一生里不变，
// 声明一次就不会在两次结算之间漂移。
func New(inst types.InstrumentID, hedge types.HedgeFlag, day types.TradingDay,
	dateType refdata.PositionDateType) (*Position, error) {

	if err := day.Validate(); err != nil {
		return nil, fmt.Errorf("建仓失败: %w", err)
	}
	if hedge == types.HedgeUnknown {
		// ⚠️ 投机套保标志决定保证金率，不能默认。
		return nil, fmt.Errorf("投机套保标志未指定 —— 它决定保证金率，不是标签")
	}
	if dateType == refdata.PositionDateUnknown {
		return nil, fmt.Errorf("合约 %s 的 PositionDateType 未指定 —— "+
			"它决定结算时今仓变不变昨仓，而那一步只发生一次。"+
			"⚠️ 若这条路上**确实用不到**它（只重放当日成交、永不结算），"+
			"请显式传 refdata.PositionDateNotNeeded —— **不要编一个值**，"+
			"编出来的值会被后来的人当成实测值",
			inst.Canonical())
	}
	return &Position{Instrument: inst, Hedge: hedge, Day: day, dateType: dateType}, nil
}

// DateType 返回该合约区不区分今昨仓。
func (p *Position) DateType() refdata.PositionDateType { return p.dateType }

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
//	Close           由 order 决定顺序 —— 用 MeasuredCloseOrder 取（只有 NoUseHistory 有实测值）
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
		return nil, fmt.Errorf("平仓消耗顺序未指定：本库不提供默认值 —— 用 MeasuredCloseOrder 按合约的 PositionDateType 取"+
			"（实验 4 只在 NoUseHistory 上有实测值：先平昨；UseHistory 上裸 Close 未测，simnow_pending#1）"+
			"。候选：%v / %v / %v", YesterdayFirst, TodayFirst, FIFO)

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
	// ⚠️ 结算时今仓一律变昨仓、基线推进到结算价 —— **两种 PositionDateType 相同**。
	//
	// ✅ 2026-09-15 使用者裁决（cn-futures-rules.md §13 #20）：**全部 NoUseHistory 交易所跟 CTP**（范围由使用者在评审会话中裁定）。
	// ⚠️ 观测只覆盖大商所（下面那一行）；郑商所及其他报 NoUseHistory 的交易所是**外推、无观测**。
	//
	//	CTP / SimNow  DCE.m2701 一手跨结算：Position 1 / TodayPosition 0 / YdPosition 1，平昨被接受（#4 夹具 ①、§13 #16）
	//	快期          同一种仓结算后 today=3 / his=0 —— 持仓永远记今仓（kq_facts 24）
	//
	// 两个口子各自成立，本库选 CTP（design.md §5：CTP 权威裁决）。此前这里对 NoUseHistory 走 RebaseAll（只推基线、
	// 不标昨仓），与快期一致、与 CTP 相反 —— 后果是大商所跨结算后平昨，柜台接受而本库拒绝。
	// ⚠️ 与快期的分岔是**有意的口子差**：跨日结转对拍一旦接上 DCE，类 B「NoUseHistory 不滚」会出现，按口子差登记。
	//
	// ⚠️ 基线推进这一半两个口子一致，且有 CTP 判别样本（开仓 3399 ≠ 昨结 3384，TestNoUseHistoryBasisAdvancesOnCTP）。
	//
	// ⚠️ PositionDateType 在这里**只剩一个作用**：零值 / NotNeeded 不许结算（下面的 default）。
	switch p.dateType {
	case refdata.UseHistory, refdata.NoUseHistory:
		p.long.SettleAll(settlementPrice)
		p.short.SettleAll(settlementPrice)
	default:
		// ⚠️ 这里是**唯一**的关口，New 刻意不拦（理由见 New 的注释）。
		return fmt.Errorf("合约 %s 的 PositionDateType 未指定，**无法结算** —— "+
			"它决定结算时今仓变不变昨仓，而那一步只发生一次。"+
			"⚠️ 它是**逐合约**的规则数据，天勤字典不给，只能靠柜台行为测"+
			"（见 state.md 的 kq_facts 24）；**不许按交易所推**："+
			"猜对的猜测与查过的事实长得一模一样，直到某个合约不一样为止。"+
			"⚠️ 猜错的后果是今昨仓不滚动（silent-risks.md 第 1 条）——"+
			"账永远是平的，只是平今费率一直按平昨收、保证金基线停在开仓日",
			p.Instrument.Canonical())
	}
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
