package ctpfixture

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
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

// TestFacadeSettlesM2701AcrossDaysAgainstCTP 用**生产的门面**按 CTP 预设走完 #4 夹具的跨日：
// DCE.m2701 交易日 20260914 开多 1 @3399 → 按 3384 结算 → 20260915 开今 1 @3360 → OF_Close 1 @3360，
// 在四个时点比那条持仓记录（`DCE.m2701/2/1`，今昨合在一条里）。
//
// 它钉住：结算滚仓、基线推进（PositionCost 33840）、昨仓保证金按昨结算价（4737.6，CTP 预设的 MarginBasis 第一次在昨仓上被判别）、
// 今昨混合占用（9441.6）、裸 CLOSE 先平昨（#4，CloseProfit −240）、以及 §13 #21 那一笔手续费 0.1。
// ⚠️ #21 三个候选都预言 0.1，这一格不判别候选，只钉「本库在收敛前的做法与这一笔一致」。
//
// ⚠️ **它钉住 CTPChoices 的哪几格**（评审 20260915 逐格翻过）：
//
//	MarginBasis  钉住。昨仓「按昨结算价」相对「按最新价」在「开盘前」那一格判别（LastAll 给 4704、柜台 4737.6）；
//	             相对 PreSettleAll 的判别只来自今仓腿（14:00 / -2 / -3）—— 只有昨仓时两者同值
//	Mark         钉住（PositionProfit 四格）
//	FeeBasis     不钉：m2701 按手收费（按额 0），计价口径不影响手续费 —— 这条钉的是手续费**档位**（#21 那笔），不是计价
//	SideScope / Algorithm  不钉：单合约、不比 Available
//
// ⚠️ 输入来源：乘数 specs-20260908.json；手续费率 ctp-commission-rates-20260915.txt（DCE.m2701 每手 0.2 / 0.2 / 0.1，按额 0）；
// 保证金率取 -2 那条记录的 MarginRateByMoney（昨仓单独时记录报 0，§13 #1）；PositionDateType 取 measured-rules-20260909.json
// （快期结算行为实测 no_use_history）；结算价 = 次日行情的 PreSettlementPrice。
// ⚠️ 本库一侧取账户合计：规则数据只装 m2701，门面里结构上不可能有第二条腿。
// ⚠️ 账户级跨日（上日结存）不比：两对跨日截面都有未归因的正差（§13 #5）。
func TestFacadeSettlesM2701AcrossDaysAgainstCTP(t *testing.T) {
	fx := loadCTP(t)
	get := func(name string) ctpFixture {
		f, ok := fx[name]
		if !ok {
			t.Fatalf("⚠️ 缺夹具 %s", name)
		}
		return f
	}
	const sym, rec = "DCE.m2701", "DCE.m2701/2/1"
	d1, d2 := types.TradingDay(20260914), types.TradingDay(20260915)
	id, err := types.ParseSymbol(sym, d1)
	if err != nil {
		t.Fatal(err)
	}
	mult, ok := specMultiplier(t, sym, id)
	if !ok {
		t.Fatal("⚠️ 规格快照里没有 m 的乘数")
	}
	dayEnd := get("ctp-status-20260914-10.json")
	s0, s2, s3 := get("ctp-slices-20260915.json"), get("ctp-slices-20260915-2.json"), get("ctp-slices-20260915-3.json")
	rate := num(t, s2.Positions[rec], "MarginRateByMoney")
	if !rate.IsPositive() {
		t.Fatal("⚠️ -2 那条记录没给保证金率")
	}
	d := decimal.RequireFromString
	rules := oneInstrumentRules{
		inst: refdata.Instrument{ID: id, VolumeMultiple: mult, PriceTick: decimal.NewFromInt(1),
			PositionDateType: refdata.NoUseHistory},
		commission: refdata.CommissionRates{OpenByVolume: d("0.2"), CloseByVolume: d("0.2"), CloseTodayByVolume: d("0.1")},
		margin:     refdata.MarginRates{LongByMoney: rate, ShortByMoney: rate},
	}
	ch := futsim.CTPChoices()
	ch.FeeRounding = fee.NoRounding // §13 #5 未收敛；按手收费，取整口径不起作用
	sim, err := futsim.New(futsim.Config{Day: d1, PreBalance: decimal.NewFromInt(20000000), Rules: rules, Choices: ch})
	if err != nil {
		t.Fatal(err)
	}
	markRec := func(day types.TradingDay, f ctpFixture) {
		t.Helper()
		r := f.Positions[rec]
		if err := sim.Mark(day, futsim.Quote{Instrument: id,
			Last: num(t, r, "SettlementPrice"), HasLast: true,
			PreSettlement: num(t, r, "PreSettlementPrice"), HasPreSettlement: true}); err != nil {
			t.Fatal(err)
		}
	}
	apply := func(day types.TradingDay, tr map[string]any) {
		t.Helper()
		dir, err := types.DirectionFromCTP(tr["Direction"].(string))
		if err != nil {
			t.Fatal(err)
		}
		off, err := types.OffsetFromCTP(tr["OffsetFlag"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if err := sim.ApplyTrade(day, match.Trade{Instrument: id, Direction: dir, Offset: off,
			Hedge: types.Speculation, Price: num(t, tr, "Price"), Volume: int(flt(t, tr, "Volume"))}); err != nil {
			t.Fatalf("成交 %v：%v", tr["TradeID"], err)
		}
	}
	compare := func(label string, f ctpFixture) {
		t.Helper()
		r := f.Positions[rec]
		p, ok := sim.Position(id, types.Speculation)
		if !ok {
			t.Fatalf("%s：门面里没有 m2701 持仓", label)
		}
		side, _ := p.Side(types.Buy)
		openCost, posCost := decimal.Zero, decimal.Zero
		for _, l := range side.Lots() {
			v := decimal.NewFromInt(int64(l.Volume)).Mul(mult)
			openCost = openCost.Add(l.OpenPrice.Mul(v))
			posCost = posCost.Add(l.Basis.Mul(v))
		}
		a := sim.Account()
		lib := map[string]decimal.Decimal{
			"Position": decimal.NewFromInt(int64(side.Volume())), "TodayPosition": decimal.NewFromInt(int64(side.VolumeToday())),
			"OpenCost": openCost, "PositionCost": posCost,
			"UseMargin": a.CurrMargin, "PositionProfit": a.PositionProfit, "CloseProfit": a.CloseProfit, "Commission": a.Commission,
		}
		for _, k := range []string{"Position", "TodayPosition", "OpenCost", "PositionCost", "UseMargin", "PositionProfit", "CloseProfit", "Commission"} {
			got, _ := lib[k].Float64()
			if want := flt(t, r, k); math.Abs(got-want) > 1e-6 {
				t.Errorf("⚠️ %s %s：本库 %v，柜台 %v", label, k, got, want)
			}
		}
	}

	// 20260914：开多 1 @3399（记录 OpenCost 33990）
	markRec(d1, dayEnd)
	apply(d1, map[string]any{"TradeID": "20260914 开仓（由 OpenCost 33990 反推）", "Direction": "0", "OffsetFlag": "0", "Price": 3399.0, "Volume": 1.0})
	compare("20260914 14:00", dayEnd)

	// 结算：今结算价 = 次日行情的昨结算价
	settle := num(t, s0.Quotes[sym], "PreSettlementPrice")
	if err := sim.Settle(d1, map[types.InstrumentID]decimal.Decimal{id: settle}, d2); err != nil {
		t.Fatal(err)
	}
	markRec(d2, s0)
	compare("20260915 开盘前（-）", s0)

	trades := s3.Trades
	sort.Slice(trades, func(i, j int) bool { return flt(t, trades[i], "SequenceNo") < flt(t, trades[j], "SequenceNo") })
	if len(trades) != 2 || len(s2.Trades) != 1 {
		t.Fatalf("⚠️ -2/-3 的成交数 %d/%d，期望 1/2 —— 夹具变了", len(s2.Trades), len(trades))
	}
	markRec(d2, s2)
	apply(d2, trades[0])
	compare("-2 开今之后", s2)
	markRec(d2, s3)
	apply(d2, trades[1])
	compare("-3 通用平仓之后", s3)
}

// TestFacadeUndatedCloseOnYesterdayOnlyAgainstCTP 是 §13 #21 **第二段**的对拍：只有昨仓时裸平，手续费收哪一档。
//
// ⚠️ 上一条（TestFacadeSettlesM2701AcrossDaysAgainstCTP）钉的是第一段 —— 今1昨1 裸平1 收平今档 0.1 ——
// 而那一段三个候选**都预言 0.1**，不判别候选。这一条才是判别的那一格：
//
//	E1  今0昨2 裸平1   (a) 收平昨 0.2   (b) 收平今 0.1   (c) 收「行为平昨费率」—— 若等于 0.1 与 b 同值
//	实测 0.2 ⇒ 只剩 (a)
//
// ⚠️ **它在 §13 #21 收敛之前会红**：本库当时对「超出今仓那一段、两档费率又不同」报错不猜，
// 于是 E1 那笔成交根本进不了门面。这正是它的判别力 —— 从生产代码出发、比柜台的字节，
// 不是在测试里手写一个期望值（silent-risks 方法论 96）。
//
// ⚠️ E2（显式平昨）那一笔也比，但**它不是第二次独立判别**：发出去的是 OF_CloseYesterday，
// 成交记录里的开平标志却是 '1'（大商所改写成了通用平仓）⇒ 它进门面时也是一笔裸 CLOSE，与 E1 同一种输入。
//
// ⚠️ 输入来源：20260916 的两片开仓价从 ① 的 OpenCost 68530 与 -2 的 34260 反推（3427、3426）；
// 先开哪一片由 FIFO（§13 #13）推得 —— **推错了 OpenCost 那一格会红**，所以这个假设是被比对着的。
// 结算价 = ① 行情的 PreSettlementPrice 3434；保证金率取 -2 那条记录（① 只有昨仓，记录报 0，§13 #1）。
// ⚠️ 20260916 当天的计价不比（没有那一天的截面），它只是为了走一次真实的 Settle 把两片翻成昨仓。
func TestFacadeUndatedCloseOnYesterdayOnlyAgainstCTP(t *testing.T) {
	fx := loadCTP(t)
	get := func(name string) ctpFixture {
		f, ok := fx[name]
		if !ok {
			t.Fatalf("⚠️ 缺夹具 %s", name)
		}
		return f
	}
	const sym, rec = "DCE.m2701", "DCE.m2701/2/1"
	d0, d1 := types.TradingDay(20260916), types.TradingDay(20260917)
	id, err := types.ParseSymbol(sym, d0)
	if err != nil {
		t.Fatal(err)
	}
	mult, ok := specMultiplier(t, sym, id)
	if !ok {
		t.Fatal("⚠️ 规格快照里没有 m 的乘数")
	}
	e1Before, e1After := get("ctp-slices-20260917.json"), get("ctp-slices-20260917-2.json")
	e2After := get("ctp-slices-20260917-4.json")
	rate := num(t, e1After.Positions[rec], "MarginRateByMoney")
	if !rate.IsPositive() {
		t.Fatal("⚠️ -2 那条记录没给保证金率")
	}
	d := decimal.RequireFromString
	rules := oneInstrumentRules{
		inst: refdata.Instrument{ID: id, VolumeMultiple: mult, PriceTick: decimal.NewFromInt(1),
			PositionDateType: refdata.NoUseHistory},
		commission: refdata.CommissionRates{OpenByVolume: d("0.2"), CloseByVolume: d("0.2"), CloseTodayByVolume: d("0.1")},
		margin:     refdata.MarginRates{LongByMoney: rate, ShortByMoney: rate},
	}
	ch := futsim.CTPChoices()
	ch.FeeRounding = fee.NoRounding
	sim, err := futsim.New(futsim.Config{Day: d0, PreBalance: decimal.NewFromInt(20000000), Rules: rules, Choices: ch})
	if err != nil {
		t.Fatal(err)
	}
	settle := num(t, e1Before.Quotes[sym], "PreSettlementPrice")

	// 20260916：两片开仓（价格反推，次序按 FIFO 推 —— 由下面 OpenCost 的比对核对）
	if err := sim.Mark(d0, futsim.Quote{Instrument: id, Last: settle, HasLast: true,
		PreSettlement: settle, HasPreSettlement: true}); err != nil {
		t.Fatal(err)
	}
	for _, px := range []string{"3427", "3426"} {
		if err := sim.ApplyTrade(d0, match.Trade{Instrument: id, Direction: types.Buy, Offset: types.Open,
			Hedge: types.Speculation, Price: d(px), Volume: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := sim.Settle(d0, map[types.InstrumentID]decimal.Decimal{id: settle}, d1); err != nil {
		t.Fatal(err)
	}

	markRec := func(f ctpFixture) {
		t.Helper()
		r := f.Positions[rec]
		if err := sim.Mark(d1, futsim.Quote{Instrument: id,
			Last: num(t, r, "SettlementPrice"), HasLast: true,
			PreSettlement: num(t, r, "PreSettlementPrice"), HasPreSettlement: true}); err != nil {
			t.Fatal(err)
		}
	}
	compare := func(label string, f ctpFixture) {
		t.Helper()
		r := f.Positions[rec]
		a := sim.Account()
		lib := map[string]decimal.Decimal{
			"CloseProfit": a.CloseProfit, "Commission": a.Commission,
			"UseMargin": a.CurrMargin, "PositionProfit": a.PositionProfit,
			"Position": decimal.Zero, "TodayPosition": decimal.Zero, "OpenCost": decimal.Zero, "PositionCost": decimal.Zero,
		}
		// ⚠️ 平光之后门面里可能已经没有这条持仓 —— 那时四个持仓字段按 0 比，柜台同样报 0。
		if p, ok := sim.Position(id, types.Speculation); ok {
			side, _ := p.Side(types.Buy)
			openCost, posCost := decimal.Zero, decimal.Zero
			for _, l := range side.Lots() {
				v := decimal.NewFromInt(int64(l.Volume)).Mul(mult)
				openCost = openCost.Add(l.OpenPrice.Mul(v))
				posCost = posCost.Add(l.Basis.Mul(v))
			}
			lib["Position"] = decimal.NewFromInt(int64(side.Volume()))
			lib["TodayPosition"] = decimal.NewFromInt(int64(side.VolumeToday()))
			lib["OpenCost"], lib["PositionCost"] = openCost, posCost
		}
		for _, k := range []string{"Position", "TodayPosition", "OpenCost", "PositionCost", "UseMargin", "PositionProfit", "CloseProfit", "Commission"} {
			got, _ := lib[k].Float64()
			if want := flt(t, r, k); math.Abs(got-want) > 1e-6 {
				t.Errorf("⚠️ %s %s：本库 %v，柜台 %v", label, k, got, want)
			}
		}
	}
	apply := func(label string, tr map[string]any) {
		t.Helper()
		dir, err := types.DirectionFromCTP(tr["Direction"].(string))
		if err != nil {
			t.Fatal(err)
		}
		off, err := types.OffsetFromCTP(tr["OffsetFlag"].(string))
		if err != nil {
			t.Fatal(err)
		}
		if err := sim.ApplyTrade(d1, match.Trade{Instrument: id, Direction: dir, Offset: off,
			Hedge: types.Speculation, Price: num(t, tr, "Price"), Volume: int(flt(t, tr, "Volume"))}); err != nil {
			t.Fatalf("⚠️ %s 那笔成交进不了门面：%v —— §13 #21 收敛之前本库对这一段报错不猜，这里就是它会红的地方", label, err)
		}
	}

	markRec(e1Before)
	compare("E1 ① 只有昨仓、平仓之前", e1Before)

	if len(e1After.Trades) != 1 || len(e2After.Trades) != 2 {
		t.Fatalf("⚠️ -2/-4 的成交数 %d/%d，期望 1/2 —— 夹具变了", len(e1After.Trades), len(e2After.Trades))
	}
	if off := e1After.Trades[0]["OffsetFlag"]; off != "1" {
		t.Fatalf("⚠️ 前提：E1 那笔是裸 CLOSE（'1'），夹具里是 %v", off)
	}
	markRec(e1After)
	apply("E1 裸平", e1After.Trades[0])
	compare("E1 ② 裸平 1 手之后（判别 #21 的那一格）", e1After)

	trades := e2After.Trades
	sort.Slice(trades, func(i, j int) bool { return flt(t, trades[i], "SequenceNo") < flt(t, trades[j], "SequenceNo") })
	// ⚠️ E2 发出去的是显式平昨（'4'），成交记录却是 '1' —— 钉住这个改写，它决定了 E2 **不是**独立判别。
	if off := trades[1]["OffsetFlag"]; off != "1" {
		t.Errorf("⚠️ E2 那笔的成交开平标志是 %v —— 此前量到的是 '1'（大商所把显式平昨改写成了平仓）。"+
			"变了的话 E2 就成了一次真正的「显式平昨」观测，§13 #21 的证据要重新数", off)
	}
	markRec(e2After)
	apply("E2 平昨", trades[1])
	compare("E2 ② 平昨 1 手之后", e2After)
}

// TestFacadeFreezeRb2701AgainstCTP 用门面的 FreezeOf 按 CTP 预设算一笔开仓挂单的冻结，比 ctp-frozen-20260910 的账户冻结字段。
//
// 那一刻账上只挂着这一笔：SHFE.rb2701 买开 1 手，记录的 LongFrozenAmount 30050 ⇒ 挂单价 3005。
//
//	FrozenMargin      4808 = 3005 × 10 × 0.16（挂单价）；按昨结算价 3164 会是 5062.4 —— 钉住 CTPChoices.FreezeMargin
//	FrozenCommission  3.01 = 3005 × 10 × 0.0001 + 0.005 —— 声明费率里没有每手 0.005（§13 #19），本库按声明算得 3.005，差恰好 0.005 × 手数
//
// ⚠️ 挂单价是从委托额反推的（夹具不带委托明细）；昨结算价取同一条记录的 PreSettlementPrice。
func TestFacadeFreezeRb2701AgainstCTP(t *testing.T) {
	f, ok := loadCTP(t)["ctp-frozen-20260910.json"]
	if !ok {
		t.Fatal("⚠️ 缺夹具 ctp-frozen-20260910.json")
	}
	const sym, rec = "SHFE.rb2701", "SHFE.rb2701/1"
	day := types.TradingDay(20260910)
	id, err := types.ParseSymbol(sym, day)
	if err != nil {
		t.Fatal(err)
	}
	mult, ok := specMultiplier(t, sym, id)
	if !ok {
		t.Fatal("⚠️ 规格快照里没有 rb 的乘数")
	}
	r := f.Positions[rec]
	amount, frozenVol := num(t, r, "LongFrozenAmount"), int64(flt(t, r, "LongFrozen"))
	if frozenVol != 1 {
		t.Fatalf("⚠️ 前提：只挂一手，得到 LongFrozen %d", frozenVol)
	}
	price := amount.Div(mult)
	d := decimal.RequireFromString
	rules := oneInstrumentRules{
		inst: refdata.Instrument{ID: id, VolumeMultiple: mult, PriceTick: decimal.NewFromInt(1),
			PositionDateType: refdata.PositionDateNotNeeded},
		// ctp-commission-rates-20260915.txt：SHFE.rb2701 三档按额 0.0001、每手 0（声明）
		commission: refdata.CommissionRates{OpenByMoney: d("0.0001"), CloseByMoney: d("0.0001"), CloseTodayByMoney: d("0.0001")},
		margin:     refdata.MarginRates{LongByMoney: num(t, r, "MarginRateByMoney"), ShortByMoney: num(t, r, "MarginRateByMoney")},
	}
	ch := futsim.CTPChoices()
	ch.FeeRounding = fee.NoRounding
	sim, err := futsim.New(futsim.Config{Day: day, PreBalance: decimal.NewFromInt(20000000), Rules: rules, Choices: ch})
	if err != nil {
		t.Fatal(err)
	}
	if err := sim.Mark(day, futsim.Quote{Instrument: id, PreSettlement: num(t, r, "PreSettlementPrice"), HasPreSettlement: true}); err != nil {
		t.Fatal(err)
	}
	fr, err := sim.FreezeOf(day, order.Request{Instrument: id, Direction: types.Buy, Offset: types.Open,
		Hedge: types.Speculation, Price: price, Volume: 1})
	if err != nil {
		t.Fatal(err)
	}
	gotM, _ := fr.Margin.Float64()
	if want := flt(t, f.Account, "FrozenMargin"); math.Abs(gotM-want) > 1e-6 {
		t.Errorf("⚠️ 冻结保证金：本库 %v，柜台 %v（挂单价 %s、昨结算价 %v）", gotM, want, price, flt(t, r, "PreSettlementPrice"))
	}
	gotC, _ := fr.Commission.Add(d("0.005")).Float64()
	if want := flt(t, f.Account, "FrozenCommission"); math.Abs(gotC-want) > 1e-6 {
		t.Errorf("⚠️ 冻结手续费：本库（已加 0.005 × 1 手）%v，柜台 %v —— 差不再是 §13 #19 那个常数", gotC, want)
	}
}

// TestMeasuredTickRoundingAgainstCTPQuotes 拿 CTP 夹具行情里的涨跌停价，复现 futsim.MeasuredTickRounding 那张表（评审 20260915 建议）。
//
// 表的出处是 probes.md §12（**快期**行情反解，七个合约）；这里的样本是 **CTP** 行情（柜台自己下发的涨跌停价），两个独立来源。
// 涨跌幅比例同样取自 §12（rb 5% / m 6% / ag 20%），最小变动价位取自 specs-20260908.json —— 都不从这批 CTP 行情里反解。
//
// ⚠️ 判别力：只有「昨结 × 比例」不是 tick 整数倍的样本才分得开取整方向（§12 后两行就是没判别力的样本）。
// 每个交易所至少要有一份「换成另一个方向就对不上」的样本，否则本条只是在复述。
func TestMeasuredTickRoundingAgainstCTPQuotes(t *testing.T) {
	ratios := map[string]string{"rb": "0.05", "m": "0.06", "ag": "0.20"} // probes.md §12
	// ratioPending 是**已知**出现在 CTP 行情里、而比例还没有独立来源的品种 —— 逐个写理由，不是随手加名字的豁免。
	//
	// ⚠️ 它们进 CTP 夹具不是为了这条测试，是别的实验顺带把行情落进来的（dumpSlices 总会附上本合约行情）。
	// 比例不许从这批 CTP 行情里反解（那样就是拿被测数据验自己），而独立来源 —— 快期夹具 —— 目前补不上：
	// 快期 `status` 实验只把**账户上有持仓**的合约写进夹具，`-symbols` 里的只打在控制台上（20260917 试过，
	// 控制台读数不算落盘证据）。⇒ 补法有**两环**，缺一不可（20260917 试到第二环停下）：
	//   ① 让快期探针把 -symbols 的行情也落进夹具（observedQuotes 加上显式点名的合约；试过，夹具里有了）
	//   ② 本条的最小变动价位取自 specs-20260908.json，而那份快照只有 4 个合约 —— 要一份带上这两个合约的新规格快照
	// 两环都齐了再照 §12 反解、登记比例、从这里删掉。
	// ⚠️ 表里的品种**只跳过这一条测试**；表外的新品种照样红。补上比例之后必须从这里删掉 ——
	// 下面那道「已登记比例却还在待登记表里」的检查会红。
	ratioPending := map[string]string{
		"j":  "DCE.j2701：20260917 ctp-feeprobe（§13 #5 造带小数的逐笔手续费）的收尾截面顺带落了它的行情",
		"MA": "CZCE.MA701：20260917 夜 §13 #21 在郑商所的 X1/X2/X0 截面带上了它的行情（ctp-slices-20260918*）",
		"i":  "DCE.i2701 / i2705：§13 #24 的事前登记样本（ctp-status-20260921-2）。按快期反解的 9%（kq_facts 22）五个候选都对不上，事后看像 6% —— 比例在 CTP 上没有独立来源，不许从这批行情反解",
	}
	// tickPending 是**合约**的最小变动价位不在规格快照（specs-20260908.json，只有少数合约）里的样本 —— 逐个写理由。
	// ⚠️ 不从同品种别的月份借（那是推得），也不用控制台读数顶（不算落盘证据）；补上规格来源之后删掉。
	tickPending := map[string]string{
		"DCE.m2709": "§13 #24 的判别样本（ctp-status-20260921-2：昨结 3133 × 6% ⇒ 柜台 3320 / 2946，只有往里收对上）。判定已记在 state.md 的 #24 登记块；规格来源补上后改进 knownDivergence",
	}
	tickSeen := map[string]bool{}
	for p := range ratioPending {
		if _, dup := ratios[p]; dup {
			t.Errorf("⚠️ %s 已经登记了比例，却还在 ratioPending 里 —— 豁免表烂在原地，删掉那一条", p)
		}
	}
	pendingSeen := map[string]bool{}
	// knownDivergence 是**已登记**的分歧样本（合约/交易日）：本库的取整方向与柜台对不上，而规则的修正还没落地。
	// ⚠️ 不是跳过：柜台值与本库值**两边都钉死**，任何一边变了都红；修好之后这一条要删（下面的反方向检查会逼它）。
	type divergence struct{ counterUp, counterLo, libUp, libLo, why string }
	knownDivergence := map[string]divergence{
		"DCE.m2701/20260921": {"3634", "3224", "3635", "3223",
			"§13 #24：昨结 3429 × 6% = 3634.74 / 3223.26。柜台往里收（涨停向下、跌停向上），本库按大商所四舍五入。" +
				"CTP 上大商所第一个能分开两者的样本；快期 20260909 m2701 昨结 3415 早已给出同样的形状（kq_facts 22 当时的候选集里没有「往里收」）"},
	}
	divergenceSeen := map[string]bool{}
	other := map[types.Exchange]refdata.TickRounding{types.SHFE: refdata.TickHalfUp, types.DCE: refdata.TickFloor}
	table := futsim.MeasuredTickRounding()

	type sample struct{ sym, day string }
	seen := map[sample]bool{}
	discriminating := map[types.Exchange]int{}
	n := 0
	for name, f := range loadCTP(t) {
		for sym, q := range f.Quotes {
			day, _ := q["TradingDay"].(string)
			k := sample{sym, day}
			if seen[k] {
				continue
			}
			seen[k] = true
			td, err := types.ParseTradingDay(day)
			if err != nil {
				t.Fatalf("%s %s：%v", name, sym, err)
			}
			id, err := types.ParseSymbol(sym, td)
			if err != nil {
				t.Fatal(err)
			}
			ratio, ok := ratios[id.Product]
			if why, pending := ratioPending[id.Product]; !ok && pending {
				if !pendingSeen[id.Product] {
					t.Logf("ⓘ %s 比例待登记，本条跳过：%s", sym, why)
					pendingSeen[id.Product] = true
				}
				continue
			}
			if !ok {
				t.Errorf("⚠️ 行情里出现了 %s，而 probes.md §12 没有它的涨跌幅比例 —— 新样本要先登记比例，不许从这批行情反解", sym)
				continue
			}
			rounding, ok := table[id.Exchange]
			if !ok {
				t.Errorf("⚠️ %s 的交易所 %s 不在 MeasuredTickRounding 里", sym, id.Exchange)
				continue
			}
			if why, pending := tickPending[sym]; pending {
				if !tickSeen[sym] {
					t.Logf("ⓘ %s 最小变动价位待登记，本条跳过：%s", sym, why)
					tickSeen[sym] = true
				}
				continue
			}
			tick := specTick(t, sym)
			inst := refdata.Instrument{ID: id, PriceTick: tick, PriceLimitRatio: decimal.RequireFromString(ratio), HasPriceLimitRatio: true}
			pre := num(t, q, "PreSettlementPrice")
			up, lo, ok := inst.PriceLimits(pre, true, rounding)
			if !ok {
				t.Fatalf("%s：PriceLimits 没给出结果", sym)
			}
			wantUp, wantLo := num(t, q, "UpperLimitPrice"), num(t, q, "LowerLimitPrice")
			if dv, known := knownDivergence[sym+"/"+day]; known {
				divergenceSeen[sym+"/"+day] = true
				d := decimal.RequireFromString
				if !wantUp.Equal(d(dv.counterUp)) || !wantLo.Equal(d(dv.counterLo)) || !up.Equal(d(dv.libUp)) || !lo.Equal(d(dv.libLo)) {
					t.Errorf("⚠️ 已登记分歧 %s %s 的形状变了：柜台 %s / %s（登记 %s / %s），本库 %s / %s（登记 %s / %s）—— 修好了就删掉这一条，否则重看（%s）",
						sym, day, wantUp, wantLo, dv.counterUp, dv.counterLo, up, lo, dv.libUp, dv.libLo, dv.why)
				} else {
					t.Logf("ⓘ 已登记分歧 %s %s：%s", sym, day, dv.why)
				}
			} else if !up.Equal(wantUp) || !lo.Equal(wantLo) {
				t.Errorf("⚠️ %s 交易日 %s：昨结 %s × %s 按 %v 得 %s / %s，柜台 %s / %s", sym, day, pre, ratio, rounding, up, lo, wantUp, wantLo)
			}
			if u2, l2, _ := inst.PriceLimits(pre, true, other[id.Exchange]); !u2.Equal(wantUp) || !l2.Equal(wantLo) {
				discriminating[id.Exchange]++
			}
			n++
		}
	}
	// 反方向：豁免表里的品种必须真在语料里出现过。否则那一条已经不豁免任何东西，
	// 而表外看起来仍像「有个已知缺口」—— 删夹具或改名时它就这样烂在原地（评审 20260917 nit）。
	for k, why := range tickPending {
		if !tickSeen[k] {
			t.Errorf("⚠️ tickPending 里的 %s 在语料里没出现 —— 删掉那一条（%s）", k, why)
		}
	}
	for k, dv := range knownDivergence {
		if !divergenceSeen[k] {
			t.Errorf("⚠️ 已登记分歧 %s 在语料里没出现 —— 删掉那一条（%s）", k, dv.why)
		}
	}
	for p, why := range ratioPending {
		if !pendingSeen[p] {
			t.Errorf("⚠️ ratioPending 里的 %s 在 CTP 行情语料里一次都没出现 —— 豁免的品种已经不在语料里，删掉那一条（理由原写：%s）", p, why)
		}
	}
	if n < 7 {
		t.Fatalf("⚠️ 只找到 %d 份不重复的行情样本（下界 7）—— 夹具或读法变了", n)
	}
	for _, ex := range []types.Exchange{types.SHFE, types.DCE} {
		if discriminating[ex] == 0 {
			t.Errorf("⚠️ %s 没有一份样本在另一个取整方向下对不上 —— 本条对它的取整方向没有判别力", ex)
		}
	}
	t.Logf("ⓘ %d 份行情样本；有判别力的：上期所 %d、大商所 %d", n, discriminating[types.SHFE], discriminating[types.DCE])
}

// specTick 从天勤规格快照取最小变动价位。
func specTick(t *testing.T, symbol string) decimal.Decimal {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash("../../testdata/refdata/specs-20260908.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Specs []struct {
			Instrument string          `json:"instrument"`
			PriceTick  json.RawMessage `json:"price_tick"`
		} `json:"specs"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, sp := range doc.Specs {
		if sp.Instrument == symbol {
			return decimal.RequireFromString(string(sp.PriceTick))
		}
	}
	t.Fatalf("⚠️ 规格快照里没有 %s 的最小变动价位", symbol)
	return decimal.Zero
}

// TestFacadePlaceAgainstCTPFrozen 用门面的 Place 按 CTP 预设复现三份「挂着时」的截面：开仓挂单、平昨挂单、平今挂单。
//
// 每份比：账户 FrozenMargin、FrozenCommission，以及「可用 − 结存」（= −占用 − 冻结；这几份持仓盈亏都不为正，§13 #17 的排除项为 0）。
// 平仓挂单另比持仓侧冻住的手数（门面 Place 返回的 Frozen）与多头记录的 ShortFrozen。
// ⚠️ 手续费与「可用 − 结存」都按声明费率算，比柜台少恰好 0.005 × 手数（§13 #19）—— 钉的是这个差。
// ⚠️ 挂单价从记录的委托额反推（夹具不带委托明细）；涨跌幅比例 5% 取 probes §12；报单时刻取截面的 captured_at。
func TestFacadePlaceAgainstCTPFrozen(t *testing.T) {
	fx := loadCTP(t)
	const sym = "SHFE.rb2701"
	d := decimal.RequireFromString
	day := func(s string) types.TradingDay {
		td, err := types.ParseTradingDay(s)
		if err != nil {
			t.Fatal(err)
		}
		return td
	}
	id, err := types.ParseSymbol(sym, day("20260910"))
	if err != nil {
		t.Fatal(err)
	}
	mult, _ := specMultiplier(t, sym, id)
	sessions := []refdata.Session{
		{Start: refdata.MustClockTime(9, 0, 0), End: refdata.MustClockTime(10, 15, 0)},
		{Start: refdata.MustClockTime(10, 30, 0), End: refdata.MustClockTime(11, 30, 0)},
		{Start: refdata.MustClockTime(13, 30, 0), End: refdata.MustClockTime(15, 0, 0)},
	}
	cal, err := refdata.NewCalendar([]types.TradingDay{day("20260909"), day("20260910"), day("20260911"), day("20260914")},
		[]refdata.SessionTable{{Exchange: types.SHFE, Product: "rb", Day: sessions,
			Night: []refdata.Session{{Start: refdata.MustClockTime(21, 0, 0), End: refdata.MustClockTime(23, 0, 0)}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rules := oneInstrumentRules{
		inst: refdata.Instrument{ID: id, VolumeMultiple: mult, PriceTick: decimal.NewFromInt(1), IsTrading: true,
			PositionDateType:    refdata.UseHistory, // measured-rules-20260909.json（快期结算行为）；平今 / 平昨两笔都显式给了今昨
			MinLimitOrderVolume: 1, MaxLimitOrderVolume: 500, PriceLimitRatio: d("0.05"), HasPriceLimitRatio: true},
		commission: refdata.CommissionRates{OpenByMoney: d("0.0001"), CloseByMoney: d("0.0001"), CloseTodayByMoney: d("0.0001")},
		margin:     refdata.MarginRates{LongByMoney: d("0.16"), ShortByMoney: d("0.16")},
	}
	newSim := func(td types.TradingDay) *futsim.Simulator {
		ch := futsim.CTPChoices()
		ch.FeeRounding = fee.NoRounding
		s, err := futsim.New(futsim.Config{Day: td, PreBalance: decimal.NewFromInt(20000000), Rules: rules, Choices: ch,
			Calendar: cal, TickRounding: futsim.MeasuredTickRounding(), PositionLimits: map[types.InstrumentID]int{id: 100}})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	mark := func(s *futsim.Simulator, td types.TradingDay, last, pre float64) {
		t.Helper()
		if err := s.Mark(td, futsim.Quote{Instrument: id, Last: decimal.NewFromFloat(last), HasLast: true,
			PreSettlement: decimal.NewFromFloat(pre), HasPreSettlement: true}); err != nil {
			t.Fatal(err)
		}
	}
	at := func(f ctpFixture) time.Time {
		tm, err := time.Parse(time.RFC3339, f.CapturedAt)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	near := func(a, b float64) bool { return math.Abs(a-b) <= 1e-6 }

	for _, c := range []struct {
		file, rec string
		dir       types.Direction
		off       types.Offset
		amountKey string
		frozenKey string // 持仓记录里冻住手数的字段；开仓挂单不比
		wantToday int
		wantHis   int
		setup     func() (*futsim.Simulator, types.TradingDay)
	}{
		{"ctp-frozen-20260910.json", "SHFE.rb2701/1", types.Buy, types.Open, "LongFrozenAmount", "", 0, 0,
			func() (*futsim.Simulator, types.TradingDay) {
				s := newSim(day("20260910"))
				mark(s, day("20260910"), 3146, 3164)
				return s, day("20260910")
			}},
		{"ctp-frozen-20260911.json", "SHFE.rb2701/2", types.Sell, types.CloseYesterday, "ShortFrozenAmount", "ShortFrozen", 0, 1,
			func() (*futsim.Simulator, types.TradingDay) {
				// 20260910 开多 1 @3148（ctp-status-20260910-7 的 OpenCost 31480），按 3147 结算（次日记录的 PreSettlementPrice）
				s := newSim(day("20260910"))
				mark(s, day("20260910"), 3148, 3164)
				if err := s.ApplyTrade(day("20260910"), match.Trade{Instrument: id, Direction: types.Buy, Offset: types.Open,
					Hedge: types.Speculation, Price: d("3148"), Volume: 1}); err != nil {
					t.Fatal(err)
				}
				if err := s.Settle(day("20260910"), map[types.InstrumentID]decimal.Decimal{id: d("3147")}, day("20260911")); err != nil {
					t.Fatal(err)
				}
				mark(s, day("20260911"), 3137, 3147)
				return s, day("20260911")
			}},
		{"ctp-frozen-20260914.json", "SHFE.rb2701/2/1", types.Sell, types.CloseToday, "ShortFrozenAmount", "ShortFrozen", 1, 0,
			func() (*futsim.Simulator, types.TradingDay) {
				// 当日开多 1 @3105（记录 OpenCost 31050）
				s := newSim(day("20260914"))
				mark(s, day("20260914"), 3105, 3117)
				if err := s.ApplyTrade(day("20260914"), match.Trade{Instrument: id, Direction: types.Buy, Offset: types.Open,
					Hedge: types.Speculation, Price: d("3105"), Volume: 1}); err != nil {
					t.Fatal(err)
				}
				return s, day("20260914")
			}},
	} {
		f, ok := fx[c.file]
		if !ok {
			t.Fatalf("⚠️ 缺夹具 %s", c.file)
		}
		r := f.Positions[c.rec]
		price := num(t, r, c.amountKey).Div(mult)
		s, td := c.setup()
		fr, err := s.Place(td, at(f), "o", order.Request{Instrument: id, Direction: c.dir, Offset: c.off,
			Hedge: types.Speculation, Price: price, Volume: 1})
		if err != nil {
			t.Errorf("%s：挂单 %s @%s 失败：%v", c.file, c.off, price, err)
			continue
		}
		a := s.Account()
		gotM, _ := a.FrozenMargin.Float64()
		gotC, _ := a.FrozenCommission.Add(d("0.005")).Float64()
		if want := flt(t, f.Account, "FrozenMargin"); !near(gotM, want) {
			t.Errorf("⚠️ %s 冻结保证金：本库 %v，柜台 %v", c.file, gotM, want)
		}
		if want := flt(t, f.Account, "FrozenCommission"); !near(gotC, want) {
			t.Errorf("⚠️ %s 冻结手续费（已加 0.005）：本库 %v，柜台 %v", c.file, gotC, want)
		}
		gotGap, _ := a.Available.Sub(a.Balance).Sub(d("0.005")).Float64()
		if want := flt(t, f.Account, "Available") - flt(t, f.Account, "Balance"); !near(gotGap, want) {
			t.Errorf("⚠️ %s 可用 − 结存（已减 0.005）：本库 %v，柜台 %v", c.file, gotGap, want)
		}
		if c.frozenKey != "" {
			if fr.VolumeToday != c.wantToday || fr.VolumeHistory != c.wantHis || int(flt(t, r, c.frozenKey)) != c.wantToday+c.wantHis {
				t.Errorf("⚠️ %s 持仓侧冻住：本库 今 %d / 昨 %d，柜台 %s = %v", c.file, fr.VolumeToday, fr.VolumeHistory, c.frozenKey, flt(t, r, c.frozenKey))
			}
		}
	}
}
