package fixture

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/dream-until-dawn/futures-position-simulator-go/view"
	"github.com/shopspring/decimal"
)

func probesDir() string { return filepath.Join("..", "..", "testdata", "probes") }

func loadAll(t *testing.T) []*Fixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(probesDir(), "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out []*Fixture
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		fx, err := Load(f, filepath.Base(p))
		f.Close()
		if err != nil {
			t.Fatalf("⚠️ %s 读不进来：%v —— "+
				"夹具格式漂移了，而漂移之后的夹具是读不了的证据", filepath.Base(p), err)
		}
		out = append(out, fx)
	}
	if len(out) < 10 {
		t.Fatalf("只读到 %d 份夹具 —— 太少，下面的断言可能在空转", len(out))
	}
	return out
}

// TestLoadEveryFixture 断言每一份入库夹具都读得进来。
//
// ⚠️ 它同时是格式漂移的守卫：Load 开了 DisallowUnknownFields，
// 夹具里多一个顶层键就会失败。对**自己的**格式，多出来的字段是信号不是常态。
func TestLoadEveryFixture(t *testing.T) {
	all := loadAll(t)
	withTrades, totalTrades, skipped := 0, 0, 0
	for _, f := range all {
		if len(f.Trades) > 0 {
			withTrades++
			totalTrades += len(f.Trades)
		}
		skipped += len(f.SkippedTrades)
		for _, s := range f.SkippedTrades {
			t.Errorf("⚠️ %s 有解析不了的成交：%s —— "+
				"少一笔成交，重放出来的持仓就是错的，"+
				"而错的持仓会与柜台比出一堆看起来像真差异的差异", f.Path, s)
		}
	}
	t.Logf("夹具 %d 份，其中 %d 份带成交，共 %d 笔，解析失败 %d 笔",
		len(all), withTrades, totalTrades, skipped)
	// ⚠️ 下界卡在**带成交的夹具数**上：成交是这一整套的输入，
	// 一份都没有时下面的重放测试会全部空转而照样绿。
	if withTrades < 3 {
		t.Fatalf("只有 %d 份夹具带成交 —— 重放没有输入，"+
			"下面的对拍会空转而照样绿", withTrades)
	}
}

// TestDashParsesAsAbsentNotZero 是本包最要紧的一条。
func TestDashParsesAsAbsentNotZero(t *testing.T) {
	all := loadAll(t)
	dashes, zeros := 0, 0
	for _, f := range all {
		for sym, p := range f.Positions {
			for k, v := range p {
				if v.Absent {
					dashes++
					if !v.Number.IsZero() {
						t.Errorf("%s %s.%s 声明无值却带着数 %s", f.Path, sym, k, v.Number)
					}
					continue
				}
				if !v.IsText && v.Number.IsZero() {
					zeros++
				}
			}
		}
	}
	t.Logf("持仓截面里 \"-\" 共 %d 处，真正的 0 共 %d 处", dashes, zeros)
	// ⚠️ 两个下界，缺一不可：
	// 只有 "-" 而没有 0，说明解析把所有 0 也读成了无值；
	// 只有 0 而没有 "-"，说明 "-" 被读成了 0 —— 而那正是要堵的洞。
	if dashes < 100 {
		t.Errorf("⚠️ 只解析出 %d 处 \"-\" —— 疑似被读成了 0", dashes)
	}
	if zeros < 100 {
		t.Errorf("⚠️ 只解析出 %d 处真正的 0 —— 疑似把 0 也读成了无值", zeros)
	}
}

