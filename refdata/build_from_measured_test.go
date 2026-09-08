package refdata_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// 实测出来的规则数据，全部有出处，全部**不是猜的**。
//
//	乘数 / 最小变动价位 / 手数上下限   天勤合约字典（cmd/refdata-sync -specs）
//	保证金率                          probes.md §7.2，从每手保证金反解，至少三档
//	手续费率                          probes.md §10.2，分类 + 跨月份外推
//	涨跌幅比例                        probes.md §12，从涨跌停价反解，两家取整方向不同
//
// ⚠️ **唯独 PositionDateType 没有**。它今天测出了行为
// （上期所滚今昨仓、大商所不滚，kq_facts 24），但那是**一个口子上的行为**，
// 而 refdata 快照要的是**规则**。单一来源不够。
var measured = []struct {
	sym        string
	multiplier string
	tick       string
	limitRatio string
	marginRate string
	feeByMoney string
	feeByLot   string
}{
	{"SHFE.rb2701", "10", "1", "0.05", "0.07", "0.00001", "0"},
	{"SHFE.cu2701", "5", "10", "0.09", "0.11", "0.00005", "0"},
	{"DCE.m2701", "10", "1", "0.06", "0.07", "0", "1.5"},
	{"DCE.i2701", "100", "0.5", "0.09", "0.11", "0.0001", "0"},
	{"SHFE.ag2702", "15", "1", "0.2", "0.22", "0.00005", "0"},
}

