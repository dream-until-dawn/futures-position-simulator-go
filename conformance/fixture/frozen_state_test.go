package fixture

import (
	"encoding/json"
	"math/big"
	"reflect"
	"strings"
	"testing"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// frozenStateSeed 通过真实门面操作创建完整合成状态，不使用待比的资金/持仓输出。
func frozenStateSeed(t *testing.T) (futsim.Config, futsim.State, types.InstrumentID, types.InstrumentID) {
	t.Helper()
	rb := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
	m := types.InstrumentID{Exchange: types.DCE, Product: "m", Year: 2027, Month: 1}
	spec := Spec{Multiplier: dd("10"), Margin: refdata.MarginRates{LongByMoney: dd("0.1"), ShortByMoney: dd("0.1")}, Commission: refdata.CommissionRates{OpenByVolume: dd("1"), CloseByVolume: dd("2"), CloseTodayByVolume: dd("3")}}
	rules := specRules{version: 1, byID: map[types.InstrumentID]Spec{rb: spec, m: spec}, dates: map[types.InstrumentID]refdata.PositionDateType{rb: refdata.UseHistory, m: refdata.NoUseHistory}}
	cfg := futsim.Config{Day: 20260907, PreBalance: dd("100000"), Rules: rules, Choices: fixtureChoices()}
	sim, err := futsim.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range []types.InstrumentID{rb, m} {
		if err = sim.Mark(cfg.Day, futsim.Quote{Instrument: inst, Last: dd("100"), HasLast: true, PreSettlement: dd("100"), HasPreSettlement: true}); err != nil {
			t.Fatal(err)
		}
	}
	trade := func(inst types.InstrumentID, dir types.Direction, off types.Offset, volume int) {
		t.Helper()
		if err := sim.ApplyTrade(cfg.Day, match.Trade{Instrument: inst, Direction: dir, Offset: off, Hedge: types.Speculation, Price: dd("100"), Volume: volume}); err != nil {
			t.Fatal(err)
		}
	}
	trade(rb, types.Buy, types.Open, 2)
	trade(rb, types.Sell, types.Open, 1)
	trade(m, types.Buy, types.Open, 1)
	if err = sim.Settle(cfg.Day, map[types.InstrumentID]decimal.Decimal{rb: dd("100"), m: dd("100")}, 20260908); err != nil {
		t.Fatal(err)
	}
	cfg.Day = 20260908
	trade(rb, types.Buy, types.Open, 3)
	trade(rb, types.Sell, types.Open, 2)
	trade(m, types.Buy, types.Open, 1)
	// 裸平已按今日档收过1手：剩今仓1手但可用平今收费额度为0，后续冻结应按平昨档2元。
	trade(m, types.Sell, types.Close, 1)
	seed, err := sim.State()
	if err != nil {
		t.Fatal(err)
	}
	seed.LastOrderSequence = 17
	return cfg, seed, rb, m
}

func frozenRecord(exchange, code, direction, offset, volume string) map[string]Value {
	return withPrice(withLeft(ord("status", "ALIVE", "exchange_id", exchange, "instrument_id", code, "direction", direction, "offset", offset), volume), "100")
}

func stateJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestFrozenStateCompleteBook 独立手算金额和手数，另对照门面接入结果及序号，避免只有两条同源路径对上。
func TestFrozenStateCompleteBook(t *testing.T) {
	cfg, seed, rb, m := frozenStateSeed(t)
	f := &Fixture{TradingDay: cfg.Day, HasOrders: true, Orders: map[string]map[string]Value{
		"z-history": frozenRecord("SHFE", "rb2701", "SELL", "CLOSEYESTERDAY", "2"),
		"a-today":   frozenRecord("SHFE", "rb2701", "SELL", "CLOSETODAY", "3"),
		"b-short":   frozenRecord("SHFE", "rb2701", "BUY", "CLOSETODAY", "2"),
		"m-rewrite": frozenRecord("DCE", "m2701", "SELL", "CLOSETODAY", "1"),
		"c-open":    frozenRecord("SHFE", "rb2701", "BUY", "OPEN", "2"),
		"finished":  ord("status", "FINISHED"),
	}}
	ids := []string{"z-history", "a-today", "b-short", "m-rewrite", "c-open"}
	before := stateJSON(t, seed) + stateJSON(t, f)
	book, has, err := FrozenBookWithState(f, cfg, seed, ids)
	if err != nil || !has || book == nil {
		t.Fatalf("完整冻结状态接入失败 has=%v err=%v", has, err)
	}
	if book.LastSequence() != 22 || !reflect.DeepEqual(book.OrderedIDs(), ids) {
		t.Fatalf("冻结接收顺序或高水位丢失 ids=%v high=%d", book.OrderedIDs(), book.LastSequence())
	}
	// 独立手算：开仓2*100*10*0.1=200；费用2*2+3*3+2*3+1*2+2*1=23。
	if total := book.Total(); !total.Margin.Equal(dd("200")) || !total.Commission.Equal(dd("23")) {
		t.Fatalf("冻结金额或额度未按完整状态计算 total=%+v", total)
	}
	if x := book.TotalOf(rb, types.Buy); x.VolumeToday != 3 || x.VolumeHistory != 2 {
		t.Fatalf("多头今昨冻结错误 %+v", x)
	}
	if x := book.TotalOf(rb, types.Sell); x.VolumeToday != 2 || x.VolumeHistory != 0 {
		t.Fatalf("空头冻结错误 %+v", x)
	}
	req, fr, ok := book.Get("m-rewrite")
	if !ok || req.Offset != types.Close || fr.VolumeToday != 1 || fr.VolumeHistory != 0 || !fr.Commission.Equal(dd("2")) {
		t.Fatalf("有效请求或费用额度丢失 req=%+v fr=%+v", req, fr)
	}
	if x := book.TotalOf(m, types.Buy); x.VolumeToday != 1 || x.VolumeHistory != 0 {
		t.Fatalf("跨合约冻结串仓 %+v", x)
	}
	if before != stateJSON(t, seed)+stateJSON(t, f) {
		t.Fatal("冻结接入改动调用者输入")
	}
	direct, err := futsim.Restore(cfg, seed)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		req, fr, ok := book.Get(id)
		if !ok {
			t.Fatal(id)
		}
		got, err := direct.PlaceAccepted(cfg.Day, id, req)
		if err != nil || stateJSON(t, got) != stateJSON(t, fr) {
			t.Fatalf("接入与直接门面不同 id=%s err=%v", id, err)
		}
	}
	// 待比输出不能反过来影响输入；真正可比的资金/持仓由调用者另行核对。
	f.Account = map[string]Value{"available": {Number: dd("999999999")}}
	f.Positions = map[string]map[string]Value{"garbage": {"volume_long": {Number: dd("999")}}}
	again, _, err := FrozenBookWithState(f, cfg, seed, ids)
	if err != nil || stateJSON(t, again.Total()) != stateJSON(t, book.Total()) {
		t.Fatalf("冻结误用柜台输出作为输入 err=%v", err)
	}
}

