package ctpfixture

import (
	"fmt"
	"strings"
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

// TestFacadeCloseTodayRewriteAgainstCTP 核规则 #25 两所原始截面，独立固定费用和盈亏判据。
// 昨仓种子按结果重建；合成请求价取观测成交价，只验核算和委托改写，不声称复现真实撮合。
// 时段、限仓和涨跌停比例是测试配置，尤其郑商所取整并非实测，不能加入实测规则表。
func TestFacadeCloseTodayRewriteAgainstCTP(t *testing.T) {
	fx := loadCTP(t)
	d := decimal.RequireFromString
	d0, day := types.TradingDay(20260923), types.TradingDay(20260924)
	for _, c := range []struct {
		sym                    string
		first                  int
		open, today, yesterday string
		fees, profits          [2]string
	}{
		{"DCE.m2709", 1, "0.2", "0.1", "0.2", [2]string{"0.1", "0.2"}, [2]string{"-70", "-20"}},
		{"CZCE.MA709", 5, "2", "6", "2", [2]string{"6", "2"}, [2]string{"10", "-90"}},
	} {
		t.Run(c.sym, func(t *testing.T) {
			var stages [4]ctpFixture
			for i := range stages {
				n := c.first + i
				name := "ctp-slices-20260924.json"
				if n != 1 {
					name = fmt.Sprintf("ctp-slices-20260924-%d.json", n)
				}
				f, ok := fx[name]
				if !ok || f.TradingDay != "20260924" {
					t.Fatalf("缺原始夹具 %s", name)
				}
				stages[i] = f
			}
			id, err := types.ParseSymbol(c.sym, day)
			if err != nil {
				t.Fatal(err)
			}
			rules := oneInstrumentRules{inst: refdata.Instrument{ID: id, VolumeMultiple: d("10"), PriceTick: d("1"), PositionDateType: refdata.NoUseHistory, IsTrading: true, MinLimitOrderVolume: 1, MaxLimitOrderVolume: 100, PriceLimitRatio: d("0.1"), HasPriceLimitRatio: true}, commission: refdata.CommissionRates{OpenByVolume: d(c.open), CloseTodayByVolume: d(c.today), CloseByVolume: d(c.yesterday)}, margin: refdata.MarginRates{LongByMoney: d("0.1"), ShortByMoney: d("0.1")}}
			cal, err := refdata.NewCalendar([]types.TradingDay{d0, day}, []refdata.SessionTable{{Exchange: id.Exchange, Product: id.Product, Day: []refdata.Session{{Start: refdata.MustClockTime(9, 0, 0), End: refdata.MustClockTime(15, 0, 0)}}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ch := futsim.CTPChoices()
			ch.FeeRounding = fee.NoRounding
			cfg := futsim.Config{Day: d0, PreBalance: d("1000000"), Rules: rules, Choices: ch, Calendar: cal, TickRounding: map[types.Exchange]refdata.TickRounding{id.Exchange: refdata.TickFloor}, PositionLimits: map[types.InstrumentID]int{id: 100}}
			pre := num(t, stages[0].Quotes[c.sym], "PreSettlementPrice")
			makeSim := func() *futsim.Simulator {
				s, err := futsim.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Mark(d0, futsim.Quote{Instrument: id, Last: pre, HasLast: true, PreSettlement: pre, HasPreSettlement: true}); err != nil {
					t.Fatal(err)
				}
				if err := s.ApplyTrade(d0, match.Trade{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: pre, Volume: 1}); err != nil {
					t.Fatal(err)
				}
				if err := s.Settle(d0, map[types.InstrumentID]decimal.Decimal{id: pre}, day); err != nil {
					t.Fatal(err)
				}
				if err := s.Mark(day, futsim.Quote{Instrument: id, Last: pre, HasLast: true, PreSettlement: pre, HasPreSettlement: true}); err != nil {
					t.Fatal(err)
				}
				return s
			}
			a, b := makeSim(), makeSim()
			seen := map[string]bool{}
			_, code, _ := strings.Cut(c.sym, ".")
			key := c.sym + "/2/1"
			for step := 1; step <= 3; step++ {
				// 每份截面是累积成交集合，按 TradeID 求新增，禁止依赖数组顺序。
				var added []map[string]any
				for _, tr := range stages[step].Trades {
					if tr["InstrumentID"] != code || tr["ExchangeID"] != string(id.Exchange) {
						continue
					}
					k := fmt.Sprint(tr["TradeID"])
					if !seen[k] {
						added = append(added, tr)
						seen[k] = true
					}
				}
				if len(added) != 1 {
					t.Fatalf("每步应有一笔新增成交，得到 %d", len(added))
				}
				tr := added[0]
				off, err := offsetOf(tr["OffsetFlag"])
				if err != nil {
					t.Fatal(err)
				}
				dir := types.Buy
				requested := types.Open
				if step > 1 {
					dir, requested = types.Sell, types.CloseToday
					if off != types.Close {
						t.Fatal("原始平仓回报不是 Close")
					}
				}
				price := num(t, tr, "Price")
				vol := int(flt(t, tr, "Volume"))
				if vol != 1 {
					t.Fatal("夹具手数不是 1")
				}
				before := a.Account()
				got, err := a.Submit(day, time.Date(2026, 9, 24, 10, 0, 0, 0, refdata.CNZone()), order.Request{Instrument: id, Direction: dir, Offset: requested, Hedge: types.Speculation, Price: price, Volume: vol})
				if err != nil {
					t.Fatalf("显式平今委托路径失败：%v", err)
				}
				if got.Offset != off {
					t.Errorf("改写回报 %v，柜台 %v", got.Offset, off)
				}
				if err := b.ApplyTrade(day, match.Trade{Instrument: id, Direction: dir, Offset: off, Hedge: types.Speculation, Price: price, Volume: vol}); err != nil {
					t.Fatal(err)
				}
				feeWant := num(t, stages[step].Positions[key], "Commission").Sub(num(t, stages[step-1].Positions[key], "Commission")).Round(6)
				profitWant := num(t, stages[step].Positions[key], "CloseProfitByDate").Sub(num(t, stages[step-1].Positions[key], "CloseProfitByDate"))
				if step > 1 && (!feeWant.Equal(d(c.fees[step-2])) || !profitWant.Equal(d(c.profits[step-2]))) {
					t.Fatal("夹具增量与独立登记值不同")
				}
				if !a.Account().Commission.Sub(before.Commission).Equal(feeWant) || !a.Account().CloseProfit.Sub(before.CloseProfit).Equal(profitWant) {
					t.Fatalf("风险103手续费或逐日盈亏不符，步骤 %d", step)
				}
				p, _ := a.Position(id, types.Speculation)
				today := int(flt(t, stages[step].Positions[key], "TodayPosition"))
				history := int(flt(t, stages[step].Positions[key], "Position")) - today
				if p.VolumeToday(types.Buy) != today || p.VolumeHistory(types.Buy) != history {
					t.Fatal("风险103今昨仓不符")
				}
				sa, err := a.State()
				if err != nil {
					t.Fatal(err)
				}
				sb, err := b.State()
				if err != nil {
					t.Fatal(err)
				}
				if fmt.Sprintf("%+v", sa) != fmt.Sprintf("%+v", sb) {
					t.Fatal("委托路径与原始成交重放状态不同")
				}
			}
		})
	}
}
