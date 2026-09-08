package fixture

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/pnl"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Spec 是重建一个合约所需的规则数据。
//
// ⚠️ 全部字段都必须由调用方给，**没有默认值**：
// 一个「差不多能用」的规格会在平今平昨和大边上静默算错
// （refdata.Builder 的零值报错也是这个理由）。
type Spec struct {
	Multiplier    decimal.Decimal
	Commission    refdata.CommissionRates
	Margin        refdata.MarginRates
	MaxMarginSide bool
}

// Rebuild 从一份夹具**重建整个账户**，然后与柜台的账户截面逐字段比。
//
// 链条：
//
//	pre_balance（夹具给的起点）
//	  → 逐笔成交：手续费累加、平仓盈亏累加
//	  → 全部持仓：保证金合计、持仓盈亏合计
//	  → account 自己推出 balance / available / risk_ratio
//
// ⚠️ 哪些字段有判别力、哪些是同义反复，必须分清：
//
//	同义反复  pre_balance / static_balance / deposit / withdraw
//	          —— 它们是**输入**，或由输入平凡推出；比它们等于比自己抄的数
//	有判别力  commission / close_profit / position_profit / margin
//	          —— 本库自己算的
//	真正的验收 balance / available / risk_ratio
//	          —— 由上面那几个**再推一层**，错一处就全错
//
// ⚠️ 本函数**不处理昨仓**：有昨仓的合约要先走 Carry。
// 调用方用 Fixture.HasHistoryPosition 判，这里只在撞上时报错。
func Rebuild(f *Fixture, specs map[string]Spec) (account.Snapshot, error) {
	var zero account.Snapshot
	pre, ok := numberOf(f.Account, "pre_balance")
	if !ok {
		return zero, fmt.Errorf("夹具 %s 的账户截面里没有 pre_balance —— "+
			"⚠️ 那是整条资金链的起点，没有它重建出来的每一个数都是错的", f.Path)
	}
	acc, err := account.New("CNY", f.TradingDay, pre)
	if err != nil {
		return zero, err
	}

	// —— 入金出金 ——
	//
	// ⚠️ 照实取，不假设为零：本批夹具里它们确实都是 0，
	// 而「恒为 0 的减项不会暴露自己被漏掉」正是 §13 第 12 条。
	if d, ok := numberOf(f.Account, "deposit"); ok && d.IsPositive() {
		if err := acc.Deposit(f.TradingDay, d); err != nil {
			return zero, err
		}
	}
	if w, ok := numberOf(f.Account, "withdraw"); ok && w.IsPositive() {
		if err := acc.Withdraw(f.TradingDay, w); err != nil {
			return zero, err
		}
	}

	totalMarginCompany, totalMarginExchange := decimal.Zero, decimal.Zero
	totalPositionProfit := decimal.Zero

	for _, sym := range f.Symbols() {
		trades := f.TradesOf(sym)
		if len(trades) == 0 {
			continue
		}
		if f.HasHistoryPosition(sym) {
			return zero, fmt.Errorf("⚠️ %s 有昨仓，而本函数只重放当日成交 —— "+
				"昨仓那几手今天的成交里没有一笔能解释它，先走 Carry", sym)
		}
		spec, ok := specs[sym]
		if !ok {
			return zero, fmt.Errorf("合约 %s 没有规格 —— "+
				"⚠️ 不填默认值：一个「差不多能用」的规格会静默算错", sym)
		}
		preSettle, ok := f.PreSettlement(sym)
		if !ok {
			return zero, fmt.Errorf("合约 %s 没有昨结算价 —— "+
				"⚠️ 手续费基准、保证金基准都要它；这份夹具不自足", sym)
		}

		// —— 逐笔：手续费 ——
		for _, tr := range trades {
			c, err := fee.Compute(spec.Commission, tr.Offset, preSettle,
				spec.Multiplier, tr.Volume, decimalx.NoRounding)
			if err != nil {
				return zero, fmt.Errorf("%s 手续费：%w", sym, err)
			}
			if err := acc.AddCommission(f.TradingDay, c); err != nil {
				return zero, err
			}
		}

		// —— 重放：持仓与已实现平仓 ——
		p, realized, err := ReplayRealized(nil, trades[0].Instrument,
			types.Speculation, f.TradingDay, trades)
		if err != nil {
			return zero, fmt.Errorf("%s 重放：%w", sym, err)
		}
		for _, rz := range realized {
			legs := toLegs(rz.Consumed)
			res, err := pnl.CloseProfit(legs, rz.Direction, rz.ClosePrice, spec.Multiplier)
			if err != nil {
				return zero, fmt.Errorf("%s 平仓盈亏：%w", sym, err)
			}
			// ⚠️ 用**逐日盯市**口径进结存：中国期货的资金结算走这一套
			// （cn-futures-rules.md §5）。逐笔对冲是客户账单上的另一栏，
			// 不参与结存 —— 混用会让权益曲线整条错，且今仓上看不出来。
			if err := acc.AddCloseProfit(f.TradingDay, res.ByDate); err != nil {
				return zero, err
			}
		}

		// —— 截面：保证金与持仓盈亏 ——
		long, short, err := MarginOf(p, spec.Margin, spec.Multiplier, preSettle,
			spec.MaxMarginSide, margin.PreSettleAll, margin.ByInstrument)
		switch {
		case IsNoPosition(err):
			// 空仓：不占保证金，也没有持仓盈亏。
		case err != nil:
			return zero, fmt.Errorf("%s 保证金：%w", sym, err)
		default:
			totalMarginExchange = totalMarginExchange.Add(long).Add(short)
			totalMarginCompany = totalMarginCompany.Add(long).Add(short)
			pp, err := positionProfitOf(f, sym, p, spec.Multiplier)
			if err != nil {
				return zero, fmt.Errorf("%s 持仓盈亏：%w", sym, err)
			}
			totalPositionProfit = totalPositionProfit.Add(pp)
		}
	}

	if err := acc.SetMargin(f.TradingDay, totalMarginCompany, totalMarginExchange); err != nil {
		return zero, err
	}
	if err := acc.SetPositionProfit(f.TradingDay, totalPositionProfit); err != nil {
		return zero, err
	}
	// ⚠️ 重建完先自查内部不变式，再拿去与柜台比。
	// 内部就不自洽的话，与柜台比出来的差异指向的是本库内部，而不是两边的差异。
	if err := acc.Check(); err != nil {
		return zero, fmt.Errorf("重建出来的账户内部不自洽：%w —— "+
			"⚠️ 此时与柜台比出的差异指向本库内部，不是两边的差异", err)
	}
	return acc.Snapshot(), nil
}