// TestReplayIsUnambiguous 断言重放在三种消耗顺序下给出同一个结果。
//
// ⚠️ 三者相同**不等于**消耗顺序不重要，只等于**这次样本分不开它们**。
// 本测试因此同时报告「有几个合约的样本能分开它们」——
// 那个数现在应当是 0，而实验 4 的目的就是把它变成非 0。
func TestReplayIsUnambiguous(t *testing.T) {
	all := loadAll(t)
	replayed, ambiguous, withCloses := 0, 0, 0
	for _, f := range all {
		for _, sym := range f.Symbols() {
			trades := f.TradesOf(sym)
			if len(trades) == 0 {
				continue
			}
			closes := 0
			for _, tr := range trades {
				if tr.Offset != types.Open {
					closes++
				}
			}
			if closes > 0 {
				withCloses++
			}
			inst := trades[0].Instrument
			_, err := Replay(inst, types.Speculation, f.TradingDay, trades)
			if err != nil {
				if strings.Contains(err.Error(), "重放有歧义") {
					ambiguous++
					t.Logf("ⓘ %s %s：%v", f.Path, sym, err)
					continue
				}
				t.Errorf("⚠️ %s %s 重放失败：%v", f.Path, sym, err)
				continue
			}
			replayed++
		}
	}
	t.Logf("重放成功 %d 个合约截面（其中 %d 个含平仓成交），有歧义 %d 个",
		replayed, withCloses, ambiguous)
	// ⚠️ 下界：一次平仓都没重放过时，「三种顺序一致」是空话。
	if withCloses < 5 {
		t.Fatalf("只有 %d 个合约截面含平仓成交 —— "+
			"「三种消耗顺序一致」这句话此时没被考验过", withCloses)
	}
	if ambiguous > 0 {
		t.Logf("⚠️ 有 %d 个截面能把三种消耗顺序分开 —— "+
			"那正是实验 4 要的样本，去看上面的 ⓘ 行", ambiguous)
	}
}

