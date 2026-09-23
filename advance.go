package futsim

import (
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Bar 是盘中推进的一根 K 线（design.md 门面形状 §13，§6.5 的「本库只要五样」）。
//
// ⚠️ 时间按 **[Start, End)** 读：End 是右开的。`Volume` / `Turnover` / `OpenInterest` / `Open` 都不收 ——
// 前三样本库用不上，`Open` 要了才能建模跳空成交，而本版一律按挂单价（§13 决策点 2）。
type Bar struct {
	Instrument       types.InstrumentID
	TradingDay       types.TradingDay
	Start, End       time.Time
	High, Low, Close decimal.Decimal
}

// Filled 是一根 K 线上成交的一笔挂单。⚠️ F9b 不撮合，这里恒为空 —— 撮合是 F9c。
type Filled struct {
	ID    string
	Trade match.Trade
}

// Advanced 是 Advance 的结果。
type Advanced struct {
	// Filled 是这根 K 线上成交的挂单。⚠️ F9b 恒为空。
	Filled []Filled
	// Unchecked 是这根 K 线上**没能查**的守卫项，与 Place 的「没查成」同形：
	// 算不出涨跌停（没 Mark 过昨结算价 / 没给取整方向 / 规格没比例）时跳过那一项并记在这里，**不报错** ——
	// 报错会让没配 TickRounding 的调用方完全用不了 Advance（design.md 门面形状 §13）。
	Unchecked []order.Unchecked
}

// Advance 用一根 K 线推进盘中：① 守卫 → ④ 按 Close 计价。
//
// ⚠️ **F9b 只做守卫与计价，不撮合**：②（挑单）③（逐笔 Fill）是 F9c，那一期要先定 `Choices.RestingFill`
// （第九项口径，使用者 20260922 裁决：零值在撮合入口报错、预设不给值）。所以本版的 `Advanced.Filled` 恒为空，
// 簿上有没有挂单都不影响这根 K 线的结果。
//
// 守卫（顺序即下面的顺序，任一条不过 ⇒ 报错且**状态不动**）：
//
//	交易日与门面当前的不同            ⇒ 报错（「先 Settle」，§4 的双层时钟在门面上的落点）
//	Start ≥ End / Low > 0 不成立      ⇒ 报错（数据错）
//	Low ≤ Close ≤ High 不成立         ⇒ 报错（数据错）
//	有日历时：[Start, End) 不在同一个连续时段里，或那一段属于别的交易日 ⇒ 报错
//	High / Low 越过当日涨跌停         ⇒ 报错（数据错，不裁；涨跌停与 Place 同一来源：refdata × TickRounding）
//	算不出涨跌停                      ⇒ **跳过这一项**，记进 Unchecked，不报错
//
// ⚠️ 原子性：本版只有最后一步 `Mark` 动状态，而 `Mark` 自己算完才 commit ⇒ 整根 K 线要么全推进要么全不动，
// 不需要 `State` / `Restore` 快照（F9c 有候选挂单时才需要，见 §13）。
func (s *Simulator) Advance(bar Bar) (Advanced, error) {
	var out Advanced
	if err := s.usable(bar.TradingDay); err != nil {
		return Advanced{}, err
	}
	if !bar.Start.Before(bar.End) {
		return Advanced{}, fmt.Errorf("%s 的 K 线 [%s, %s) 不是一段正时长 —— 按 [Start, End) 右开读",
			bar.Instrument, bar.Start.Format(time.RFC3339), bar.End.Format(time.RFC3339))
	}
	if !bar.Low.IsPositive() {
		return Advanced{}, fmt.Errorf("%s 的 K 线最低价 %s 不为正 —— 零只可能是缺失的伪装（§6.5）", bar.Instrument, bar.Low)
	}
	if bar.Low.GreaterThan(bar.Close) || bar.Close.GreaterThan(bar.High) {
		return Advanced{}, fmt.Errorf("%s 的 K 线 低 %s / 收 %s / 高 %s 不满足 低 ≤ 收 ≤ 高 —— 数据错",
			bar.Instrument, bar.Low, bar.Close, bar.High)
	}
	if err := s.barInOneSession(bar); err != nil {
		return Advanced{}, err
	}
	if err := s.barWithinLimits(bar, &out); err != nil {
		return Advanced{}, err
	}
	if err := s.Mark(bar.TradingDay, Quote{Instrument: bar.Instrument, Last: bar.Close, HasLast: true}); err != nil {
		return Advanced{}, err
	}
	return out, nil
}

// barInOneSession 判 [Start, End) 落在**同一个连续时段**里，且那一段属于这根 K 线声称的交易日。
//
// ⚠️ End 是右开的，所以判的是 End 的**前一瞬**：End 正好等于时段收盘时刻（10:15 / 11:30 / 15:00 / 23:00）时
// 这根 K 线是合法的最后一根 —— 拿 End 去单点查会把它们全误拒（评审 20260917 条件 1）。
// ⚠️ 没给日历就跳过这一项：日历是可选输入，没有它本来就答不了时段（与 Place 同一口径）。
func (s *Simulator) barInOneSession(bar Bar) error {
	if s.calendar == nil {
		return nil
	}
	ex, product := bar.Instrument.Exchange, bar.Instrument.Product
	from, err := s.calendar.SessionAt(bar.Start, ex, product)
	if err != nil {
		return fmt.Errorf("%s 的 K 线起点 %s：%w", bar.Instrument, bar.Start.Format("2006-01-02 15:04:05"), err)
	}
	last := bar.End.Add(-time.Nanosecond)
	to, err := s.calendar.SessionAt(last, ex, product)
	if err != nil {
		return fmt.Errorf("%s 的 K 线 [%s, %s) 的末尾越出了交易时段（End 右开，判的是它的前一瞬）：%w",
			bar.Instrument, bar.Start.Format("15:04:05"), bar.End.Format("15:04:05"), err)
	}
	if from.Session != to.Session || from.Anchor != to.Anchor {
		return fmt.Errorf("%s 的 K 线 [%s, %s) 跨了两个时段（起点在 %s→%s、末尾在 %s→%s）—— 一根 K 线只能落在一个连续时段里",
			bar.Instrument, bar.Start.Format("15:04:05"), bar.End.Format("15:04:05"),
			from.Session.Start, from.Session.End, to.Session.Start, to.Session.End)
	}
	if from.TradingDay != bar.TradingDay {
		return fmt.Errorf("%s 的 K 线声称交易日 %d，而 [%s, %s) 按日历属于交易日 %d",
			bar.Instrument, bar.TradingDay, bar.Start.Format("2006-01-02 15:04:05"), bar.End.Format("15:04:05"), from.TradingDay)
	}
	return nil
}

// barWithinLimits 判 High / Low 没越过当日涨跌停；算不出涨跌停时跳过并记进 Unchecked。
func (s *Simulator) barWithinLimits(bar Bar, out *Advanced) error {
	skip := func(missing string) {
		out.Unchecked = append(out.Unchecked, order.Unchecked{Check: order.CheckPriceLimit, Missing: missing})
	}
	inst, err := s.rules.Instrument(bar.Instrument)
	if err != nil {
		skip("合约规格")
		return nil
	}
	px := s.prices[bar.Instrument]
	rounding := s.tickRounding[bar.Instrument.Exchange]
	switch {
	case !px.hasPre:
		skip("昨结算价")
		return nil
	case !rounding.Valid():
		skip("取整方向 —— ⚠️ 两家交易所不同（上期所向下、大商所往里收），挑一个会静默错")
		return nil
	}
	up, lo, ok := inst.PriceLimits(px.pre, px.hasPre, rounding)
	if !ok {
		skip("涨跌幅比例（规则数据里没有）")
		return nil
	}
	if bar.High.GreaterThan(up) || bar.Low.LessThan(lo) {
		return fmt.Errorf("%s 的 K 线 高 %s / 低 %s 越过当日涨跌停 %s / %s —— 数据错，本库不裁（涨跌停与报单校验同一来源：昨结算价 %s × 比例，%v）",
			bar.Instrument, bar.High, bar.Low, up, lo, px.pre, rounding)
	}
	return nil
}
