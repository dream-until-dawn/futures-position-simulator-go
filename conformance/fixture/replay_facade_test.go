package fixture

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// mustSpec 取一个合约的规格（乘数 / 手续费率 / 保证金率），没登记就 Fatal。
func mustSpec(t *testing.T, f *Fixture, sym string) Spec {
	t.Helper()
	spec, ok := specForSymbol(t, f, sym)
	if !ok {
		t.Fatalf("⚠️ %s 没有登记规格 —— 不填默认值", sym)
	}
	return spec
}

// sameDayPre 取门面计价前提要的昨结算价：夹具自己报了就用它；没报就借**同一交易日、同一合约**的兄弟夹具报的值，
// 那些值必须**唯一**（多值或一个都没有 ⇒ ok=false）。borrowed 报告是不是借来的。
//
// ⚠️ 只给 ReplayOnFacade 当前提（使用者 2026-09-15 定，design.md §11 决策点 3）：借来的数**不许**进任何依赖昨结算价的比对 ——
// 保证金对拍自己取夹具报的值、缺就跳过，不走这里。
func sameDayPre(all []*Fixture, f *Fixture, sym string) (pre decimal.Decimal, borrowed, ok bool) {
	if p, has := f.PreSettlement(sym); has {
		return p, false, true
	}
	vals := map[string]decimal.Decimal{}
	for _, o := range all {
		if o.TradingDay != f.TradingDay {
			continue
		}
		if p, has := o.PreSettlement(sym); has {
			vals[p.String()] = p
		}
	}
	if len(vals) != 1 {
		return decimal.Zero, false, false
	}
	for _, v := range vals {
		return v, true, true
	}
	return decimal.Zero, false, false
}

// errNoSameDayPre 是「既没有自己的昨结算价、同日兄弟夹具里也没有唯一值」—— 调用方跳过并计数，不猜。
var errNoSameDayPre = errors.New("没有昨结算价，同日兄弟夹具里也没有唯一值")

// errNoCarrySource 是「带昨仓、而找不到结转来源（上一日夹具 / 交易所结算价 / 规格）」—— 调用方跳过并计数。
var errNoCarrySource = errors.New("带昨仓而结转不了")

// positionOf 取一个合约在门面上的持仓：**带昨仓就结转**（carryStartFor + ReconstructOnFacade），否则重放当日成交。
//
// ⚠️ F7c 撞出来的：旧 Replay 对带昨仓的合约也只重放当日成交，20260909 那批 rb2701 里有一笔裸 CLOSE ——
// 柜台平的是**昨仓**，旧重放手里只有今仓，就**拿今仓去平**，量上恰好对得上、不报错；对拍只比没有昨仓的那一侧，于是一直没露头。
// 门面在 PositionDateNotNeeded 下拒绝裸 CLOSE（没有实测的消耗顺序），不将错就错 ⇒ 带昨仓的合约走结转才对。
func positionOf(t *testing.T, all []*Fixture, f *Fixture, sym string) (p *position.Position, borrowed bool, err error) {
	t.Helper()
	if f.HasHistoryPosition(sym) {
		src, ok, why := carryStartFor(t, all, f, sym)
		if !ok {
			return nil, false, fmt.Errorf("%w：%s", errNoCarrySource, why)
		}
		p, err = ReconstructOnFacade(src.prev, f, sym, src.spec, positionDateOf(t, sym), src.settle, f.TradingDay)
		return p, false, err
	}
	return replaySameDay(t, all, f, sym)
}

// replaySameDay 在门面上重放当日成交（PositionDateNotNeeded：当日样本不结算），昨结算价按 sameDayPre 取。
func replaySameDay(t *testing.T, all []*Fixture, f *Fixture, sym string) (p *position.Position, borrowed bool, err error) {
	t.Helper()
	pre, borrowed, ok := sameDayPre(all, f, sym)
	if !ok {
		return nil, false, errNoSameDayPre
	}
	p, err = ReplayOnFacade(f, sym, mustSpec(t, f, sym), refdata.PositionDateNotNeeded, pre)
	return p, borrowed, err
}