// TestReplayMatchesOracleVolumeAndPrice 是第一次真正的持仓对拍。
//
// ⚠️ 它只比**两个方向的手数与开仓均价**，不比全部 54 个字段 ——
// 理由不是省事，是这两个字段能被**重放本身**验证，而别的字段
// （保证金、盈亏）还需要费率与行情，那是另外几层输入。
// 一次把没准备好的输入也拉进来，红了之后分不清是哪一层的问题。
func TestReplayMatchesOracleVolumeAndPrice(t *testing.T) {
	all := loadAll(t)
	var fields []conformance.Field
	checked, withPosition, multiPrice := 0, 0, 0
	for _, f := range all {
		for _, sym := range f.Symbols() {
			trades := f.TradesOf(sym)
			if len(trades) == 0 {
				continue
			}
			p, err := Replay(trades[0].Instrument, types.Speculation, f.TradingDay, trades)
			if err != nil {
				continue // 歧义与失败在上一条测试里已经报过
			}
			oracle := f.Positions[sym]
			for _, side := range []struct {
				name string
				dir  types.Direction
			}{{"long", types.Buy}, {"short", types.Sell}} {
				s, err := p.Side(side.dir)
				if err != nil {
					t.Fatal(err)
				}
				vol := s.Volume()
				if vol > 0 {
					withPosition++
				}
				// 这个方向的明细里有没有两个不同的开仓价？
				prices := map[string]bool{}
				for _, l := range s.Lots() {
					prices[l.OpenPrice.String()] = true
				}
				if len(prices) > 1 {
					multiPrice++
				}
				checked++
				key := f.Path + " " + sym + "." + side.name
				fields = append(fields, conformance.Field{
					Name:      key + ".volume",
					Library:   decimal.NewFromInt(int64(vol)),
					Oracle:    oracle["volume_"+side.name].Number,
					Triggered: vol > 0,
				})
				avg, has := s.AvgOpenPrice()
				o := oracle["open_price_"+side.name]
				fields = append(fields, conformance.Field{
					Name:          key + ".open_price",
					Library:       avg,
					LibraryAbsent: !has,
					Oracle:        o.Number,
					OracleAbsent:  o.Absent,
					Triggered:     vol > 0,
				})
			}
		}
	}
	// ⚠️ 两条判别力守卫，问的是不同的事：
	//
	//	有持仓的方向够不够多   全是空仓时「手数都是 0、均价都无值」两边一致，
	//	                       什么都没证明 —— 那正是零值假通过
	//	有没有一个**多价**样本 单笔样本上「均价 = 那一笔的价」，
	//	                       加权平均这条逻辑一次都没走到
	//
	// 第二条是第一条盖不住的：一百个单笔持仓也证不了均价是加权平均。
	if withPosition < 10 {
		t.Fatalf("只有 %d 个有持仓的方向 —— 全是空仓的话两边都「一致」，"+
			"而那正是零值假通过", withPosition)
	}
	if multiPrice == 0 {
		t.Fatalf("⚠️ 一个「多笔且不同价」的持仓都没有 —— " +
			"单笔样本上均价恒等于那一笔的价，加权平均这条逻辑一次都没走到")
	}
	t.Logf("有持仓的方向 %d 个，其中多笔不同价的 %d 个", withPosition, multiPrice)
	r := conformance.Classify("全部带成交的夹具", fields, decimal.RequireFromString("0.0000001"))
	t.Logf("比对 %d 个方向、%d 个字段", checked, len(fields))
	for _, v := range []conformance.Verdict{
		conformance.Matched, conformance.Untriggered, conformance.Failed,
	} {
		t.Logf("  %-16s %d", v.String(), r.Counts[v])
	}

	// ⚠️ 这里**不要求** r.Passed()，而这不是放宽，是把判据说准。
	//
	// 一批夹具里必然有空仓合约，它们的「手数都是 0、均价两边都无值」确实一致，
	// 但那什么都没证明 —— Classify 把它们判成 Untriggered 正是对的，
	// 而 Passed() 因此永远为假。拿 Passed() 当判据，等于要求样本里没有空仓合约，
	// 那是个跟被测性质无关的要求。
	//
	// 真正要断言的是下面两条，它们合起来比 Passed() 更强也更准：
	if n := r.Counts[conformance.Failed]; n != 0 {
		t.Errorf("⚠️ 有 %d 个字段对不上 —— 重放出来的持仓与柜台不符：\n%s", n, r.Summary())
	}
	if n := r.Counts[conformance.Matched]; n < 20 {
		t.Errorf("⚠️ 只有 %d 个字段「对得上且被触发过」—— 太少。"+
			"剩下的都是空仓方向的空洞一致，那不算验证", n)
	}
	if n := r.Counts[conformance.Untriggered]; n > 0 {
		t.Logf("ⓘ %d 个字段值对得上但未触发（空仓方向）—— **不计入通过**。"+
			"它们的存在本身是提醒：这批样本里大部分合约是空的", n)
	}
	for _, e := range r.Errs {
		t.Errorf("⚠️ 判定本身出错：%v", e)
	}
}