// TestFrozenStateFailureIsAtomic 第二单失败不能泄漏部分簿，输入完整不变。
func TestFrozenStateFailureIsAtomic(t *testing.T) {
	for _, kind := range []string{"cumulative-close", "funds"} {
		t.Run(kind, func(t *testing.T) {
			cfg, seed, _, _ := frozenStateSeed(t)
			first, second := frozenRecord("SHFE", "rb2701", "SELL", "CLOSETODAY", "2"), frozenRecord("SHFE", "rb2701", "SELL", "CLOSETODAY", "2")
			if kind == "funds" {
				seed.Account.PreBalance = seed.Account.CurrMargin.Add(seed.Account.Commission).Add(seed.Account.Withdraw).Sub(seed.Account.Deposit).Sub(seed.Account.CloseProfit).Sub(seed.Account.PositionProfit).Add(dd("1050"))
				first = frozenRecord("SHFE", "rb2701", "BUY", "OPEN", "10")
				second = frozenRecord("SHFE", "rb2701", "BUY", "OPEN", "1")
			}
			f := &Fixture{TradingDay: cfg.Day, HasOrders: true, Orders: map[string]map[string]Value{"first": first, "second": second}}
			before := stateJSON(t, seed) + stateJSON(t, f)
			book, has, err := FrozenBookWithState(f, cfg, seed, []string{"first", "second"})
			if err == nil || book != nil || has || !strings.Contains(err.Error(), "second") {
				t.Fatalf("累计超平或资金不足未原子失败 kind=%s has=%v book=%v err=%v", kind, has, book, err)
			}
			if before != stateJSON(t, seed)+stateJSON(t, f) {
				t.Fatal("失败冻结改动输入")
			}
		})
	}
}