// TestBuildFromMeasuredDataStopsAtWhatIsMissing 拿**全部实测数据**去建一份真快照，
// 看 Builder 到底卡在哪一项。
//
// ⚠️ 它期望 Build **失败**，而这不是「测试期望失败」的花招：
// `refdata.Builder` 的零值报错是本项目的一条设计约束 ——
//
//	一个「差不多能用」的规格会在平今平昨和大边上静默算错
//
// 而一条约束只有被**真实数据**撞过一次，才知道它拦的是不是要拦的那个。
// 在此之前它只被合成数据测过，而合成数据是照着约束写的。
//
// ⚠️ 哪天 PositionDateType 有了第二个来源，这条会以「竟然建成了」的形式红 ——
// 那是好消息，而好消息同样需要有人被通知到。
func TestBuildFromMeasuredDataStopsAtWhatIsMissing(t *testing.T) {
	// 用规格文件核对手抄的乘数与 tick —— ⚠️ 两个独立来源，不是抄同一份。
	specs := loadSpecFile(t)
	b := refdata.NewBuilder(20260908)
	day := types.NewTradingDay(2026, 9, 8)

	for _, m := range measured {
		id, err := types.ParseSymbol(m.sym, day)
		if err != nil {
			t.Fatal(err)
		}
		mult := decimal.RequireFromString(m.multiplier)
		tick := decimal.RequireFromString(m.tick)
		// ⚠️ 到期日**从规格文件取**，不再硬填。
		// 上一版写死 20270115，而那是照 rb2701 抄的 —— 用在 m2701 上就错了
		// （大商所是 14 号）。一个「反正 Builder 不检查它」的硬填，
		// 会在检查它的那天才暴露，而那天离写下它已经很远。
		expire := types.NewTradingDay(2027, 1, 15)
		if s, ok := specs[m.sym]; ok {
			if !s.mult.Equal(mult) || !s.tick.Equal(tick) {
				t.Errorf("⚠️ %s 的乘数/tick：手抄 %s/%s，上游字典 %s/%s —— "+
					"两条独立通路对不上", m.sym, mult, tick, s.mult, s.tick)
			}
			expire = s.expire
		} else {
			t.Errorf("⚠️ %s 不在规格文件里 —— 到期日只能回落到硬填值", m.sym)
		}
		b.AddInstrument(refdata.Instrument{
			ID:             id,
			VolumeMultiple: mult,
			PriceTick:      tick,
			// ⚠️ PositionDateType **刻意留零值**：这正是要看 Builder 拦不拦的那一项。
			// 填一个「上期所就是 UseHistory」的猜测，会让整条测试失去意义 ——
			// 而那也正是这条设计约束要防的动作。
			IsTrading:          true,
			ExpireDate:         expire,
			PriceLimitRatio:    decimal.RequireFromString(m.limitRatio),
			HasPriceLimitRatio: true,
		})
		rate := decimal.RequireFromString(m.marginRate)
		b.AddMarginRates(id, types.Speculation, refdata.MarginRates{
			LongByMoney: rate, ShortByMoney: rate,
			LongByVolume: decimal.Zero, ShortByVolume: decimal.Zero,
			CompanyAddOn: decimal.Zero,
		})
		byMoney := decimal.RequireFromString(m.feeByMoney)
		byLot := decimal.RequireFromString(m.feeByLot)
		b.AddCommissionRates(id, types.Speculation, refdata.CommissionRates{
			OpenByMoney: byMoney, OpenByVolume: byLot,
			CloseByMoney: byMoney, CloseByVolume: byLot,
			CloseTodayByMoney: byMoney, CloseTodayByVolume: byLot,
		})
	}

	_, err := b.Build()
	if err == nil {
		t.Fatal("⚠️ **快照建成了** —— 而 PositionDateType 是零值。" +
			"要么 Builder 的零值拒绝失效了，要么这条测试的前提变了。" +
			"⚠️ 若是 PositionDateType 真的有了来源，那是好消息，" +
			"但这条测试要跟着改：把它填上，再看还差什么")
	}
	// ⚠️ 卡在哪一项要**点名**：一句笼统的「规格不全」，
	// 与「我们其实还差三样」在读的时候完全一样。
	if !strings.Contains(err.Error(), "PositionDateType") {
		t.Errorf("⚠️ Build 失败了，但报的不是 PositionDateType：%v —— "+
			"那说明还有**别的**缺口，先查清楚它是什么", err)
	}
	t.Logf("Builder 如期拦下，报的是：%v", err)
	t.Logf("ⓘ 实测已备齐：乘数 / 最小变动价位 / 到期日 / 保证金率 / 手续费率 / 涨跌幅比例（%d 个合约）",
		len(measured))
	// ⚠️ 到期日不是硬填的了 —— 而两家交易所的到期日**不同**（上期所 15 号、
	// 大商所 14 号），所以「照一个合约抄一个数」在另一家上必然错。
	// 这条把那件事钉住：若哪天规格文件里两家变成同一天，先查数据。
	shfe := specs["SHFE.rb2701"].expire
	dce := specs["DCE.m2701"].expire
	if shfe == dce {
		t.Errorf("⚠️ 上期所与大商所的到期日都是 %s —— "+
			"实测是 20270115 / 20270114（15 号 / 14 号）。"+
			"两家同天的话，「到期日按合约取」这件事在本样本上就没有判别力了", shfe)
	}
	t.Logf("ⓘ 到期日取自规格文件：rb2701 %s、m2701 %s —— "+
		"⚠️ 两家不同，硬填一个数必然在另一家上错", shfe, dce)
	t.Log("⚠️ 仍缺：PositionDateType（今天测出了**行为**，但单一来源，" +
		"而快照要的是**规则**）；MaxMarginSideAlgorithm（此口子未启用，测不出）")

	// —— 对照：把 PositionDateType 填上，就应当建得成 ——
	//
	// ⚠️ 没有这一条，上面那条可以靠「Builder 一律失败」通过，
	// 而那时它证明的是「Builder 坏了」，不是「我们差一项数据」。
	b2 := refdata.NewBuilder(20260908)
	for _, m := range measured {
		id, _ := types.ParseSymbol(m.sym, day)
		pdt := refdata.NoUseHistory
		if strings.HasPrefix(m.sym, "SHFE.") {
			pdt = refdata.UseHistory
		}
		b2.AddInstrument(refdata.Instrument{
			ID:             id,
			VolumeMultiple: decimal.RequireFromString(m.multiplier),
			PriceTick:      decimal.RequireFromString(m.tick),
			// ⚠️ 这个值**只在对照组里**用，且它是按交易所猜的 ——
			// 而「按交易所硬编码在绝大多数合约上都对，于是错的那几个
			// 不会被测出来」正是 Instrument.Validate 的报错原文。
			// 它出现在这里是为了证明**除它之外都齐了**，不是主张它对。
			PositionDateType:   pdt,
			IsTrading:          true,
			ExpireDate:         specs[m.sym].expire,
			PriceLimitRatio:    decimal.RequireFromString(m.limitRatio),
			HasPriceLimitRatio: true,
		})
		rate := decimal.RequireFromString(m.marginRate)
		b2.AddMarginRates(id, types.Speculation, refdata.MarginRates{
			LongByMoney: rate, ShortByMoney: rate,
			LongByVolume: decimal.Zero, ShortByVolume: decimal.Zero,
			CompanyAddOn: decimal.Zero,
		})
		byMoney := decimal.RequireFromString(m.feeByMoney)
		byLot := decimal.RequireFromString(m.feeByLot)
		b2.AddCommissionRates(id, types.Speculation, refdata.CommissionRates{
			OpenByMoney: byMoney, OpenByVolume: byLot,
			CloseByMoney: byMoney, CloseByVolume: byLot,
			CloseTodayByMoney: byMoney, CloseTodayByVolume: byLot,
		})
	}
	snap, err := b2.Build()
	if err != nil {
		t.Fatalf("⚠️ 只差 PositionDateType 这句话不成立 —— 填上之后仍然建不成：%v\n"+
			"那说明还有别的缺口没被上面那条点出来", err)
	}
	t.Logf("ⓘ 对照组：把 PositionDateType 填上（**按交易所猜的**）之后，"+
		"快照建成，%d 个合约 —— 也就是**除它之外的料都齐了**", len(measured))

	// 顺带验一次涨跌停推算 —— 那是本次新测出来的数据的第一次使用。
	inst, err := snap.Instrument(mustID(t, "SHFE.rb2701", day))
	if err != nil {
		t.Fatalf("快照里没有 rb2701：%v", err)
	}
	// ⚠️ 取整方向按实测给：上期所向下取整（probes.md §12）。
	// 它**不是默认值** —— PriceLimits 的零值会报「推不出来」，
	// 因为大商所是四舍五入，默认挑一种会在另一家上静默错。
	up, lo, ok := inst.PriceLimits(decimal.RequireFromString("3158"), true, refdata.TickFloor)
	if !ok {
		t.Fatal("⚠️ 推不出涨跌停 —— 而涨跌幅比例明明填了")
	}
	// 实测：rb2701 昨结 3158 时柜台给 3315 / 3000。
	if !up.Equal(decimal.RequireFromString("3315")) ||
		!lo.Equal(decimal.RequireFromString("3000")) {
		t.Errorf("⚠️ 涨跌停推出 %s / %s，柜台实测是 3315 / 3000 —— "+
			"这条链子（实测涨跌幅比例 → 快照 → 推涨跌停）第一次跑通就对不上", up, lo)
	}
	t.Logf("ⓘ 涨跌停推算：昨结 3158 → %s / %s，与柜台实测一致 —— "+
		"**今天新测出来的涨跌幅比例第一次被用上，且用对了**", up, lo)
}

type specRow struct {
	mult, tick decimal.Decimal
	expire     types.TradingDay
}

func loadSpecFile(t *testing.T) map[string]specRow {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "refdata", "specs-20260908.json"))
	if err != nil {
		t.Skipf("没有规格文件：%v", err)
	}
	var raw struct {
		Specs []struct {
			Instrument string      `json:"instrument"`
			Mult       json.Number `json:"volume_multiple"`
			Tick       json.Number `json:"price_tick"`
			Expire     string      `json:"expire_date"`
		} `json:"specs"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]specRow{}
	for _, r := range raw.Specs {
		exp, err := types.ParseTradingDay(r.Expire)
		if err != nil {
			t.Fatalf("合约 %s 的到期日 %q：%v", r.Instrument, r.Expire, err)
		}
		out[r.Instrument] = specRow{
			decimal.RequireFromString(r.Mult.String()),
			decimal.RequireFromString(r.Tick.String()),
			exp,
		}
	}
	return out
}

func mustID(t *testing.T, sym string, day types.TradingDay) types.InstrumentID {
	t.Helper()
	id, err := types.ParseSymbol(sym, day)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
