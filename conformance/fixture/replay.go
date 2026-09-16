package fixture

import (
	"fmt"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Replayed 是在门面上重放 / 结转出来的结果：持仓，以及**这个合约逐方向**的保证金占用。
//
// ⚠️ HasMargin 为假有两种原因，都不许悄悄当成零：
//
//	借来的昨结算价（borrowedPre）  占用依赖它，而借来的数不属于这份证据（design.md「门面的形状」§11 决策点 3、§12）
//	这个合约此刻空仓            没有占用可言
//
// ⚠️ 给的是**交易所口径**（与柜台比的那一档）。公司口径含券商加收，是券商侧的配置；两者在本批实测费率上恒等（加收为零，待实测第 14 条），
// 分不开不等于同一件事 —— 要公司口径走门面的 MarginGroups。
//
// ⚠️ 逐方向的数取自门面的 MarginGroups，且**只认组键等于本合约**的那一组：
// 快期口径按合约合并（ByInstrument），组键就是合约；按品种合并时一组跨多个合约，「这个合约的多头占用」没有定义。
type Replayed struct {
	Position                *position.Position
	MarginLong, MarginShort decimal.Decimal
	HasMargin               bool
}

// replayedOf 从模拟器取持仓与逐方向占用。borrowedPre 为真时**不给**占用。
func replayedOf(sim *futsim.Simulator, inst types.InstrumentID, borrowedPre bool) Replayed {
	p, _ := sim.Position(inst, types.Speculation)
	out := Replayed{Position: p}
	if borrowedPre {
		return out
	}
	for _, g := range sim.MarginGroups() {
		if g.Key != inst.Canonical() {
			continue
		}
		out.MarginLong, out.MarginShort, out.HasMargin = g.LongExchange, g.ShortExchange, true
		break
	}
	return out
}

// ReplayOnFacade 在**门面**上重放一份夹具里某个合约的**当日**成交，返回持仓与逐方向占用。
//
// F7c（2026-09-15）之前这里是 Replay / ReplayFrom / ReplayRealized：自己调 position.Open / Close、
// 自己把 UseHistory 上的裸 CLOSE 翻成平昨（closeOffsetOf，与门面 datedOffset 同义的第二处）。
// 现在记账链条只在门面里有一份（design.md「门面的形状」§11）。
//
// # ⚠️ pre 由调用方显式给，而且**只当门面的计价前提用**
//
// 门面每记一笔成交都要算手续费（快期口径按昨结算价），有仓时要计价输入；旧的逐合约重放两样都不要。
// 交易日 20260908 行情进夹具之前采的那批夹具没有昨结算价 —— 对拍测试从同日同合约的兄弟夹具借一个唯一值传进来
// （使用者 2026-09-15 定，design.md §11 决策点 3）。
// ⇒ 本函数**只返回持仓**，不返回模拟器与账户：借来的数让门面跑得起来，而占用 / 手续费 / 持仓盈亏这些依赖它的数
// 从这里拿不出去，结构上进不了任何比对。要比保证金，用夹具**自己**报的昨结算价，缺就跳过。
//
// ⚠️ borrowedPre 由调用方**显式**告知（昨结算价是不是借同日兄弟夹具的）：借来的时候不返回占用 ——
// F7c 只在文档与返回类型上说「借来的只当前提」，F8 把它变成结构上的（§12）。
//
// ⚠️ 与 ReconstructOnFacade 建模拟器的那十几行重复，是刻意没合：那一段锚着 590–597 八条破坏，合并会让它们全部改指。
//
// ⚠️ 失去的一道检查：旧 Replay 的「三种消耗顺序各跑一遍、不一致就报歧义」。当日样本全是今仓，三种顺序结构上消耗同一批，
// 那道检查在当日样本上一直在答「一致」（silent-risks 登记）。
func ReplayOnFacade(f *Fixture, symbol string, spec Spec,
	dateType refdata.PositionDateType, pre decimal.Decimal, borrowedPre bool) (Replayed, error) {

	inst, err := types.ParseSymbol(symbol, f.TradingDay)
	if err != nil {
		return Replayed{}, fmt.Errorf("合约键 %q：%w", symbol, err)
	}
	trades := f.TradesOf(symbol)
	if len(trades) == 0 {
		return Replayed{}, fmt.Errorf("夹具 %s 里 %s 一笔成交都没有 —— 重放出来是空仓，而空仓与「有仓但没记录」长得一样", f.Path, symbol)
	}
	pb, ok := numberOf(f.Account, "pre_balance")
	if !ok {
		return Replayed{}, fmt.Errorf("夹具 %s 没有 pre_balance", f.Path)
	}
	last, ok := f.Positions[symbol]["last_price"]
	if !ok || last.Absent || last.IsText || !last.Number.IsPositive() {
		return Replayed{}, fmt.Errorf("夹具 %s 里 %s 没有最新价 —— 这是「没有」不是「零」", f.Path, symbol)
	}

	rules := specRules{version: 1, byID: map[types.InstrumentID]Spec{inst: spec},
		dates: map[types.InstrumentID]refdata.PositionDateType{inst: dateType}}
	sim, err := futsim.New(futsim.Config{Day: f.TradingDay, PreBalance: pb, Rules: rules, Choices: fixtureChoices()})
	if err != nil {
		return Replayed{}, err
	}
	if err := sim.Mark(f.TradingDay, futsim.Quote{Instrument: inst, Last: last.Number, HasLast: true,
		PreSettlement: pre, HasPreSettlement: true}); err != nil {
		return Replayed{}, err
	}
	for _, tr := range trades {
		if err := sim.ApplyTrade(f.TradingDay, match.Trade{Instrument: inst, Direction: tr.Direction, Offset: tr.Offset,
			Hedge: types.Speculation, Price: tr.Price, Volume: tr.Volume}); err != nil {
			return Replayed{}, fmt.Errorf("重放 %s 成交 %s（%v/%v %d 手）：%w", symbol, tr.TradeID, tr.Direction, tr.Offset, tr.Volume, err)
		}
	}
	return replayedOf(sim, inst, borrowedPre), nil
}

func opposite(d types.Direction) types.Direction {
	if d == types.Buy {
		return types.Sell
	}
	return types.Buy
}
