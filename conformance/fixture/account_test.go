package fixture

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/dream-until-dawn/futures-position-simulator-go/pnl"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestAccountAggregatesFromTrades 把对拍从**逐笔**抬到**账户层**。
//
// 账户截面的 21 个字段里，大多数需要完整的资金流才能重建；
// 但有两个能只从成交与持仓推出来，而它们恰好是最要紧的两个：
//
//	commission    Σ 每一笔的手续费（本库自己算的，不是把柜台的加起来）
//	close_profit  Σ 每一次平仓的已实现盈亏
//
// ⚠️ 关键在「本库自己算的」：把柜台给的每笔费额加起来再与柜台的合计比，
// 验的是**柜台自己内部一致**，与本库无关。这里两侧必须一侧是本库、一侧是柜台。
func TestAccountAggregatesFromTrades(t *testing.T) {
	all := loadAll(t)
	var target *Fixture
	for _, f := range all {
		if f.Path == "status-20260908-7.json" {
			target = f
		}
	}
	if target == nil {
		t.Skip("找不到带行情的夹具")
	}

	libCommission := decimal.Zero
	libCloseByDate, libCloseByTrade := decimal.Zero, decimal.Zero
	closes, opens := 0, 0

	for _, sym := range target.Symbols() {
		trades := target.TradesOf(sym)
		if len(trades) == 0 {
			continue
		}
		pre, hasPre := target.PreSettlement(sym)
		if !hasPre {
			t.Fatalf("⚠️ %s 没有昨结算价 —— 这份夹具不自足", sym)
		}
		multStr, ok := multipliers[sym]
		if !ok {
			t.Fatalf("%s 没有登记乘数", sym)
		}
		mult := decimal.RequireFromString(multStr)
		product, _ := splitProduct(trades[0].Instrument.Product)
		rates, _, ok := ratesOf(product)
		if !ok {
			t.Fatalf("品种 %s 没有登记费率", product)
		}

		// —— 手续费：本库逐笔算，再加起来 ——
		//
		// ⚠️ 基准用昨结算价（快期实测口径），不是成交价 ——
		// 那条已知偏离在 TestFeeAgainstEveryTrade 里单独钉着，
		// 这里若用成交价，账户合计会以一个**看起来只是小差**的形式对不上。
		for _, tr := range trades {
			c, err := fee.Compute(rates, tr.Offset, pre, mult, tr.Volume, decimalx.NoRounding)
			if err != nil {
				t.Fatalf("%s：%v", sym, err)
			}
			libCommission = libCommission.Add(c)
			if tr.Offset == types.Open {
				opens++
			} else {
				closes++
			}
		}

		// —— 平仓盈亏：从重放的已实现片段算 ——
		_, realized, err := ReplayRealized(nil, trades[0].Instrument,
			types.Speculation, refdata.PositionDateNotNeeded, target.TradingDay, trades)
		if err != nil {
			t.Fatalf("%s 重放失败：%v", sym, err)
		}
		for _, rz := range realized {
			legs := make([]pnl.Leg, 0, len(rz.Consumed))
			for _, l := range rz.Consumed {
				legs = append(legs, pnl.Leg{
					Volume: l.Volume, OpenPrice: l.OpenPrice, Basis: l.Basis,
				})
			}
			res, err := pnl.CloseProfit(legs, rz.Direction, rz.ClosePrice, mult)
			if err != nil {
				t.Fatalf("%s 算平仓盈亏失败：%v", sym, err)
			}
			libCloseByDate = libCloseByDate.Add(res.ByDate)
			libCloseByTrade = libCloseByTrade.Add(res.ByTrade)
		}
	}

	oracleCommission := target.Account["commission"].Number
	oracleClose := target.Account["close_profit"].Number
	t.Logf("成交 %d 笔（开 %d / 平 %d）", opens+closes, opens, closes)
	t.Logf("  手续费   本库 %s   柜台 %s", libCommission, oracleCommission)
	t.Logf("  平仓盈亏 本库逐日盯市 %s / 逐笔对冲 %s   柜台 %s",
		libCloseByDate, libCloseByTrade, oracleClose)

	// —— 判别力守卫 ——
	//
	// ⚠️ 平仓笔数为零时，close_profit 两边都是 0，「一致」什么都不说明。
	if closes < 10 {
		t.Fatalf("⚠️ 只有 %d 笔平仓 —— 平仓盈亏那条判别力不足", closes)
	}
	// ⚠️ 柜台的平仓盈亏必须**非零**：全平在成本价上时它是 0，
	// 而本库也算出 0 —— 那是两个零相等，不是一次验证。
	if oracleClose.IsZero() {
		t.Fatal("⚠️ 柜台的 close_profit 是 0 —— 本批全平在成本价上？" +
			"那么「平仓盈亏对得上」只是两个零相等")
	}

	// ⚠️ 把残差显式打出来，别让容差把它藏了。
	//
	// 实测：柜台的 commission 合计是 394.6773000000001，而本库算的是 394.6773。
	// 那个尾巴是**柜台侧的 float64 累加误差** —— 本库用 decimal，不产生它。
	// 差在 1e-13 量级，落在容差内，但它是一个**关于柜台的事实**：
	// 一个要求精确相等的对拍会在这里红，而红的理由与本库无关。
	resid := libCommission.Sub(oracleCommission).Abs()
	t.Logf("  手续费残差 %s —— ⚠️ 柜台侧是 float64 累加，本库是 decimal；"+
		"这个尾巴是柜台的，不是本库的", resid)
	if resid.IsZero() {
		t.Logf("ⓘ 本次残差为零 —— 柜台这次没有累出尾巴。" +
			"不要据此认为它永远不会：上一次观测到的是 1e-13")
	}
	// ⚠️ 上界：残差大到能被看见时，那就不是浮点尾巴了。
	if resid.GreaterThan(decimal.RequireFromString("0.00001")) {
		t.Errorf("⚠️ 手续费残差 %s 已经超出浮点尾巴的量级 —— "+
			"这是一个真实的差异，不要用容差盖过去", resid)
	}

	fields := []conformance.Field{
		{Name: "commission", Library: libCommission, Oracle: oracleCommission, Triggered: true},
		{Name: "close_profit", Library: libCloseByDate, Oracle: oracleClose, Triggered: true},
	}
	r := conformance.Classify(target.Path+"·账户合计", fields,
		decimal.RequireFromString("0.0000001"))
	if !r.Passed() {
		t.Errorf("⚠️ 账户层对不上：\n%s", r.Summary())
	}

	// ⚠️ 明说这一条**测不到**什么：
	// 本批全是今仓，两条基线都是开仓价，于是逐日盯市与逐笔对冲**必然相等**。
	// 「两套口径都实现了」这句话在这里没有被验证 —— 要等昨仓。
	if libCloseByDate.Equal(libCloseByTrade) {
		t.Logf("⚠️ 逐日盯市与逐笔对冲算出同一个数（%s）—— "+
			"本批全是今仓，两条基线本就相同。"+
			"**两套口径的区别在这里没有被验证**，要等今晚的昨仓", libCloseByDate)
	} else {
		t.Logf("ⓘ 两套口径给出了不同的数（%s vs %s）—— "+
			"第一次真正分开，去更新 rules_pending", libCloseByDate, libCloseByTrade)
	}
}
