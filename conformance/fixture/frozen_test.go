package fixture

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

func ord(kv ...string) map[string]Value {
	m := map[string]Value{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = Value{Text: kv[i+1], IsText: true}
	}
	return m
}

func withLeft(o map[string]Value, n string) map[string]Value {
	o["volume_left"] = Value{Number: dd(n)}
	return o
}

func withPrice(o map[string]Value, px string) map[string]Value {
	o["limit_price"] = Value{Number: dd(px)}
	return o
}

// frozenBookOf 按对拍的筛法凑规格（specsForOrders）、按实测 PositionDateType（positionDates）走 FrozenBook。
//
// 凑不齐规格 ⇒ skipped=true：调用方把冻结字段留作「未实现」并计数，不拿半本簿去比。
// ⚠️ F6b 之前冻结手数不要规格（FrozenOf 自己数），这类夹具照样比手数；改由门面算之后要规格与昨结算价 ——
// 语料里只有 exp-reject-tick-vs-limit-20260909-3 一份（缺 DCE.i2701 的昨结算价），design.md §10「F6b 修订」。
// ⚠️ 读委托的错（没有 status 之类）不被「凑不齐规格」吞掉：先单独读一遍合约。
func frozenBookOf(t *testing.T, f *Fixture) (book *order.Book, has, skipped bool, err error) {
	t.Helper()
	if !f.HasOrders {
		return nil, false, false, nil
	}
	if _, err := LiveOrderSymbols(f); err != nil {
		return nil, false, false, err
	}
	specs, ok := specsForOrders(t, f)
	if !ok {
		return nil, false, true, nil
	}
	book, has, err = FrozenBook(f, specs, positionDates)
	return book, has, false, err
}

// sideTotals 取簿上某合约多头、空头两侧的冻结（平仓单归被平的那一侧）。
func sideTotals(t *testing.T, book *order.Book, f *Fixture, sym string) (long, short order.Frozen) {
	t.Helper()
	inst, err := types.ParseSymbol(sym, f.TradingDay)
	if err != nil {
		t.Fatal(err)
	}
	return book.TotalOf(inst, types.Buy), book.TotalOf(inst, types.Sell)
}

// synthetic 是一份记了委托的合成夹具：rb2701 与 m2701 有规格与昨结算价。
func synthetic(t *testing.T, orders map[string]map[string]Value) (*Fixture, map[string]Spec) {
	t.Helper()
	f := &Fixture{HasOrders: true, TradingDay: mustDay(t, "20260909"), Orders: orders,
		Quotes: map[string]map[string]Value{
			"SHFE.rb2701": {"pre_settlement": {Number: dd("3163")}},
			"DCE.m2701":   {"pre_settlement": {Number: dd("3000")}},
		}}
	specs := map[string]Spec{
		"SHFE.rb2701": {Multiplier: dd("10"), Commission: mustRates(t, "rb"), Margin: mustMargin(t, "rb")},
		"DCE.m2701":   {Multiplier: dd("10"), Commission: mustRates(t, "m"), Margin: mustMargin(t, "m")},
	}
	return f, specs
}

