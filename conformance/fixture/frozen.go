package fixture

import (
	"fmt"
	"sort"
	"strings"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// FrozenBook 把一份夹具里**全部挂着的委托**按门面算冻结，记进一本挂单簿。
//
// 调用方从簿上取：`Total()` 是账户侧金额合计（`frozen_margin` / `frozen_commission`），
// `TotalOf(合约, 持仓方向)` 是持仓侧手数（`volume_*_frozen_*`）。
//
// # ⚠️ 冻多少由门面算，本函数只做两件事：读委托、记簿
//
// 2026-09-15（F6b）之前这里有 FrozenOf（手数）与 FrozenAccountOf（金额）两个函数，
// 自己调 fee / margin 算、自己按方向与今昨累加 —— 与门面的 FreezeOf 是**两份实现**，
// 一起退化时对拍全绿（design.md「门面的形状」§10）。替换前核过：带委托且凑得齐规格的 24 份夹具，
// 两边逐项相同（金额合计、每个合约的多空今昨手数）；换冻结保证金基准或把第八项口径置零，两边立刻分开。
//
// 口径是快期的（fixtureChoices）：开仓冻保证金按**昨结算价**（kq_facts 46）、手续费按昨结算价（kq_facts 4）、
// 平仓不冻保证金（kq_facts 49）、UseHistory 上的裸 CLOSE 冻昨仓（kq_facts 32）。
//
// # ⚠️ 读不到就报错，不给默认值
//
//   - 合约没有规格、没有昨结算价（手续费与开仓保证金的基准）
//   - 委托没有 status / direction / offset / limit_price
//   - 平仓委托所在合约的 PositionDateType 没实测（dates 里没有）而又是裸 CLOSE：
//     快期上它等于平昨只在 UseHistory 上量过，**不按交易所推**
//
// 第二个返回值报告**这份夹具记没记委托**：
//
//	false  20260909 之前的夹具 —— 冻结比不了，调用方应当把这些字段留作「未实现」
//	true   记了 —— 哪怕一笔挂单都没有，那也是一个**可以拿去对拍**的结论
//
// ⚠️ 这个区分不能省：两种情形下冻结量都是 0，判成一致什么都不说明。
func FrozenBook(f *Fixture, specs map[string]Spec, dates map[string]refdata.PositionDateType) (*order.Book, bool, error) {
	if !f.HasOrders {
		return nil, false, nil
	}
	live, err := liveOrders(f)
	if err != nil {
		return nil, false, err
	}
	ids := make([]string, 0, len(live))
	for id := range live {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	type entry struct {
		id  string
		sym string
		req order.Request
	}
	var entries []entry
	rules := specRules{version: 1, byID: map[types.InstrumentID]Spec{}, dates: map[types.InstrumentID]refdata.PositionDateType{}}
	for _, id := range ids {
		o := live[id]
		sym, ok := textOf(o, "exchange_id", "instrument_id")
		if !ok {
			return nil, false, fmt.Errorf("委托 %s 读不出合约", id)
		}
		// ⚠️ 平仓委托的 direction 是**下单方向**，被平的是反向持仓 —— 簿的 TotalOf 按这个取反，这里原样放进请求
		dir, off, err := dirOffsetOf(o)
		if err != nil {
			return nil, false, fmt.Errorf("委托 %s：%w", id, err)
		}
		inst, err := types.ParseSymbol(sym, f.TradingDay)
		if err != nil {
			return nil, false, fmt.Errorf("委托 %s 的合约 %s：%w", id, sym, err)
		}
		spec, ok := specs[sym]
		if !ok {
			return nil, false, fmt.Errorf("委托 %s 的合约 %s 没有规格", id, sym)
		}
		px, ok := numberOf(o, "limit_price")
		if !ok || !px.IsPositive() {
			return nil, false, fmt.Errorf("委托 %s 没有为正的 limit_price", id)
		}
		left, _ := numberOf(o, "volume_left") // liveOrders 已保证为正
		rules.byID[inst] = spec
		if d, ok := dates[sym]; ok {
			rules.dates[inst] = d
		}
		entries = append(entries, entry{id, sym, order.Request{Instrument: inst, Direction: dir, Offset: off,
			Hedge: types.Speculation, Price: px, Volume: int(left.IntPart())}})
	}

	book := order.NewBook()
	if len(entries) == 0 {
		return book, true, nil
	}
	// 上日结存给 0：FreezeOf 不看资金，这里不编一个数
	sim, err := futsim.New(futsim.Config{Day: f.TradingDay, PreBalance: decimal.Zero, Rules: rules, Choices: fixtureChoices()})
	if err != nil {
		return nil, false, err
	}
	marked := map[string]bool{}
	for _, e := range entries {
		if marked[e.sym] {
			continue
		}
		pre, ok := f.PreSettlement(e.sym)
		if !ok {
			return nil, false, fmt.Errorf("委托 %s 的合约 %s 没有昨结算价 —— 手续费基准缺失", e.id, e.sym)
		}
		if err := sim.Mark(f.TradingDay, futsim.Quote{Instrument: e.req.Instrument, PreSettlement: pre, HasPreSettlement: true}); err != nil {
			return nil, false, fmt.Errorf("%s 计价：%w", e.sym, err)
		}
		marked[e.sym] = true
	}
	for _, e := range entries {
		fr, err := sim.FreezeOf(f.TradingDay, e.req)
		if err != nil {
			return nil, false, fmt.Errorf("委托 %s：%w", e.id, err)
		}
		in := order.FreezeInput{Margin: fr.Margin, Commission: fr.Commission,
			UndatedToday: fr.VolumeToday, UndatedHistory: fr.VolumeHistory}
		if err := book.Insert(e.id, e.req, in); err != nil {
			return nil, false, fmt.Errorf("委托 %s 进簿：%w", e.id, err)
		}
	}
	return book, true, nil
}

// aliveOf 判断一笔委托还挂着没有。
func aliveOf(o map[string]Value) (alive, ok bool) {
	v, exists := o["status"]
	if !exists || !v.IsText {
		return false, false
	}
	return strings.EqualFold(v.Text, "ALIVE"), true
}

// textOf 把几个文本字段拼成合约键，形如 SHFE.rb2701。
func textOf(o map[string]Value, keys ...string) (string, bool) {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, ok := o[k]
		if !ok || !v.IsText {
			return "", false
		}
		parts = append(parts, v.Text)
	}
	return strings.Join(parts, "."), true
}

// dirOffsetOf 读委托的方向与开平标志。
//
// ⚠️ 读不到就报错，不给默认值：一个默认成 OPEN 的开平标志
// 会让平仓委托的冻结整个消失，而账面上只表现为「可平量多了几手」。
func dirOffsetOf(o map[string]Value) (types.Direction, types.Offset, error) {
	dv, ok := o["direction"]
	if !ok || !dv.IsText {
		return 0, 0, fmt.Errorf("没有 direction 字段")
	}
	ov, ok := o["offset"]
	if !ok || !ov.IsText {
		return 0, 0, fmt.Errorf("没有 offset 字段")
	}
	var dir types.Direction
	switch strings.ToUpper(dv.Text) {
	case "BUY":
		dir = types.Buy
	case "SELL":
		dir = types.Sell
	default:
		return 0, 0, fmt.Errorf("买卖方向 %q 不认识", dv.Text)
	}
	var off types.Offset
	switch strings.ToUpper(ov.Text) {
	case "OPEN":
		off = types.Open
	case "CLOSE":
		off = types.Close
	case "CLOSETODAY":
		off = types.CloseToday
	case "CLOSEYESTERDAY":
		off = types.CloseYesterday
	default:
		return 0, 0, fmt.Errorf("开平标志 %q 不认识", ov.Text)
	}
	return dir, off, nil
}

// liveOrders 挑出**还挂着且还有未成交量**的委托。
//
// ⚠️ 单独一个函数是为了让「哪些委托要算冻结」只有一份定义。
// 调用方（对拍要先凑齐这些合约的规格）与 FrozenBook 各写一遍的话，
// 两份筛法一旦分岔，表现是**对拍悄悄少比几份夹具** —— 不报错，只是覆盖变小。
func liveOrders(f *Fixture) (map[string]map[string]Value, error) {
	out := map[string]map[string]Value{}
	for id, o := range f.Orders {
		alive, ok := aliveOf(o)
		if !ok {
			return nil, fmt.Errorf("委托 %s 没有 status 字段 —— "+
				"读不到不当成「挂着」也不当成「终结」", id)
		}
		if !alive {
			continue
		}
		left, ok := numberOf(o, "volume_left")
		if !ok || !left.IsPositive() {
			continue
		}
		out[id] = o
	}
	return out, nil
}

// LiveOrderSymbols 列出 FrozenBook 会用到规格的那些合约，升序。
func LiveOrderSymbols(f *Fixture) ([]string, error) {
	live, err := liveOrders(f)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for id, o := range live {
		sym, ok := textOf(o, "exchange_id", "instrument_id")
		if !ok {
			return nil, fmt.Errorf("委托 %s 读不出合约", id)
		}
		if !seen[sym] {
			seen[sym] = true
			out = append(out, sym)
		}
	}
	sort.Strings(out)
	return out, nil
}
