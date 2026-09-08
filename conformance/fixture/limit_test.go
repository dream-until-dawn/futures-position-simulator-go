package fixture

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/shopspring/decimal"
)

// candidates 是待试的取整方式。
//
// ⚠️ 它们**不是**本文件自己实现的 —— 用的是 refdata.Instrument.PriceLimits，
// 也就是生产代码那一条路径。
//
// 第一版在这里自己写了一套 floor/ceil/round，于是本测试验的是
// 「我在测试里写的取整能命中柜台」，而**生产代码对不对，它一个字都没说**。
// 两个实现同一件事，一起退化时测试全绿 —— 那正是本项目反复警告的形状。
var candidates = []refdata.TickRounding{
	refdata.TickFloor, refdata.TickCeil, refdata.TickHalfUp,
}

// limitsFor 用**生产代码**推一个合约在给定比例与取整下的涨跌停。
func limitsFor(tick, ratio decimal.Decimal, r refdata.TickRounding,
	pre decimal.Decimal) (up, lo decimal.Decimal, ok bool) {

	inst := refdata.Instrument{
		PriceTick:          tick,
		PriceLimitRatio:    ratio,
		HasPriceLimitRatio: true,
	}
	return inst.PriceLimits(pre, true, r)
}

// TestLimitRatioFromQuotes 从**已经在手的行情**反解涨跌幅比例与取整方向。
//
// ⚠️ 又一次「横着读已经付过代价的数据」（方法论第 31 条）：
// 涨跌停价、昨结算价、最小变动价位分别躺在夹具的行情段与合约规格文件里，
// 而「涨跌幅比例」是 refdata 快照缺的四块之一。把它们放一起就有答案。
//
// 判据：找出**同时**命中涨停与跌停的 (比例, 取整) 组合。
//
//	比例遍历 0.5% … 30%，步长 0.5%
//	取整三种：向下 / 向上 / 四舍五入
//
// ⚠️ 只命中一种时才算判出来；命中多种说明这个样本**分不开**它们
// （整除的合约就是这样，比如 ag 的 20% 与 rb2610 的 5%）——
// 那种样本要单独计数，不能算进结论。
//
// ⚠️ 单一来源：这些行情来自快期，交易所的日行情**不带涨跌停价**。
// 所以本条进 kq_facts 而不是 rules_measured。
func TestLimitRatioFromQuotes(t *testing.T) {
	specs := loadSpecsForTest(t)
	all := loadAll(t)

	type obs struct{ pre, up, lo, tick decimal.Decimal }
	rows := map[string]obs{}
	for _, f := range all {
		for sym, q := range f.Quotes {
			pre, ok1 := numberOf(q, "pre_settlement")
			up, ok2 := numberOf(q, "upper_limit")
			lo, ok3 := numberOf(q, "lower_limit")
			s, ok4 := specs[sym]
			if !ok1 || !ok2 || !ok3 || !ok4 {
				continue
			}
			if !pre.IsPositive() || !up.IsPositive() || !lo.IsPositive() {
				continue
			}
			rows[sym] = obs{pre, up, lo, s.PriceTick}
		}
	}
	if len(rows) < 5 {
		t.Fatalf("只有 %d 个合约同时有行情与规格 —— 太少，本条可能在空转", len(rows))
	}

	// 已实测的结论，钉在这里。⚠️ 红了先查夹具与规格文件，别改期望值。
	want := map[string]struct {
		ratio    string
		rounding string // 空表示本样本分不开取整方向
	}{
		"SHFE.rb2610": {"0.05", ""}, // 3100×5% 整除，三种取整同值
		"SHFE.rb2701": {"0.05", "向下取整"},
		"SHFE.rb2705": {"0.05", "向下取整"},
		"SHFE.cu2701": {"0.09", "向下取整"},
		"SHFE.ag2702": {"0.2", ""}, // 16095×20% 整除
		"DCE.m2701":   {"0.06", "四舍五入"},
		"DCE.m2703":   {"0.06", "四舍五入"},
		"DCE.m2705":   {"0.06", "四舍五入"},
		"DCE.i2701":   {"0.09", "四舍五入"},
	}

	decided, ambiguous := 0, 0
	roundingByExchange := map[string]map[string]bool{}
	syms := make([]string, 0, len(rows))
	for s := range rows {
		syms = append(syms, s)
	}
	sort.Strings(syms)

	for _, sym := range syms {
		o := rows[sym]
		var hits []string
		var ratios []string
		for r := decimal.RequireFromString("0.005"); r.LessThanOrEqual(decimal.RequireFromString("0.30")); r = r.Add(decimal.RequireFromString("0.005")) {
			for _, rd := range candidates {
				up, lo, ok := limitsFor(o.tick, r, rd, o.pre)
				if ok && up.Equal(o.up) && lo.Equal(o.lo) {
					hits = append(hits, r.String()+"@"+rd.String())
					ratios = append(ratios, r.String())
				}
			}
		}
		if len(hits) == 0 {
			t.Errorf("⚠️ %s：没有任何 (比例, 取整) 组合能同时命中涨停 %s 与跌停 %s"+
				"（昨结 %s，tick %s）—— 说明涨跌停价不是「昨结 × (1±比例) 再对齐 tick」，"+
				"那条假设本身要重查", sym, o.up, o.lo, o.pre, o.tick)
			continue
		}
		// 比例必须唯一；取整可以不唯一（整除的合约分不开）。
		uniqRatio := map[string]bool{}
		for _, r := range ratios {
			uniqRatio[r] = true
		}
		if len(uniqRatio) != 1 {
			t.Errorf("⚠️ %s 反解出多个比例 %v —— 遍历步长太粗或假设有问题", sym, hits)
			continue
		}
		var ratio string
		for r := range uniqRatio {
			ratio = r
		}
		w, known := want[sym]
		if !known {
			t.Logf("ⓘ %s 未登记：反解得 %v", sym, hits)
			continue
		}
		if ratio != w.ratio {
			t.Errorf("⚠️ %s 的涨跌幅比例反解得 %s，登记的是 %s —— "+
				"⚠️ 先查夹具与规格文件，**别改期望值**", sym, ratio, w.ratio)
		}
		if len(hits) == 1 {
			decided++
			gotRound := hits[0][len(ratio)+1:]
			if w.rounding == "" {
				t.Errorf("⚠️ %s 登记为「分不开取整方向」，实际只命中 %s —— "+
					"样本变了，登记要跟着改", sym, gotRound)
			} else if gotRound != w.rounding {
				t.Errorf("⚠️ %s 的取整方向反解得 %s，登记的是 %s", sym, gotRound, w.rounding)
			}
			// ⚠️ 按分隔符切，**不按位置**。
			// 第一版写的是 sym[:len(sym)-7]，它假设「四位交易所 + 点 + 六位合约」——
			// 而 DCE.m2701 比 SHFE.rb2701 短两位，于是切出个 "DC"。
			// 位置切法在最常见的样本上是对的，那正是它危险的地方。
			ex, _, _ := strings.Cut(sym, ".")
			if roundingByExchange[ex] == nil {
				roundingByExchange[ex] = map[string]bool{}
			}
			roundingByExchange[ex][gotRound] = true
		} else {
			ambiguous++
			if w.rounding != "" {
				t.Errorf("⚠️ %s 登记了取整方向 %s，而本样本命中多种 %v —— "+
					"那意味着这个样本**分不开**它们，登记是过度断言",
					sym, w.rounding, hits)
			}
		}
		t.Logf("%-14s 比例 %-6s 取整 %v", sym, ratio, hits)
	}

	// ⚠️ 判别力：整除的合约分不开取整方向，必须有**不整除**的样本才谈得上判出来。
	if decided < 3 {
		t.Fatalf("⚠️ 只有 %d 个合约判出了取整方向（%d 个分不开）—— "+
			"整除的合约三种取整同值，光靠它们什么都没判出来", decided, ambiguous)
	}
	if ambiguous == 0 {
		t.Logf("ⓘ 本批没有「分不开」的样本 —— 那一档的处理逻辑没被走到")
	}

	// ⚠️ 跨交易所的差异：这是本条最值钱的一句，也最容易被过度断言。
	exs := make([]string, 0, len(roundingByExchange))
	for e := range roundingByExchange {
		exs = append(exs, e)
	}
	sort.Strings(exs)
	for _, e := range exs {
		ms := make([]string, 0, len(roundingByExchange[e]))
		for m := range roundingByExchange[e] {
			ms = append(ms, m)
		}
		sort.Strings(ms)
		t.Logf("交易所 %s 判出的取整方向：%v", e, ms)
		if len(ms) > 1 {
			t.Errorf("⚠️ 交易所 %s 内部出现多种取整方向 %v —— "+
				"那说明「取整方向按交易所」这个说法不成立，要按品种甚至按合约看", e, ms)
		}
	}
	if len(exs) < 2 {
		t.Errorf("⚠️ 只判出了 %d 家交易所的取整方向 —— "+
			"「两家不同」这句话要两家都判出来才成立", len(exs))
	}
}

func loadSpecsForTest(t *testing.T) map[string]ContractSpec {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "refdata", "specs-20260908.json"))
	if err != nil {
		t.Skipf("没有规格文件，跳过：%v", err)
	}
	defer f.Close()
	specs, err := LoadSpecs(f)
	if err != nil {
		t.Fatal(err)
	}
	return specs
}