func toLegs(lots []position.Lot) []pnl.Leg {
	out := make([]pnl.Leg, 0, len(lots))
	for _, l := range lots {
		out = append(out, pnl.Leg{Volume: l.Volume, OpenPrice: l.OpenPrice, Basis: l.Basis})
	}
	return out
}

// positionProfitOf 用夹具里的最新价算持仓盈亏（逐日盯市口径）。
func positionProfitOf(f *Fixture, sym string, p *position.Position,
	multiplier decimal.Decimal) (decimal.Decimal, error) {

	last, ok := f.Positions[sym]["last_price"]
	if !ok || last.Absent || last.IsText || !last.Number.IsPositive() {
		return decimal.Zero, fmt.Errorf("没有最新价 —— 这是「没有」不是「零」")
	}
	total := decimal.Zero
	for _, d := range []types.Direction{types.Buy, types.Sell} {
		s, err := p.Side(d)
		if err != nil {
			return decimal.Zero, err
		}
		if s.Volume() == 0 {
			continue
		}
		v, err := pnl.PositionProfit(toLegs(s.Lots()), d,
			pnl.Prices{Last: last.Number, HasLast: true}, pnl.MarkLast, multiplier)
		if err != nil {
			return decimal.Zero, err
		}
		total = total.Add(v)
	}
	return total, nil
}

func numberOf(m map[string]Value, key string) (decimal.Decimal, bool) {
	v, ok := m[key]
	if !ok || v.Absent || v.IsText {
		return decimal.Zero, false
	}
	return v.Number, true
}