// TestPositionViewAgainstFixtureShowsTheGap 把 54 个字段的全量对拍跑出来。
//
// ⚠️ 它**不要求通过**，而是把「离 100% 还差多少」变成一个数并钉住。
// 本库有未实现的字段（保证金今昨拆分、报单冻结、volume_*_yd），
// 它们按设计必须判失败 —— 一个能通过的全量对拍，
// 只可能来自把未实现项渲染成 0 再与柜台的 0 比。
func TestPositionViewAgainstFixtureShowsTheGap(t *testing.T) {
	all := loadAll(t)
	var target *Fixture
	for _, f := range all {
		if f.Path == "avg-price-20260908.json" {
			target = f
		}
	}
	if target == nil {
		t.Skip("找不到 avg-price-20260908.json")
	}
	const sym = "SHFE.rb2701"
	trades := target.TradesOf(sym)
	if len(trades) == 0 {
		t.Fatalf("%s 在 %s 里没有成交", sym, target.Path)
	}
	p, err := Replay(trades[0].Instrument, types.Speculation, target.TradingDay, trades)
	if err != nil {
		t.Fatal(err)
	}
	oracle := target.Positions[sym]
	lib, err := view.PositionOf(p, view.PositionInput{
		Multiplier: decimal.NewFromInt(10),
		LastPrice:  oracle["last_price"].Number, HasLast: true,
		// ⚠️ 保证金不给：本视图不重算，而 margin 包的接线是另一层。
		// 于是 margin_* 会判失败，那是**正确**的结果 —— 本库这一层确实还没接上。
	})
	if err != nil {
		t.Fatal(err)
	}
	fields, errs := ComparePosition(lib, oracle, TriggeredByVolume(oracle))
	for _, e := range errs {
		t.Errorf("⚠️ %v", e)
	}
	r := conformance.Classify(target.Path+" "+sym, fields, decimal.RequireFromString("0.0000001"))
	t.Logf("\n%s", r.Summary())

	// ⚠️ 棘轮：这些数**现在**是多少就钉多少。
	// 改好一个字段这条会红，退化一个字段也会红 —— 两种红都是要看的。
	//
	// 现状（交易日 20260908，SHFE.rb2701，3 手多头 @{3151,3151,3171}）：
	//
	//	32  对得上   手数、今昨拆分、开仓/持仓均价与成本、浮盈与持仓盈亏、最新价
	//	 3  不建模   期权市值 ×3，带 v1.0.0 到期版本
	//	15  没实现   保证金 ×6、报单冻结 ×6、volume_*_yd ×2、盘口状态 ×1
	//	 2  失败     open_cost_long_today / position_cost_long_today
	//	             —— 柜台恒填 0 而本库算真值（kq_facts 14），至今无裁决者
	//
	// ⚠️ 「失败」只有 2 个不代表快接近了：真正的缺口在「没实现」那 15 个上，
	// 而它们**不比值**。把这两个数加起来看才是离 100% 的距离。
	want := map[conformance.Verdict]int{
		conformance.Matched:        32,
		conformance.NotModeled:     3,
		conformance.NotImplemented: 15,
		conformance.Untriggered:    0,
		conformance.Failed:         2,
		conformance.KnownDeviation: 0,
	}
	// ⚠️ 合计守卫：上面每一档都钉了数，它们之和必须等于字段数。
	// 少钉一档时，那一档可以随便变而这条测试照样绿。
	sum := 0
	for _, n := range want {
		sum += n
	}
	if sum != len(fields) {
		t.Errorf("⚠️ 钉的各档之和 %d ≠ 字段数 %d —— 有档位没钉，它可以随便变", sum, len(fields))
	}
	for v, n := range want {
		if got := r.Counts[v]; got != n {
			t.Errorf("⚠️ 「%v」%d 个，钉的是 %d 个 —— "+
				"改好了字段就把这个数改小，退化了要查；两种都不许默默改", v, got, n)
		}
	}
	if r.Passed() {
		t.Error("⚠️ 全量对拍被判通过 —— 本库明明还有未实现的字段，" +
			"通过只可能来自把未实现项渲染成 0 再与柜台的 0 比")
	}
}

// TestTriggeredMustBeGiven 断言触发判定不能省。
func TestTriggeredMustBeGiven(t *testing.T) {
	_, errs := ComparePosition(view.Position{}, map[string]Value{}, nil)
	if len(errs) == 0 {
		t.Error("⚠️ 没给触发判定却没报错 —— 由值推触发是本仓库明确禁止的")
	}
}

// TestTradesAreSortedDeterministically 断言重放顺序是确定的。
//
// ⚠️ map 的迭代顺序是随机的。靠它重放会得到一个**每次都不同**的持仓，
// 而其中大多数次看起来完全正常 —— 这类 bug 不会稳定复现。
func TestTradesAreSortedDeterministically(t *testing.T) {
	all := loadAll(t)
	for _, f := range all {
		if len(f.Trades) < 2 {
			continue
		}
		if !sort.SliceIsSorted(f.Trades, func(i, j int) bool {
			if f.Trades[i].At != f.Trades[j].At {
				return f.Trades[i].At < f.Trades[j].At
			}
			return f.Trades[i].TradeID < f.Trades[j].TradeID
		}) {
			t.Errorf("⚠️ %s 的成交没有排序", f.Path)
		}
	}
}