// TestFrozenStateRejectsUnknown 覆盖状态未知、清单不完整、非法手数和已有冻结，不把缺字段当零。
func TestFrozenStateRejectsUnknown(t *testing.T) {
	for _, kind := range []string{"format", "rules", "day", "frozen", "orders", "missing-order", "extra-order", "duplicate-order", "missing-volume", "fraction", "negative", "overflow", "exponent", "missing-status", "unknown-status", "missing-price", "unknown-instrument"} {
		t.Run(kind, func(t *testing.T) {
			cfg, seed, rb, _ := frozenStateSeed(t)
			f := &Fixture{TradingDay: cfg.Day, HasOrders: true, Orders: map[string]map[string]Value{"one": frozenRecord("SHFE", "rb2701", "BUY", "OPEN", "1")}}
			ids := []string{"one"}
			switch kind {
			case "format":
				seed.Format = 0
			case "rules":
				seed.RulesVersion++
			case "day":
				cfg.Day++
			case "frozen":
				seed.Account.FrozenMargin = dd("1")
			case "orders":
				sim, err := futsim.Restore(cfg, seed)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = sim.PlaceAccepted(cfg.Day, "old", order.Request{Instrument: rb, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: dd("100"), Volume: 1}); err != nil {
					t.Fatal(err)
				}
				seed, err = sim.State()
				if err != nil {
					t.Fatal(err)
				}
			case "missing-order":
				ids = nil
			case "extra-order":
				ids = append(ids, "extra")
			case "duplicate-order":
				f.Orders["two"] = frozenRecord("SHFE", "rb2701", "BUY", "OPEN", "1")
				ids = []string{"one", "one"}
			case "missing-volume":
				delete(f.Orders["one"], "volume_left")
				ids = nil
			case "fraction":
				f.Orders["one"]["volume_left"] = Value{Number: dd("1.5")}
			case "negative":
				f.Orders["one"]["volume_left"] = Value{Number: dd("-1")}
			case "overflow":
				f.Orders["one"]["volume_left"] = Value{Number: dd("1e30")}
			case "exponent":
				f.Orders["one"]["volume_left"] = Value{Number: decimal.New(1, 1000)}
			case "missing-status":
				delete(f.Orders["one"], "status")
			case "unknown-status":
				f.Orders["one"]["status"] = Value{IsText: true, Text: "UNKNOWN"}
				ids = nil
			case "missing-price":
				delete(f.Orders["one"], "limit_price")
			case "unknown-instrument":
				f.Orders["one"]["instrument_id"] = Value{IsText: true, Text: "rb2705"}
			}
			before := stateJSON(t, seed) + stateJSON(t, f)
			book, has, err := FrozenBookWithState(f, cfg, seed, ids)
			if err == nil || book != nil || has {
				t.Fatalf("未知冻结输入被接受 kind=%s has=%v err=%v", kind, has, err)
			}
			if kind == "duplicate-order" && !strings.Contains(err.Error(), "接收顺序") {
				t.Fatalf("重复顺序未由覆盖门槛拒绝 err=%v", err)
			}
			if before != stateJSON(t, seed)+stateJSON(t, f) {
				t.Fatal("非法冻结输入被修改")
			}
		})
	}
}

// TestFrozenStateResourceBounds 用有限大小反例分别检验指数和系数预算，避免依赖后面的整数上限兜底。
func TestFrozenStateResourceBounds(t *testing.T) {
	for _, value := range []decimal.Decimal{decimal.New(1, 1000), decimal.New(1, -1000), decimal.NewFromBigInt(new(big.Int).Lsh(big.NewInt(1), 600), 0)} {
		if _, err := frozenRemaining(Value{Number: value}); err == nil || !strings.Contains(err.Error(), "有限精度范围") {
			t.Fatalf("手数资源边界未独立拒绝 exp=%d err=%v", value.Exponent(), err)
		}
	}
}

// TestFrozenStateMissingAndEmpty 区分记录缺失与已知空，并保留空簿的历史高水位。
func TestFrozenStateMissingAndEmpty(t *testing.T) {
	if book, has, err := FrozenBookWithState(&Fixture{}, futsim.Config{}, futsim.State{}, nil); book != nil || has || err != nil {
		t.Fatal("无记录被当成已知空簿")
	}
	if _, _, err := FrozenBookWithState(nil, futsim.Config{}, futsim.State{}, nil); err == nil {
		t.Fatal("nil夹具被接受")
	}
	cfg, seed, _, _ := frozenStateSeed(t)
	f := &Fixture{TradingDay: cfg.Day, HasOrders: true, Orders: map[string]map[string]Value{"filled": frozenRecord("SHFE", "rb2701", "BUY", "OPEN", "0")}}
	book, has, err := FrozenBookWithState(f, cfg, seed, nil)
	if err != nil || !has || book == nil || len(book.Live()) != 0 || !book.Total().IsZero() || book.LastSequence() != 17 {
		t.Fatalf("已知空簿或历史高水位丢失 has=%v err=%v", has, err)
	}
}
