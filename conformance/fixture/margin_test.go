package fixture

import (
	"sort"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

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
	var skippedNoQuote []string

	for _, f := range all {
		for _, sym := range f.Symbols() {
			trades := f.TradesOf(sym)
			if len(trades) == 0 {
				continue
			}
			// ⚠️ 只比夹具**自己**报了昨结算价的截面：占用依赖它，借来的那一份门面不给（HasMargin 为假）
			if _, hasPre := f.PreSettlement(sym); !hasPre {
				skippedNoQuote = append(skippedNoQuote, f.Path)
				continue
			}
			withQuote++
			// ⚠️ 保证金用夹具**自己**报的昨结算价（上面缺就跳过了）；持仓走 positionOf（带昨仓结转、否则当日重放）——
			// 当日重放那一支若借了同日兄弟夹具的昨结算价，也只当门面前提，这里算保证金用的仍是上面的 pre（design.md §11 决策点 3）
			r, _, err := positionOf(t, all, f, sym)
			if err != nil {
				t.Errorf("⚠️ %s %s 重放失败：%v", f.Path, sym, err)
				continue
			}
			product, _ := splitProduct(trades[0].Instrument.Product)
			if _, ok := ratesFor(product); !ok {
				t.Errorf("⚠️ 品种 %s 没有登记保证金率", product)
				continue
			}
			if _, ok := multipliers[sym]; !ok {
				t.Errorf("⚠️ %s 没有登记乘数", sym)
				continue
			}
			// F8：逐方向占用由**门面**给（Replayed），本包不再自己把持仓翻译成 margin.Leg。
			// ⚠️ 空仓时 HasMargin 为假 —— 柜台给 "-"、本库说「没有」，两边都「没有」是空洞的一致，
			// 在 view 那条全量对拍里已按 Untriggered 记过账，这里不构造字段。
			// ⚠️ 昨结算价借自同日兄弟夹具时 HasMargin 也为假（借来的数不进依赖它的比对，§11 决策点 3 / §12）——
			// 本条上面已按「夹具自己报了昨结算价」筛过，所以走不到那一支。
			if !r.HasMargin {
				continue
			}
			long, short := r.MarginLong, r.MarginShort
			p := r.Position
			products[product] = true
			for _, side := range []struct {
				name string
				lib  decimal.Decimal
			}{{"long", long}, {"short", short}} {
				// ⚠️ F7c 之前这里「这一侧有昨仓就跳过并记数」：手数来自只回放当日成交的旧重放，昨仓那几手在前一交易日，
				// 本库算今仓的、柜台算今 + 昨 —— 差异指向夹具边界。F7c 起带昨仓的合约走结转（positionOf），持仓完整，
				// 那一侧照样比：全量扫描里「去掉这道跳过」的破坏 81 从红变绿，就是这些方向已经对得上的证据，于是删掉跳过、接回比对
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
		"今昨基准之分（本条只按快期实测的 PreSettleAll 算、不拿候选互比；F7c 起样本里有了昨仓方向，「全是今仓」不再成立，" +
		"两个候选的判别力由 TestRebuildAccountFieldByField 承担，见破坏 518）")
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