// TestFrozenBookDistinguishesNoOrdersFromNoRecord 是本文件最要紧的一条。
//
// ⚠️ 「这份夹具里没有挂着的委托」与「这份夹具根本没记委托」——
// 两种情形下冻结量**都是 0**，而前者可以拿去对拍（结论是「都是 0」），
// 后者不能：判成一致什么都不说明。
//
// 20260909 之前的全部夹具都是后者。
func TestFrozenBookDistinguishesNoOrdersFromNoRecord(t *testing.T) {
	// ① 根本没记委托
	f := &Fixture{Orders: map[string]map[string]Value{}, HasOrders: false}
	book, has, err := FrozenBook(f, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if has || book != nil {
		t.Error("⚠️ 没记委托的夹具报了 has=true —— " +
			"那会让「冻结都是 0」被当成一个可对拍的结论")
	}
	// ② 记了，但一笔挂单都没有
	f2 := &Fixture{Orders: map[string]map[string]Value{}, HasOrders: true}
	book, has, err = FrozenBook(f2, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !has || book == nil {
		t.Fatal("⚠️ 记了委托但没有挂单，报了 has=false —— " +
			"「这个合约此刻没有挂单」是一个可以拿去对拍的结论")
	}
	if tot := book.Total(); !tot.IsZero() {
		t.Errorf("没有挂单却算出了冻结：%+v", tot)
	}
}

// TestFrozenBookSidesAndDates 断言方向与今昨都记对了。
func TestFrozenBookSidesAndDates(t *testing.T) {
	f, specs := synthetic(t, map[string]map[string]Value{
		// SELL/CLOSETODAY 平的是**多头**今仓
		"a": withPrice(withLeft(ord("status", "ALIVE", "exchange_id", "SHFE",
			"instrument_id", "rb2701", "direction", "SELL", "offset", "CLOSETODAY"), "2"), "3170"),
		// BUY/CLOSEYESTERDAY 平的是**空头**昨仓
		"b": withPrice(withLeft(ord("status", "ALIVE", "exchange_id", "SHFE",
			"instrument_id", "rb2701", "direction", "BUY", "offset", "CLOSEYESTERDAY"), "3"), "3150"),
		// 开仓单不冻持仓手数
		"c": withPrice(withLeft(ord("status", "ALIVE", "exchange_id", "SHFE",
			"instrument_id", "rb2701", "direction", "BUY", "offset", "OPEN"), "9"), "3100"),
		// 已终结的不冻
		"d": withPrice(withLeft(ord("status", "FINISHED", "exchange_id", "SHFE",
			"instrument_id", "rb2701", "direction", "SELL", "offset", "CLOSETODAY"), "7"), "3170"),
		// 别的合约不算进来
		"e": withPrice(withLeft(ord("status", "ALIVE", "exchange_id", "DCE",
			"instrument_id", "m2701", "direction", "SELL", "offset", "CLOSETODAY"), "5"), "3000"),
		// UseHistory 上的裸 CLOSE 冻昨仓（快期口径，kq_facts 32）：SELL 平多头
		"g": withPrice(withLeft(ord("status", "ALIVE", "exchange_id", "SHFE",
			"instrument_id", "rb2701", "direction", "SELL", "offset", "CLOSE"), "4"), "3170"),
	})
	book, has, err := FrozenBook(f, specs, positionDates)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("has 应为 true")
	}
	long, short := sideTotals(t, book, f, "SHFE.rb2701")
	if long.VolumeToday != 2 || long.VolumeHistory != 4 {
		t.Errorf("⚠️ 多头冻结 今%d/昨%d，应为 今2/昨4 —— "+
			"SELL/CLOSETODAY 平的是多头今仓、SELL/CLOSE 在 UseHistory 上冻多头昨仓", long.VolumeToday, long.VolumeHistory)
	}
	if short.VolumeHistory != 3 || short.VolumeToday != 0 {
		t.Errorf("⚠️ 空头冻结 今%d/昨%d，应为 今0/昨3", short.VolumeToday, short.VolumeHistory)
	}
	// ⚠️ 三条判别力：开仓单、已终结、别的合约，各自都必须**没有**被算进来。
	// 少任何一条，对应的过滤写错了都不会红。
	if n := long.VolumeToday + long.VolumeHistory + short.VolumeToday + short.VolumeHistory; n != 9 {
		t.Errorf("⚠️ 合计冻结 %d 手，应为 9 —— "+
			"开仓单(9)、已终结(7)、别的合约(5) 里有东西被算进来了", n)
	}
}

// TestFrozenBookRefusesAmbiguousOrMissing 断言**读不到就报错**，不给默认值。
func TestFrozenBookRefusesAmbiguousOrMissing(t *testing.T) {
	rb := func(kv ...string) map[string]Value {
		return withPrice(withLeft(ord(append([]string{"exchange_id", "SHFE", "instrument_id", "rb2701"}, kv...)...), "1"), "3170")
	}
	m := func(kv ...string) map[string]Value {
		return withPrice(withLeft(ord(append([]string{"exchange_id", "DCE", "instrument_id", "m2701"}, kv...)...), "1"), "3000")
	}
	noPrice := withLeft(ord("status", "ALIVE", "exchange_id", "SHFE", "instrument_id", "rb2701",
		"direction", "SELL", "offset", "CLOSETODAY"), "1")
	cases := []struct {
		name  string
		o     map[string]Value
		dates bool
		drop  string
		want  string
	}{
		{"没有 status", rb("direction", "SELL", "offset", "CLOSETODAY"), true, "", "没有 status"},
		{"没有 offset", rb("status", "ALIVE", "direction", "SELL"), true, "", "没有 offset"},
		{"方向不认识", rb("status", "ALIVE", "direction", "LONG", "offset", "CLOSETODAY"), true, "", "买卖方向"},
		{"没有 limit_price", noPrice, true, "", "limit_price"},
		// PositionDateType 没实测时不按交易所推：UseHistory 上裸 CLOSE = 平昨只在 rb2701 上量过
		{"裸 CLOSE 而 PositionDateType 没实测", rb("status", "ALIVE", "direction", "SELL", "offset", "CLOSE"), false, "", "没有实测的消耗顺序"},
		{"没有规格", rb("status", "ALIVE", "direction", "SELL", "offset", "CLOSETODAY"), true, "spec", "没有规格"},
		{"没有昨结算价", rb("status", "ALIVE", "direction", "SELL", "offset", "CLOSETODAY"), true, "pre", "没有昨结算价"},
		// NoUseHistory 上的裸 CLOSE 要按持仓拆今昨，而 FrozenBook 不带持仓 —— 报错，不猜一份拆分
		{"NoUseHistory 上的裸 CLOSE", m("status", "ALIVE", "direction", "SELL", "offset", "CLOSE"), true, "", "裸 CLOSE"},
	}
	for _, c := range cases {
		f, specs := synthetic(t, map[string]map[string]Value{"x": c.o})
		switch c.drop {
		case "spec":
			delete(specs, "SHFE.rb2701")
		case "pre":
			delete(f.Quotes, "SHFE.rb2701")
		}
		dates := positionDates
		if !c.dates {
			dates = nil
		}
		_, _, err := FrozenBook(f, specs, dates)
		if err == nil {
			t.Errorf("⚠️ %s：本该报错 —— 给默认值会让冻结记到错的地方，"+
				"而账面上只表现为「可平量多了几手」", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：报错了但没说到点上（找 %q）：%v", c.name, c.want, err)
		}
	}
}

// TestEveryFixtureFrozenParses 断言**每一份记了委托的入库夹具**都算得出冻结（凑不齐规格的计数报出来）。
//
// ⚠️ 它同时报出「有几份记了委托」——那个数变大是好消息：冻结那一块的证据在积累。
func TestEveryFixtureFrozenParses(t *testing.T) {
	all := loadAll(t)
	withOrders, noSpec := 0, 0
	for _, f := range all {
		if !f.HasOrders {
			continue
		}
		withOrders++
		_, _, skipped, err := frozenBookOf(t, f)
		if err != nil {
			t.Errorf("⚠️ %s 冻结算不出来：%v", f.Path, err)
		}
		if skipped {
			noSpec++
			t.Logf("ⓘ %s：委托涉及的合约凑不齐规格", f.Path)
		}
	}
	t.Logf("%d 份夹具；**记了委托的 %d 份**，其中凑不齐规格 %d 份", len(all), withOrders, noSpec)
	if withOrders == 0 {
		t.Log("ⓘ 一份记了委托的夹具都没有 —— 冻结那一块目前**没有任何证据支撑**。")
	}
	// ⚠️ 凑不齐规格的份数是 F6b 迁移时量出来的（1 份）；变多说明规格表或筛法退化了，覆盖在悄悄变小
	if noSpec > 1 {
		t.Errorf("⚠️ 凑不齐规格的夹具 %d 份，迁移时是 1 份 —— 覆盖变小了", noSpec)
	}
}

// TestFrozenAgainstOracle 是冻结那一块的**第一次真对拍**。
//
// 本库从**挂着的委托**算出冻结手数，与柜台自己报的
// `volume_*_frozen_*` 逐字段比。
//
// ⚠️ 在此之前这一块**没有任何证据支撑**：夹具里没有委托，
// 于是本库算出来的冻结拿什么去比都比不了。破坏验证当场演示过 ——
// 把冻结合计改成漏掉昨仓那部分，全套测试照样绿。
//
// ⚠️ 裸 CLOSE 按**快期实测语义**（等于平昨，kq_facts 32）解释。
// 那是一个显式选择，写在这里而不是藏在默认值里：换口子时要重新量。
func TestFrozenAgainstOracle(t *testing.T) {
	all := loadAll(t)
	compared, nonZero := 0, 0
	for _, f := range all {
		if !f.HasOrders {
			continue // 老夹具没记委托 —— 那不是「没有挂单」
		}
		book, has, skipped, err := frozenBookOf(t, f)
		if err != nil {
			t.Errorf("⚠️ %s：%v", f.Path, err)
			continue
		}
		if skipped || !has {
			continue
		}
		for _, sym := range f.Symbols() {
			long, short := sideTotals(t, book, f, sym)
			pos := f.Positions[sym]
			for _, c := range []struct {
				side  string
				today int
				his   int
			}{{"long", long.VolumeToday, long.VolumeHistory},
				{"short", short.VolumeToday, short.VolumeHistory}} {
				for _, p := range []struct {
					key  string
					want int
				}{
					{"volume_" + c.side + "_frozen_today", c.today},
					{"volume_" + c.side + "_frozen_his", c.his},
					{"volume_" + c.side + "_frozen", c.today + c.his},
				} {
					got, ok := numberOf(pos, p.key)
					if !ok {
						t.Errorf("⚠️ %s 的 %s 没有 %s 字段", f.Path, sym, p.key)
						continue
					}
					compared++
					if p.want != 0 {
						nonZero++
					}
					if got.IntPart() != int64(p.want) {
						t.Errorf("⚠️ %s %s.%s：柜台 %s，本库从委托算出 %d —— "+
							"⚠️ 委托与冻结对不上，先查是**读委托**错了还是**归类**错了",
							f.Path, sym, p.key, got, p.want)
					}
				}
			}
			compared++
		}
	}
	if compared == 0 {
		t.Skip("还没有记了委托的夹具 —— 冻结对拍待样本")
	}
	// ⚠️ 判别力：必须有**非零**的比对。全是 0 的话，
	// 「本库算出 0、柜台报 0」什么都不说明 —— 把整块逻辑删掉它照样一致。
	if nonZero == 0 {
		t.Errorf("⚠️ 比了 %d 个字段，**没有一个是非零的** —— "+
			"全零的一致什么都不说明：把整块冻结逻辑删掉，它照样一致。"+
			"要一份**挂着单**时拍的夹具（position-frozen 实验的 -held 那几份）", compared)
	}
	t.Logf("冻结对拍：比了 %d 个字段，其中非零的 %d 个", compared, nonZero)
}

// TestLoadDistinguishesEmptyOrdersFromNoOrders 在**解析那一层**验同一个区分。
//
// ⚠️ 上面那条 TestFrozenBookDistinguishesNoOrdersFromNoRecord 直接构造 Fixture，
// 于是它验的是「给定 HasOrders，FrozenBook 怎么办」——**没有验 HasOrders 是怎么来的**。
// 破坏验证当场撞到：把 `raw.Orders != nil` 改成 `len(raw.Orders) > 0`，
// 那条测试照样绿。
//
// 而真实夹具全都走 Load，所以判据必须落在**解析**上：
//
//	"orders": {}   记了委托，此刻没有挂单  → HasOrders = true
//	没有这个键      根本没记委托            → HasOrders = false
//
// 两种情形下 len 都是 0 —— 这正是 `!= nil` 与 `len() > 0` 的全部区别。
func TestLoadDistinguishesEmptyOrdersFromNoOrders(t *testing.T) {
	const base = `{"trading_day":"20260909","captured_at":"x","note":"n",
	 "account":{},"positions":{},"trades":{},"quotes":{},"unclassified":[]`
	cases := []struct {
		name string
		json string
		want bool
	}{
		{"有 orders 键但是空的", base + `,"orders":{}}`, true},
		{"根本没有 orders 键", base + `}`, false},
	}
	for _, c := range cases {
		f, err := Load(strings.NewReader(c.json), c.name)
		if err != nil {
			t.Fatalf("%s：%v", c.name, err)
		}
		if f.HasOrders != c.want {
			t.Errorf("⚠️ %s：HasOrders = %t，应为 %t —— "+
				"两种情形下委托数都是 0，而只有前者能拿去对拍冻结。"+
				"用 len() > 0 判会把它们混成一个", c.name, f.HasOrders, c.want)
		}
	}
}
