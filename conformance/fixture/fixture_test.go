package fixture

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
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
	// ⚠️ 用**带行情**的那一份：没有昨结算价就算不出保证金，
	// 而保证金缺席时那 3 个字段会落进「还没实现」——
	// 那不是本库的状态，是这份证据不自足。
	const targetName = "status-20260908-7.json"
	var target *Fixture
	for _, f := range all {
		if f.Path == targetName {
			target = f
		}
	}
	if target == nil {
		t.Skipf("找不到 %s", targetName)
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
	pre, hasPre := target.PreSettlement(sym)
	if !hasPre {
		t.Fatalf("⚠️ %s 里 %s 没有昨结算价 —— 这份夹具不自足", targetName, sym)
	}
	rates, ok := ratesFor("rb")
	if !ok {
		t.Fatal("rb 没有登记保证金率")
	}
	mult := decimal.NewFromInt(10)
	// ⚠️ 保证金由 margin 包算再传进来，**不在 view 里重算**：
	// 重算会产生第二个实现，而两个实现一起退化时测试全绿。
	mLong, mShort, err := MarginOf(p, rates, mult, pre, false,
		margin.PreSettleAll, margin.ByInstrument)
	if err != nil {
		t.Fatalf("算保证金失败：%v", err)
	}
	lib, err := view.PositionOf(p, view.PositionInput{
		Multiplier: mult,
		LastPrice:  oracle["last_price"].Number, HasLast: true,
		MarginLong: mLong, MarginShort: mShort, HasMargin: true,
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
	//	36  对得上   手数、今昨拆分、开仓/持仓均价与成本、浮盈与持仓盈亏、
	//	             最新价、保证金合计与多头保证金，**以及 volume_*_yd ×2**
	//	 3  不建模   期权市值 ×3，带 v1.0.0 到期版本
	//	11  没实现   保证金今昨拆分 ×4、报单冻结 ×6、盘口状态 ×1
	//
	// ⚠️ volume_*_yd 是 2026-09-08 结算之后才实现得了的：那次结算把
	// 「按平今平昨算的昨仓」与「实际上是昨天开的」**分开了**（kq_facts 27），
	// 而本库的 Lot 一直同时带着 Settled 与 OpenDay 两样信息 ——
	// 只是此前没有样本要求把它们分开。
	//	 2  失败     open_cost_long_today / position_cost_long_today
	//	             —— 柜台恒填 0 而本库算真值（kq_facts 14），至今无裁决者
	//
	// ⚠️ 保证金那三个字段是**接线**接上的，不是 view 里重算的：
	// margin 包算、MarginOf 翻译、view 只承载。重算会产生第二个实现，
	// 而两个实现一起退化时测试全绿。
	//
	// ⚠️ 「失败」只有 2 个不代表快接近了：真正的缺口在「没实现」那 13 个上，
	// 而它们**不比值**。把这两个数加起来看才是离 100% 的距离。
	want := map[conformance.Verdict]int{
		conformance.Matched:        36,
		conformance.NotModeled:     3,
		conformance.NotImplemented: 11,
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

// multipliers 是合约乘数。
//
// ⚠️ 出处：probes.md §7.2 的实测（从每手保证金反解并跨合约互验）。
// 写死在这里是因为本仓库还没有一份入库的 refdata 快照 ——
// 而 refdata.Builder 的零值报错明确不许拿默认值凑一份。
// 哪天有了快照，这张表要删掉换成读快照；在那之前它是**一处已知的手抄**。
var multipliers = map[string]string{
	"SHFE.rb2610": "10", "SHFE.rb2701": "10", "SHFE.rb2705": "10",
	"DCE.m2701": "10", "DCE.m2703": "10", "DCE.m2705": "10",
	"DCE.i2701": "100", "SHFE.cu2701": "5", "SHFE.ag2702": "15",
}

// TestPositionViewAcrossAllFixtures 把全量对拍推广到**每一份带成交的夹具**。
//
// ⚠️ 单份样本上的「32 对上」可能是那一份的巧合。
// 跨夹具、跨合约、跨交易所跑同一套映射，才谈得上「字段级同构」。
//
// 它同样**不要求通过**（本库有未实现字段），要求的是：
//
//	失败数不许超过钉住的那个   ——  退化会红
//	每一份样本的字段数一致     ——  柜台改字段集会红
//	至少覆盖两个交易所         ——  判别力
func TestPositionViewAcrossAllFixtures(t *testing.T) {
	all := loadAll(t)
	type key struct{ fixture, sym string }
	totals := map[conformance.Verdict]int{}
	failedFields := map[string]int{}
	samples, withMargin, skippedHistory := 0, 0, 0
	exchanges := map[types.Exchange]bool{}
	fieldCounts := map[int][]key{}

	for _, f := range all {
		for _, sym := range f.Symbols() {
			trades := f.TradesOf(sym)
			if len(trades) == 0 {
				continue
			}
			// ⚠️ 有昨仓的截面**不能**用「只重放当日成交」的路径对拍。
			//
			// 柜台的成交截面按交易日重置：昨仓那几手是昨天开的，
			// 今天的成交里没有一笔能解释它。硬跑会漏掉那几手，
			// 而漏掉之后的持仓看起来完全正常，只是手数少了几手 ——
			// 它会与柜台比出一堆看起来像真差异的差异。
			//
			// 跳过并**计数**，不静默：那个数是「这批夹具里有多少份需要 Carry」。
			if f.HasHistoryPosition(sym) {
				skippedHistory++
				continue
			}
			multStr, ok := multipliers[sym]
			if !ok {
				t.Errorf("⚠️ %s 没有登记乘数 —— 漏乘会得到一个量级正确到肉眼看不出的错值", sym)
				continue
			}
			p, err := Replay(trades[0].Instrument, types.Speculation, f.TradingDay, trades)
			if err != nil {
				continue
			}
			oracle := f.Positions[sym]
			last := oracle["last_price"]
			in := view.PositionInput{
				Multiplier: decimal.RequireFromString(multStr),
				LastPrice:  last.Number, HasLast: !last.Absent,
			}
			// ⚠️ 有昨结算价才算保证金；没有就**不给**，让那几个字段落进
			// 「还没实现」并被记账 —— 而不是拿别处的数补上。
			if pre, ok := f.PreSettlement(sym); ok {
				product, _ := splitProduct(trades[0].Instrument.Product)
				if rates, ok := ratesFor(product); ok {
					l, s, err := MarginOf(p, rates, in.Multiplier, pre, false,
						margin.PreSettleAll, margin.ByInstrument)
					switch {
					case IsNoPosition(err):
						// 空仓：不给保证金，view 会渲染成「明确无值」。
					case err != nil:
						t.Errorf("⚠️ %s %s 算保证金失败：%v", f.Path, sym, err)
					default:
						in.MarginLong, in.MarginShort, in.HasMargin = l, s, true
						withMargin++
					}
				}
			}
			lib, err := view.PositionOf(p, in)
			if err != nil {
				t.Errorf("%s %s 渲染失败：%v", f.Path, sym, err)
				continue
			}
			fields, errs := ComparePosition(lib, oracle, TriggeredByVolume(oracle))
			for _, e := range errs {
				t.Errorf("⚠️ %s %s：%v", f.Path, sym, e)
			}
			r := conformance.Classify(f.Path+" "+sym, fields,
				decimal.RequireFromString("0.0000001"))
			for v, n := range r.Counts {
				totals[v] += n
			}
			for name, v := range r.Verdicts {
				if v == conformance.Failed {
					failedFields[name]++
				}
			}
			for _, e := range r.Errs {
				t.Errorf("⚠️ %s %s 判定出错：%v", f.Path, sym, e)
			}
			samples++
			exchanges[trades[0].Instrument.Exchange] = true
			fieldCounts[len(fields)] = append(fieldCounts[len(fields)], key{f.Path, sym})
		}
	}

	t.Logf("对拍 %d 个「夹具×合约」样本，覆盖交易所 %d 家；其中 %d 个接上了保证金",
		samples, len(exchanges), withMargin)
	if skippedHistory > 0 {
		// ⚠️ 这个数从 0 变成非 0，说明**第一份带昨仓的夹具进来了** ——
		// 那是个里程碑，不是故障：本项目最核心那对区分要等它才可观测。
		// 它同时是一张待办：这些截面要走 Carry + ReplayFrom 才能对拍。
		t.Logf("⚠️ 跳过 %d 个**带昨仓**的截面 —— "+
			"只重放当日成交解释不了昨仓（成交截面按交易日重置）。"+
			"它们要走 Carry（上一日夹具 → 按结算价结算 → 昨仓）+ ReplayFrom。"+
			"⚠️ 这个数第一次非零，意味着两条基线第一次真的分开了", skippedHistory)
	}
	for _, v := range []conformance.Verdict{
		conformance.Matched, conformance.NotModeled, conformance.NotImplemented,
		conformance.Untriggered, conformance.KnownDeviation, conformance.Failed,
	} {
		t.Logf("  %-16s %d", v.String(), totals[v])
	}
	names := make([]string, 0, len(failedFields))
	for n := range failedFields {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Logf("  ❌ %s：%d 个样本上失败", n, failedFields[n])
	}

	// ⚠️ 判别力：样本要够多、要跨交易所。
	// 全在一个交易所上跑，「跨合约一致」证明的是同一套规则的同一条路径。
	if samples < 20 {
		t.Fatalf("只有 %d 个样本 —— 太少，跨夹具这句话没意义", samples)
	}
	if len(exchanges) < 2 {
		t.Fatalf("⚠️ 只覆盖 %d 家交易所 —— 「字段级同构」这句话判别力不足", len(exchanges))
	}

	// ⚠️ 每个样本的字段数必须一致：不一致说明柜台在不同截面上给了不同的字段集，
	// 而那会让「本库多出/缺少字段」的判断随样本而变。
	if len(fieldCounts) != 1 {
		for n, ks := range fieldCounts {
			t.Errorf("⚠️ 有样本的字段数是 %d（如 %v）—— 各样本字段集不一致", n, ks[0])
		}
	}

	// 棘轮：每一个失败字段都必须归进一个**已记录的类**，不许有第三类。
	//
	// ⚠️ 两类都是「本库复现不了柜台的某个行为」，而不是「本库算错了」；
	// 两类也都**还没有裁决者**，所以按 design.md §5 走不了「已知口子差异」那一档
	// （那一档要出处、裁决者、选边理由三样齐全，现在只有出处）。
	// 于是它们留在失败里 —— 红是正确的结果。
	failClass := map[string]string{
		// 类 A：今昨拆分，柜台恒填 0 而本库算真值（kq_facts 14，188/188）。
		"open_cost_long_today": "A", "position_cost_long_today": "A",
		"open_cost_short_today": "A", "position_cost_short_today": "A",
		"open_cost_long_his": "A", "position_cost_long_his": "A",
		"open_cost_short_his": "A", "position_cost_short_his": "A",
		// 类 B：空仓边的 "-" 与 0 是**路径依赖**的（kq_facts 15）。
		//
		// ⚠️ 本库判「无值」（不存在的持仓没有成本/盈亏），柜台给什么取决于
		// 这个字段当天被设过没有 —— 本库没有那个状态，复现不了也不假装能。
		// 实测的多数方向还不一致：open_cost_long 空仓时 139/144 给 "-"，
		// 而 open_cost_short 空仓时 117/185 给 0。
		// **那个不对称只反映这个账户历史上做多更多**，不是一条规则 ——
		// 幸好当初选边是按「不存在的持仓没有成本」这句话本身，不是按哪边输得少。
		"open_cost_long": "B", "open_cost_short": "B",
		"position_cost_long": "B", "position_cost_short": "B",
		"float_profit_long": "B", "float_profit_short": "B",
		"position_profit_long": "B", "position_profit_short": "B",
		"float_profit": "B", "position_profit": "B",
	}
	seenClass := map[string]int{}
	for _, n := range names {
		c, ok := failClass[n]
		if !ok {
			t.Errorf("⚠️ 多出一个**第三类**失败字段 %s（%d 个样本）—— "+
				"已记录的只有两类：今昨拆分柜台恒填 0（kq_facts 14）与"+
				"空仓边 \"-\"/0 路径依赖（kq_facts 15）。"+
				"新出现的失败要先查清楚是哪一类，不许直接加进这张表",
				n, failedFields[n])
			continue
		}
		seenClass[c] += failedFields[n]
	}
	// ⚠️ 两类都必须**真的出现过**：一类没出现时，上面的分类判断只走了一半。
	for _, c := range []string{"A", "B"} {
		if seenClass[c] == 0 {
			t.Errorf("⚠️ 失败类 %s 一次都没出现 —— "+
				"要么它被修好了（那就把它从表里删掉并把这条一起改），"+
				"要么分类判断在空转", c)
		}
	}
	t.Logf("失败归类：类 A（今昨拆分）%d 处，类 B（空仓路径依赖）%d 处",
		seenClass["A"], seenClass["B"])
	if totals[conformance.Matched] < 300 {
		t.Errorf("⚠️ 全批只有 %d 个「对得上且被触发过」—— 太少，疑似大面积退化",
			totals[conformance.Matched])
	}
}

// TestHasHistoryPositionDiscriminates 断言昨仓判据真的会判。
//
// ⚠️ 现在全部夹具的昨仓都是 0，于是 HasHistoryPosition 恒返回 false ——
// 那条守卫在**今天**的数据上一次都不会触发。
// 一条从没被触发过的守卫，与写死 `return false` 在样本上完全同值。
//
// 今晚的夹具会带昨仓，而那时它必须真的拦住。这里用合成截面提前验一次。
func TestHasHistoryPositionDiscriminates(t *testing.T) {
	mk := func(long, short string) *Fixture {
		p := map[string]Value{}
		set := func(k, v string) {
			if v == "-" {
				p[k] = Value{Absent: true}
				return
			}
			p[k] = Value{Number: decimal.RequireFromString(v)}
		}
		set("volume_long_his", long)
		set("volume_short_his", short)
		return &Fixture{Positions: map[string]map[string]Value{"SHFE.rb2701": p}}
	}
	cases := []struct {
		name        string
		long, short string
		want        bool
	}{
		{"两边都没有昨仓", "0", "0", false},
		{"多头有昨仓", "2", "0", true},
		{"空头有昨仓", "0", "3", true},
		{"两边都有", "1", "1", true},
		{"柜台给的是无值", "-", "-", false},
	}
	if len(cases) != 5 {
		t.Fatalf("用例 %d 条，应为 5", len(cases))
	}
	yes, no := 0, 0
	for _, c := range cases {
		if got := mk(c.long, c.short).HasHistoryPosition("SHFE.rb2701"); got != c.want {
			t.Errorf("⚠️ %s：得到 %v，应为 %v —— "+
				"判错的后果是拿一份缺了昨仓的重放去对拍，"+
				"而那会比出一堆看起来像真差异的差异", c.name, got, c.want)
		}
		if c.want {
			yes++
		} else {
			no++
		}
	}
	if yes == 0 || no == 0 {
		t.Fatalf("⚠️ 用例只覆盖一侧（真 %d / 假 %d）", yes, no)
	}
	// 没有这个合约时不该说「有昨仓」。
	if mk("2", "0").HasHistoryPosition("SHFE.zzz9999") {
		t.Error("⚠️ 不存在的合约被判成有昨仓")
	}
	// ⚠️ 现状记录：今天全部入库夹具的昨仓都是 0。
	// 这个数第一次非零时，本项目最核心那对区分才第一次可观测。
	all := loadAll(t)
	withHistory := 0
	for _, f := range all {
		for _, sym := range f.Symbols() {
			if f.HasHistoryPosition(sym) {
				withHistory++
			}
		}
	}
	if withHistory == 0 {
		t.Logf("ⓘ 全部入库夹具里**一个昨仓截面都没有** —— " +
			"这条守卫在真实数据上还一次没触发过，上面靠合成样本验。" +
			"今晚结算之后它会第一次真的工作")
	} else {
		t.Logf("⚠️ 已有 %d 个带昨仓的截面 —— "+
			"去把它们接进 Carry + ReplayFrom，别让它们停在「跳过」上", withHistory)
	}
}

// TestHardcodedMultipliersMatchTheDictionary 用**上游字典**核对那张手抄的乘数表。
//
// ⚠️ `multipliers` 是一处已知的手抄，它的注释写着「哪天有了快照就删掉换成读快照」——
// 而没有任何机制逼它被删掉。这条测试是那个机制的第一步：
// 手抄的可以留（读快照要多一层 IO，测试里未必划算），但它**必须与字典一致**。
//
// 两个来源是真的独立：手抄那张是从**每手保证金反解**出来的
// （保证金 = 昨结算价 × 乘数 × 费率，probes.md §7.2），
// 字典那份来自天勤的合约规格。两条路算出同一个乘数，才谈得上「确认」。
func TestHardcodedMultipliersMatchTheDictionary(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "refdata", "specs-20260908.json"))
	if err != nil {
		t.Skipf("没有规格文件，跳过：%v", err)
	}
	defer f.Close()
	specs, err := LoadSpecs(f)
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 下界：规格文件里合约太少时，下面的核对可能一条都没跑。
	if len(specs) < 20 {
		t.Fatalf("规格文件里只有 %d 个合约 —— 太少，核对可能空转", len(specs))
	}
	checked, missing := 0, 0
	for sym, want := range multipliers {
		s, ok := specs[sym]
		if !ok {
			// ⚠️ 缺席要计数：到期合约不在「在市」列表里，那是正常的，
			// 但**全部缺席**说明核对整个没发生。
			missing++
			continue
		}
		checked++
		if !s.VolumeMultiple.Equal(decimal.RequireFromString(want)) {
			t.Errorf("⚠️ %s 的乘数：手抄 %s，上游字典 %s —— "+
				"两条独立通路对不上（手抄那张是从每手保证金反解的）",
				sym, want, s.VolumeMultiple)
		}
	}
	t.Logf("核对 %d 个合约的乘数，%d 个不在在市列表里（到期合约属正常）", checked, missing)
	if checked < 5 {
		t.Errorf("⚠️ 只核对了 %d 个 —— 太少，这条基本没起作用", checked)
	}
}
