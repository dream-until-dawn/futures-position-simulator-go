package ctpfixture

import (
	"fmt"
	"math"
	"sort"
	"testing"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// oneInstrumentRules 是只装一个合约的 refdata.Provider。
//
// ⚠️ PositionDateType 给 PositionDateNotNeeded：这条对拍只灌开仓与平今、永不结算。
// 那是调用方的声明 —— 编一个 UseHistory 会被后来的人当成从柜台合约表读来的。
type oneInstrumentRules struct {
	inst       refdata.Instrument
	commission refdata.CommissionRates
	margin     refdata.MarginRates
}

func (r oneInstrumentRules) check(id types.InstrumentID) error {
	if id != r.inst.ID {
		return fmt.Errorf("合约 %s 不在这份规则数据里", id)
	}
	return nil
}

func (r oneInstrumentRules) Instrument(id types.InstrumentID) (refdata.Instrument, error) {
	return r.inst, r.check(id)
}

func (r oneInstrumentRules) MarginRates(id types.InstrumentID, _ types.HedgeFlag) (refdata.MarginRates, error) {
	return r.margin, r.check(id)
}

func (r oneInstrumentRules) CommissionRates(id types.InstrumentID, _ types.HedgeFlag) (refdata.CommissionRates, error) {
	return r.commission, r.check(id)
}

func (oneInstrumentRules) ProductInstruments(types.Exchange, string) []types.InstrumentID { return nil }
func (oneInstrumentRules) Version() int64                                                 { return 20260915 }

// TestFacadeReplaysAg2702AgainstCTP 用**生产的门面**按 CTP 预设重放 SHFE.ag2702 交易日 20260915 的 9 笔成交，
// 在六份截面的时点逐项比那条持仓记录。
//
// 柜台一侧比的全是**同一条持仓记录**里的字段（同一次请求）：手数、OpenCost、UseMargin、PositionProfit、CloseProfit、Commission。
// ⚠️ 本库一侧，UseMargin / PositionProfit / CloseProfit / Commission 取的是**账户合计**（门面还没有逐持仓的这几项查询）。
// 它等于 ag2702 这条腿自己的值，**只因为**规则数据只装了这一个合约 —— 门面里结构上不可能有第二条腿。
// 多腿重放时这里要改取逐持仓的值，否则红得像门面算错了（评审 20260915）。
//
// ⚠️ **它钉住 CTPChoices 的哪几格**（评审 20260915 逐格翻过）：
//
//	FeeBasis / MarginBasis / Mark   钉住（翻掉分别红在 Commission / UseMargin / PositionProfit）
//	SideScope                       不钉：单合约单方向，三种范围同值
//	Algorithm                       不钉：本条不比 Available
//
// 后两格靠 TestPresetsLeaveExactlyTheUnmeasuredCellsEmpty 的字面断言，Algorithm 的 CTP 证据在 TestProductionAccountAvailableAgainstCTP。
// ⇒ 本条**不是**把 CTP 预设整个核过了。
// ⚠️ 持仓盈亏的计价价取记录自己的 `SettlementPrice`（盘中柜台就用它算持仓盈亏），不取行情快照的最新价 ——
// 行情与持仓是两次请求，中间价会动（§13 #18；-6 那份记录 15459、行情 15456）。
//
// ⚠️ 输入的来源，逐项：
//
//	乘数        天勤规格快照 specs-20260908.json          与柜台独立
//	手续费率    ctp-commission-rates-20260915.txt（柜台声明）
//	保证金率    持仓记录的 MarginRateByMoney（柜台声明，同一次请求）
//	昨结算价    行情快照
//	成交        -9 那份的当日成交（前几份的成交列表是它的前缀）
//
// ⚠️ **手续费**：柜台比本库**恰好**多 0.005 × 成交**手数** —— §13 #19（上期所行为里的每手 0.005 声明费率里没有）。
// 本样本每笔 1 手，手数与笔数同值；按笔数累加会在多手成交上少算，而报错会指向 #19（评审 20260915）。
// 本条钉的是**差恰好是那个常数**，不是「一致」：#19 哪天收敛、或本库手续费算错一分，这一格都会红。
func TestFacadeReplaysAg2702AgainstCTP(t *testing.T) {
	fx := loadCTP(t)
	slices := []string{"-4", "-5", "-6", "-7", "-8", "-9"}
	get := func(suffix string) ctpFixture {
		f, ok := fx["ctp-slices-20260915"+suffix+".json"]
		if !ok {
			t.Fatalf("⚠️ 缺夹具 ctp-slices-20260915%s.json", suffix)
		}
		return f
	}
	const sym, rec = "SHFE.ag2702", "SHFE.ag2702/2/1"
	day := types.TradingDay(20260915)
	id, err := types.ParseSymbol(sym, day)
	if err != nil {
		t.Fatal(err)
	}
	mult, ok := specMultiplier(t, sym, id)
	if !ok {
		t.Fatal("⚠️ 规格快照里没有 ag 的乘数")
	}
	first := get("-4")
	rules := oneInstrumentRules{
		inst: refdata.Instrument{ID: id, VolumeMultiple: mult, PriceTick: decimal.NewFromInt(1),
			PositionDateType: refdata.PositionDateNotNeeded},
		// ctp-commission-rates-20260915.txt：SHFE.ag2702  1e-05 0 | 1e-05 0 | 0 0（平今按额 0）
		commission: refdata.CommissionRates{OpenByMoney: decimal.RequireFromString("0.00001"),
			CloseByMoney: decimal.RequireFromString("0.00001")},
		margin: refdata.MarginRates{
			LongByMoney:  num(t, first.Positions[rec], "MarginRateByMoney"),
			ShortByMoney: num(t, first.Positions[rec], "MarginRateByMoney"),
		},
	}
	ch := futsim.CTPChoices()
	ch.FeeRounding = fee.NoRounding // ⚠️ §13 #5 未收敛；本合约的费额五位小数原样保留，「不取整」与「取到更细」同值
	sim, err := futsim.New(futsim.Config{Day: day, PreBalance: decimal.NewFromInt(20000000), Rules: rules, Choices: ch})
	if err != nil {
		t.Fatal(err)
	}
	pre := num(t, first.Quotes[sym], "PreSettlementPrice")
	if err := sim.Mark(day, futsim.Quote{Instrument: id, PreSettlement: pre, HasPreSettlement: true}); err != nil {
		t.Fatal(err)
	}

	trades := get("-9").Trades
	sort.Slice(trades, func(i, j int) bool { return flt(t, trades[i], "SequenceNo") < flt(t, trades[j], "SequenceNo") })
	applied, compared, lots := 0, 0, 0
	for _, suffix := range slices {
		f := get(suffix)
		r := f.Positions[rec]
		// 截面的计价价先给：本段成交之后的持仓盈亏按这份记录的价算
		if err := sim.Mark(day, futsim.Quote{Instrument: id, Last: num(t, r, "SettlementPrice"), HasLast: true}); err != nil {
			t.Fatal(err)
		}
		for ; applied < len(f.Trades); applied++ {
			tr := trades[applied]
			dir, err := types.DirectionFromCTP(tr["Direction"].(string))
			if err != nil {
				t.Fatal(err)
			}
			off, err := types.OffsetFromCTP(tr["OffsetFlag"].(string))
			if err != nil {
				t.Fatal(err)
			}
			vol := int(flt(t, tr, "Volume"))
			lots += vol
			if err := sim.ApplyTrade(day, match.Trade{Instrument: id, Direction: dir, Offset: off,
				Hedge: types.Speculation, Price: num(t, tr, "Price"), Volume: vol}); err != nil {
				t.Fatalf("第 %d 笔成交：%v", applied+1, err)
			}
		}
		p, ok := sim.Position(id, types.Speculation)
		if !ok {
			t.Fatalf("%s：门面里没有 ag2702 持仓", suffix)
		}
		lib := map[string]float64{}
		side, _ := p.Side(types.Buy)
		openCost := decimal.Zero
		for _, l := range side.Lots() {
			openCost = openCost.Add(l.OpenPrice.Mul(decimal.NewFromInt(int64(l.Volume))).Mul(mult))
		}
		a := sim.Account()
		lib["Position"] = float64(side.Volume())
		lib["OpenCost"], _ = openCost.Float64()
		lib["UseMargin"], _ = a.CurrMargin.Float64()
		lib["PositionProfit"], _ = a.PositionProfit.Float64()
		lib["CloseProfit"], _ = a.CloseProfit.Float64()
		lib["Commission"], _ = a.Commission.Add(decimal.RequireFromString("0.005").Mul(decimal.NewFromInt(int64(lots)))).Float64()

		for _, k := range []string{"Position", "OpenCost", "UseMargin", "PositionProfit", "CloseProfit", "Commission"} {
			want := flt(t, r, k)
			if math.Abs(lib[k]-want) > 1e-6 {
				extra := ""
				if k == "Commission" {
					extra = "（本库已加上 0.005 × 成交手数；差不再是这个常数 ⇒ §13 #19 变了，或本库手续费错了）"
				}
				t.Errorf("⚠️ ctp-slices-20260915%s（已灌 %d 笔）%s：本库 %v，柜台 %v%s", suffix, applied, k, lib[k], want, extra)
			}
		}
		compared++
	}
	if applied != 9 || compared != len(slices) {
		t.Fatalf("⚠️ 灌了 %d 笔、比了 %d 份（期望 9 / %d）—— 成交列表或截面变了", applied, compared, len(slices))
	}
}
