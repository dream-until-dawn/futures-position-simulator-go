package fixture

import (
	"sort"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// marginRates 是各品种的保证金率。
//
// ⚠️ 出处：probes.md §7.2 的实测（从每手保证金反解，跨合约跨交易所互验）。
// **至少三档**（rb/m 7%、i/cu 11%、ag 22%）—— 而我曾经用三个同为 7% 的样本
// 得出过「全局统一简化费率」这个相反结论（silent-risks.md）。
// 那次的教训是：**同值的样本不构成「统一」的证据**。
var marginRates = map[string]string{
	"rb": "0.07", "m": "0.07",
	"i": "0.11", "cu": "0.11",
	"ag": "0.22",
}

func ratesFor(product string) (refdata.MarginRates, bool) {
	r, ok := marginRates[product]
	if !ok {
		return refdata.MarginRates{}, false
	}
	d := decimal.RequireFromString(r)
	return refdata.MarginRates{
		LongByMoney: d, LongByVolume: decimal.Zero,
		ShortByMoney: d, ShortByVolume: decimal.Zero,
		CompanyAddOn: decimal.Zero,
	}, true
}

// TestMarginAgainstFixturePositions 把保证金对拍到**夹具里的每一个持仓**。
//
// 与 conformance 包里那条五个数的对拍不同，这一条的输入全部来自夹具本身：
// 持仓从成交重放出来，昨结算价从行情读出来 —— 一份自足的证据。
//
// ⚠️ 判别力在哪里、不在哪里，都要说清楚：
//
//	有   跨品种（三档费率）、跨交易所、以及**多手**持仓上的线性
//	     ——「每手保证金」是在 1 手的样本上量的，3 手是一次真的外推
//	没有 单向大边（本口子未启用，kq_facts 5）、
//	     今昨基准之分（本批全是今仓，两个候选同值）
func TestMarginAgainstFixturePositions(t *testing.T) {
	all := loadAll(t)
	var fields []conformance.Field
	products, multiLot, withQuote := map[string]bool{}, 0, 0
	skippedHistory := 0
	var skippedNoQuote []string

	for _, f := range all {
		for _, sym := range f.Symbols() {
			trades := f.TradesOf(sym)
			if len(trades) == 0 {
				continue
			}
			pre, hasPre := f.PreSettlement(sym)
			if !hasPre {
				skippedNoQuote = append(skippedNoQuote, f.Path)
				continue
			}
			withQuote++
			p, err := Replay(trades[0].Instrument, types.Speculation, f.TradingDay, trades)
			if err != nil {
				continue
			}
			product, _ := splitProduct(trades[0].Instrument.Product)
			rates, ok := ratesFor(product)
			if !ok {
				t.Errorf("⚠️ 品种 %s 没有登记保证金率", product)
				continue
			}
			mult, ok := multipliers[sym]
			if !ok {
				t.Errorf("⚠️ %s 没有登记乘数", sym)
				continue
			}
			long, short, err := MarginOf(p, rates, decimal.RequireFromString(mult), pre,
				false, // ⚠️ 单向大边：实测本口子未启用（kq_facts 5）
				margin.PreSettleAll, margin.ByInstrument)
			if IsNoPosition(err) {
				// 空仓 —— 柜台给 "-"，本库也说「没有」，这里不构造字段。
				// ⚠️ 两边都「没有」是一致，但它是空洞的一致，
				// 在 view 那条全量对拍里已经按 Untriggered 记过账，不重复计。
				continue
			}
			if err != nil {
				t.Errorf("⚠️ %s %s 算保证金失败：%v", f.Path, sym, err)
				continue
			}
			products[product] = true
			for _, side := range []struct {
				name string
				lib  decimal.Decimal
			}{{"long", long}, {"short", short}} {
				// ⚠️ 这一侧有昨仓就跳过，**并且记数**。
				//
				// 保证金是「每手 × 手数」，而手数来自重放，重放只回放**当日**成交 ——
				// 昨仓那几手的开仓单在前一交易日的夹具里。
				// 于是本库算的是今仓那几手的保证金，柜台算的是今+昨全部，
				// 差异指向的是**夹具的边界**，不是保证金公式错。
				//
				// ⚠️ 20260909 夜盘第一次出现「同一合约既有昨仓、又有当日成交」，
				// 这条当场红了 6 处 —— 此前样本里要么全是今仓、
				// 要么有昨仓那天没成交，所以这个洞一直没露头。
				if h := f.Positions[sym]["volume_"+side.name+"_his"]; !h.Absent &&
					!h.IsText && h.Number.IsPositive() {
					skippedHistory++
					continue
				}
				o := f.Positions[sym]["margin_"+side.name]
				fields = append(fields, conformance.Field{
					Name:          f.Path + " " + sym + ".margin_" + side.name,
					Library:       side.lib,
					LibraryAbsent: side.lib.IsZero(),
					Oracle:        o.Number,
					OracleAbsent:  o.Absent,
					Triggered:     !side.lib.IsZero(),
				})
			}
			if s, _ := p.Side(types.Buy); s.Volume() > 1 {
				multiLot++
			}
		}
	}

	sort.Strings(skippedNoQuote)
	t.Logf("带行情的持仓截面 %d 个，覆盖品种 %d 个，其中多手持仓 %d 个",
		withQuote, len(products), multiLot)
	if len(skippedNoQuote) > 0 {
		t.Logf("ⓘ 因缺行情跳过 %d 个截面 —— 那些夹具早于「行情进夹具」这次改动，"+
			"缺席本身是它们的记录，不去别处找一个昨结算价补上", len(skippedNoQuote))
	}

	// —— 判别力守卫 ——
	//
	// ⚠️ 多手持仓是这一条的关键：「每手保证金」是在 1 手的样本上量的，
	// 3 手是一次真的外推。没有多手样本时，这条测试只是把量到的数除以 1 再乘回 1。
	if multiLot == 0 {
		t.Fatalf("⚠️ 一个多手持仓都没有 —— " +
			"「每手保证金 × 手数」这条线性关系一次都没被考验过")
	}
	if len(fields) < 4 {
		t.Fatalf("只比了 %d 个字段 —— 太少", len(fields))
	}
	// ⚠️ 跳掉的要报出来，且卡的是不变式而不是绝对值：
	// 明天结算之后昨仓会**合法地**变多，一个卡死的数字只会天天要人去调。
	t.Logf("ⓘ 因该侧有昨仓而跳过 %d 处 —— 手数来自重放，而重放只回放当日成交",
		skippedHistory)
	if skippedHistory > len(fields) {
		t.Errorf("⚠️ 跳过 %d 处，而真正比过的只有 %d 个字段 —— "+
			"这条已经主要在跳过而不是在对拍。该给带昨仓的方向接上 Carry 了",
			skippedHistory, len(fields))
	}

	r := conformance.Classify("夹具里的持仓·保证金", fields,
		decimal.RequireFromString("0.0000001"))
	t.Logf("  对得上 %d，未触发 %d，失败 %d",
		r.Counts[conformance.Matched], r.Counts[conformance.Untriggered],
		r.Counts[conformance.Failed])
	if n := r.Counts[conformance.Failed]; n != 0 {
		t.Errorf("⚠️ 保证金对不上 %d 处：\n%s", n, r.Summary())
	}
	if r.Counts[conformance.Matched] == 0 {
		t.Error("⚠️ 一个「对得上且被触发过」都没有 —— 全是空仓，什么都没验证")
	}

	// ⚠️ 明说测不到什么，免得「全对」被读成「保证金这块完了」。
	t.Log("⚠️ 本条**测不到**：单向大边（本口子未启用，kq_facts 5）、" +
		"今昨基准之分（本批全是今仓，两个候选同值）")
}

// TestMarginRatesHaveMoreThanOneTier 是那次错误结论的守卫。
//
// ⚠️ 我曾用三个恰好同为 7% 的样本得出「快期用全局统一简化费率」——
// 那是错的（i/cu 11%、ag 22%）。同值的样本不构成「统一」的证据。
func TestMarginRatesHaveMoreThanOneTier(t *testing.T) {
	tiers := map[string]bool{}
	for _, r := range marginRates {
		tiers[r] = true
	}
	if len(tiers) < 3 {
		t.Errorf("⚠️ 保证金率只有 %d 档 —— 实测至少三档（7%%/11%%/22%%）。"+
			"档数塌成一档时，「跨品种都对」只是同一次验证做了很多遍", len(tiers))
	}
	// 反向：也不该每个品种一档 —— rb 与 m 实测同为 7%，
	// 若表里每个品种都不同，说明有人按品种各填了一个数而不是照实测填。
	if len(tiers) == len(marginRates) {
		t.Errorf("⚠️ %d 个品种恰好 %d 档 —— 实测里 rb 与 m 同为 7%%、i 与 cu 同为 11%%，"+
			"每品种一档说明这张表不是照实测填的", len(marginRates), len(tiers))
	}
}

// TestMarginNoPositionIsAnError 断言「无持仓」走的是 error 而不是 0。
//
// ⚠️ 这条路径此前**没有覆盖**：破坏验证把 errNoPosition 改成 nil，
// 全部测试照样绿 —— 因为 view.PositionOf 自己的空仓判断先生效了，
// 传进去的那个 0 根本没被用上。
// 而 MarginOf 是个公开函数，别的调用方不一定有那层保护。
//
// 「空仓没有保证金」与「保证金是 0」在数上分不开，
// 而后者会让一个空账户看起来和一个满仓账户一样安全。
func TestMarginNoPositionIsAnError(t *testing.T) {
	inst := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
	p, err := position.New(inst, types.Speculation, types.NewTradingDay(2026, 9, 8))
	if err != nil {
		t.Fatal(err)
	}
	rates, _ := ratesFor("rb")
	long, short, err := MarginOf(p, rates, decimal.NewFromInt(10),
		decimal.RequireFromString("3158"), false, margin.PreSettleAll, margin.ByInstrument)
	if err == nil {
		t.Fatalf("⚠️ 空仓算保证金得到 %s/%s 而没有报错 —— "+
			"「空仓没有保证金」与「保证金是 0」在数上分不开，"+
			"而后者会让空账户看起来和满仓账户一样安全", long, short)
	}
	if !IsNoPosition(err) {
		t.Errorf("报错了但不是「无持仓」：%v", err)
	}
	// 反向：有持仓时不该报这个错，否则上面那条可以靠「一律报错」通过。
	if err := p.Open(types.Buy, types.NewTradingDay(2026, 9, 8),
		decimal.RequireFromString("3151"), 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := MarginOf(p, rates, decimal.NewFromInt(10),
		decimal.RequireFromString("3158"), false,
		margin.PreSettleAll, margin.ByInstrument); err != nil {
		t.Errorf("有持仓时不该报错：%v", err)
	}
}

// TestMarginRefusesMissingInputs 断言乘数与昨结算价缺失都被拒。
//
// ⚠️ 两个都是「回落到 0 会让金额变成 0，而 0 看起来完全合理」那一类。
func TestMarginRefusesMissingInputs(t *testing.T) {
	inst := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
	day := types.NewTradingDay(2026, 9, 8)
	mk := func() *position.Position {
		p, err := position.New(inst, types.Speculation, day)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Open(types.Buy, day, decimal.RequireFromString("3151"), 1); err != nil {
			t.Fatal(err)
		}
		return p
	}
	rates, _ := ratesFor("rb")
	ten := decimal.NewFromInt(10)
	pre := decimal.RequireFromString("3158")
	for _, c := range []struct {
		name            string
		mult, preSettle decimal.Decimal
	}{
		{"乘数为零", decimal.Zero, pre},
		{"乘数为负", decimal.NewFromInt(-1), pre},
		{"昨结算价为零", ten, decimal.Zero},
		{"昨结算价为负", ten, decimal.NewFromInt(-1)},
	} {
		if _, _, err := MarginOf(mk(), rates, c.mult, c.preSettle, false,
			margin.PreSettleAll, margin.ByInstrument); err == nil {
			t.Errorf("⚠️ %s 应当报错 —— 回落到 0 会让保证金变成 0，"+
				"而「不占保证金」在数上完全合理", c.name)
		}
	}
	// 反向对照：两个都正常时不报错。
	if _, _, err := MarginOf(mk(), rates, ten, pre, false,
		margin.PreSettleAll, margin.ByInstrument); err != nil {
		t.Errorf("输入正常时不该报错：%v", err)
	}
}
