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
	Multiplier decimal.Decimal
	// PositionDate 是该合约区不区分今昨仓。
	//
	// ⚠️ 与本结构其余字段同理：**没有默认值**。零值会让 position.New 报错，
	// 那正是要的 —— 猜错的后果是今昨仓不滚动（silent-risks.md 第 1 条），
	// 账永远是平的，只是每一天都错。
	PositionDate  refdata.PositionDateType
	Commission    refdata.CommissionRates
	Margin        refdata.MarginRates
	MaxMarginSide bool
}

// Rebuilt 是重建的结果。
//
// ⚠️ FloatProfit 单独放在这里而不是塞进 account.Snapshot，
// 是因为**本库的账户快照里根本没有它** —— 那不是疏漏，是 view.AccountInput
// 的注释里写着的事实：浮动盈亏是逐笔对冲口径，账户侧不承载它，
// 由调用方从 pnl 侧提供。
//
// ⚠️ 而它正是本项目核心那对区分的另一半：
// account.Snapshot.PositionProfit 是逐日盯市，这里的 FloatProfit 是逐笔对冲。
type Rebuilt struct {
	Account account.Snapshot
	// FloatProfit 是全部持仓的浮动盈亏合计（逐笔对冲口径）。
	// HasFloatProfit 为假表示**算不出**（缺最新价），那是「没有」不是「零」。
	FloatProfit    decimal.Decimal
	HasFloatProfit bool
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
func Rebuild(f *Fixture, specs map[string]Spec) (Rebuilt, error) {
	var zero Rebuilt
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
	// ⚠️ 浮动盈亏与持仓盈亏**分开累加**：两者用的是不同的基线，
	// 而在今仓上它们恒等 —— 合并累加会让「它们本该不同」这件事永远看不出来。
	totalFloatProfit := decimal.Zero
	floatOK := true

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
			types.Speculation, spec.PositionDate, f.TradingDay, trades)
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
			pp, fp, err := profitsOf(f, sym, p, spec.Multiplier)
			if err != nil {
				// ⚠️ 缺最新价时**不当成零**：那会让一个算不出的合约
				// 悄悄按「不盈不亏」计入合计，而合计看起来完全正常。
				floatOK = false
				return zero, fmt.Errorf("%s 盈亏：%w", sym, err)
			}
			totalPositionProfit = totalPositionProfit.Add(pp)
			totalFloatProfit = totalFloatProfit.Add(fp)
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
	return Rebuilt{
		Account:     acc.Snapshot(),
		FloatProfit: totalFloatProfit, HasFloatProfit: floatOK,
	}, nil
}

func toLegs(lots []position.Lot) []pnl.Leg {
	out := make([]pnl.Leg, 0, len(lots))
	for _, l := range lots {
		out = append(out, pnl.Leg{Volume: l.Volume, OpenPrice: l.OpenPrice, Basis: l.Basis})
	}
	return out
}

// profitsOf 用夹具里的最新价算**两套口径**的盈亏。
//
// ⚠️ 一次算两个而不是各调一遍，是为了让「它们用的是同一批 leg、同一个价」
// 在代码上是显然的 —— 分两次取 leg 的话，一次改动只改了其中一处，
// 两个数就在不同的输入上算出来了，而它们本来就该相等的那些场合会掩盖它。
//
//	持仓盈亏 PositionProfit  逐日盯市，基线是 Lot.Basis
//	浮动盈亏 FloatProfit     逐笔对冲，基线是 Lot.OpenPrice
//
// ⚠️ 今仓上两者必然相等（两条基线都是开仓价）。
// 它们分开只发生在昨仓上 —— 那正是本项目最核心的那条区分。
func profitsOf(f *Fixture, sym string, p *position.Position,
	multiplier decimal.Decimal) (posProfit, floatProfit decimal.Decimal, err error) {

	last, ok := f.Positions[sym]["last_price"]
	if !ok || last.Absent || last.IsText || !last.Number.IsPositive() {
		return decimal.Zero, decimal.Zero,
			fmt.Errorf("没有最新价 —— 这是「没有」不是「零」")
	}
	prices := pnl.Prices{Last: last.Number, HasLast: true}
	for _, d := range []types.Direction{types.Buy, types.Sell} {
		s, err := p.Side(d)
		if err != nil {
			return decimal.Zero, decimal.Zero, err
		}
		if s.Volume() == 0 {
			continue
		}
		legs := toLegs(s.Lots())
		pp, err := pnl.PositionProfit(legs, d, prices, pnl.MarkLast, multiplier)
		if err != nil {
			return decimal.Zero, decimal.Zero, err
		}
		fp, err := pnl.FloatProfit(legs, d, prices, pnl.MarkLast, multiplier)
		if err != nil {
			return decimal.Zero, decimal.Zero, err
		}
		posProfit = posProfit.Add(pp)
		floatProfit = floatProfit.Add(fp)
	}
	return posProfit, floatProfit, nil
}

func numberOf(m map[string]Value, key string) (decimal.Decimal, bool) {
	v, ok := m[key]
	if !ok || v.Absent || v.IsText {
		return decimal.Zero, false
	}
	return v.Number, true
}