// TestSameDayPreBorrowsOnlyAUniqueValue 钉住借值的两条边：自己有就不借；同日兄弟夹具多值、或者别的交易日的值，都不借。
func TestSameDayPreBorrowsOnlyAUniqueValue(t *testing.T) {
	d, d2 := mustDay(t, "20260908"), mustDay(t, "20260909")
	q := func(px string) map[string]map[string]Value {
		return map[string]map[string]Value{"SHFE.rb2701": {"pre_settlement": {Number: dd(px)}}}
	}
	self := &Fixture{TradingDay: d, Quotes: q("3158")}
	bare := &Fixture{TradingDay: d}
	sibA := &Fixture{TradingDay: d, Quotes: q("3158")}
	sibB := &Fixture{TradingDay: d, Quotes: q("3160")}
	otherDay := &Fixture{TradingDay: d2, Quotes: q("3163")}

	if p, borrowed, ok := sameDayPre([]*Fixture{sibB, self}, self, "SHFE.rb2701"); !ok || borrowed || !p.Equal(dd("3158")) {
		t.Errorf("自己有昨结算价时要用自己的、不算借：%s %v %v", p, borrowed, ok)
	}
	if p, borrowed, ok := sameDayPre([]*Fixture{bare, sibA, otherDay}, bare, "SHFE.rb2701"); !ok || !borrowed || !p.Equal(dd("3158")) {
		t.Errorf("⚠️ 同日唯一值要借到 3158（别的交易日的 3163 不算）：%s %v %v", p, borrowed, ok)
	}
	if _, _, ok := sameDayPre([]*Fixture{bare, sibA, sibB}, bare, "SHFE.rb2701"); ok {
		t.Error("⚠️ 同日兄弟夹具报了两个不同的昨结算价，却借到了一个 —— 多值时不知道该信哪个，不借")
	}
	if _, _, ok := sameDayPre([]*Fixture{bare, otherDay}, bare, "SHFE.rb2701"); ok {
		t.Error("⚠️ 只有别的交易日的昨结算价，却借到了 —— 昨结算价逐日不同")
	}
}

// TestReplayOnFacadeRefuses 断言缺输入就报错，不凑。
func TestReplayOnFacadeRefuses(t *testing.T) {
	f := loadOne(t, "status-20260908-7.json")
	const sym = "SHFE.rb2701"
	spec := mustSpec(t, f, sym)
	pre, ok := f.PreSettlement(sym)
	if !ok {
		t.Fatal("前提：status-20260908-7 带行情")
	}
	if _, err := ReplayOnFacade(f, sym, spec, refdata.PositionDateNotNeeded, pre); err != nil {
		t.Fatalf("前提：齐全时要重放得出：%v", err)
	}
	noLast := *f
	noLast.Positions = map[string]map[string]Value{}
	for k, v := range f.Positions {
		noLast.Positions[k] = v
	}
	noLast.Positions[sym] = map[string]Value{}
	noPB := *f
	noPB.Account = map[string]Value{}
	for _, c := range []struct {
		name string
		f    *Fixture
		sym  string
		pre  decimal.Decimal
		want string
	}{
		{"没有成交", f, "SHFE.zzz2701", pre, "一笔成交都没有"},
		{"昨结算价为零", f, sym, decimal.Zero, "不为正"},
		{"没有最新价", &noLast, sym, pre, "没有最新价"},
		{"没有 pre_balance", &noPB, sym, pre, "pre_balance"},
	} {
		_, err := ReplayOnFacade(c.f, c.sym, spec, refdata.PositionDateNotNeeded, c.pre)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("⚠️ %s：应当报错（含 %q），得到 %v", c.name, c.want, err)
		}
	}
}

// TestReplayOnFacadeClosesOnTheRightSide 钉住平仓方向：SELL 平多头。
//
// ⚠️ F7c 之前由旧重放的「平仓方向取反」守着（破坏 341）；方向判定现在在门面的 ApplyTrade 里。
// 合成夹具：开多 2 手、卖出平今 1 手 ⇒ 多头剩 1 手、空头 0 手（取反搞错会平空头而空头没有仓 ⇒ 报错，或开出空头）。
func TestReplayOnFacadeClosesOnTheRightSide(t *testing.T) {
	inst, err := types.ParseSymbol("SHFE.rb2701", mustDay(t, "20260908"))
	if err != nil {
		t.Fatal(err)
	}
	f := &Fixture{Path: "合成", TradingDay: mustDay(t, "20260908"),
		Account:   map[string]Value{"pre_balance": {Number: dd("1000000")}},
		Positions: map[string]map[string]Value{"SHFE.rb2701": {"last_price": {Number: dd("3160")}}},
		Trades: []Trade{
			{TradeID: "o", Instrument: inst, Direction: types.Buy, Offset: types.Open, Price: dd("3150"), Volume: 2, At: 1},
			{TradeID: "c", Instrument: inst, Direction: types.Sell, Offset: types.CloseToday, Price: dd("3160"), Volume: 1, At: 2},
		}}
	p, err := ReplayOnFacade(f, "SHFE.rb2701", mustSpec(t, f, "SHFE.rb2701"), refdata.UseHistory, dd("3158"))
	if err != nil {
		t.Fatal(err)
	}
	if p.VolumeToday(types.Buy) != 1 || p.VolumeToday(types.Sell) != 0 {
		t.Errorf("⚠️ 开多 2、卖出平今 1 之后 多 %d / 空 %d，应为 多 1 / 空 0", p.VolumeToday(types.Buy), p.VolumeToday(types.Sell))
	}
}
