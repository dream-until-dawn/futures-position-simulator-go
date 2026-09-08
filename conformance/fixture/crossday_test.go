package fixture

import (
	"encoding/json"
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

// settlementOf 从**交易所**的日行情读某个合约的当日结算价。
//
// ⚠️ 刻意不从柜台的 quotes.settlement 读：
// 拿柜台自己的结算价去验柜台自己的逐日盯市，是同义反复。
// 两者在 20260908 上一致（3163），而一致是**核对出来的**，不是假设的。
func settlementOf(t *testing.T, sym string) (decimal.Decimal, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "refdata", "shfe-kx20260908.json"))
	if err != nil {
		return decimal.Zero, false
	}
	var raw struct {
		Settlements []struct {
			Instrument    string `json:"instrument"`
			Settlement    string `json:"settlement"`
			PreSettlement string `json:"pre_settlement"`
		} `json:"settlements"`
		Source, TradingDay, Note string
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, r := range raw.Settlements {
		if r.Instrument == sym {
			return decimal.RequireFromString(r.Settlement), true
		}
	}
	return decimal.Zero, false
}

// TestCrossDayConformance 是第一次**跨交易日**对拍。
//
// 链条全程用实测数据，没有一处是编的：
//
//	20260908 的夹具（成交 + 行情）
//	  → 重放出 D 日持仓
//	  → 按**交易所**给的 20260908 结算价 3163 结算
//	  → 得到 D+1 日的昨仓
//	  → 与 20260909 的持仓截面逐字段比
//
// ⚠️ 它**不要求全对**。已知会红的两处正是今天量到的两条口子行为，
// 而把它们跑出来、点名、归类，就是这条测试的目的：
//
//	position_price   本库给结算价 3163，柜台给**收盘价** 3177（kq_facts 26）
//	volume_*_his     大商所上本库给 3（经历过结算），柜台给 0（kq_facts 24）
//
// ⚠️ 冒出第三类就要查清楚 —— 那才是真的新东西。
func TestCrossDayConformance(t *testing.T) {
	all := loadAll(t)
	byPath := map[string]*Fixture{}
	for _, f := range all {
		byPath[f.Path] = f
	}
	prev, ok := byPath["status-20260908-8.json"]
	if !ok {
		t.Skip("找不到 20260908 的收盘前夹具")
	}
	next, ok := byPath["status-20260909-2.json"]
	if !ok {
		t.Skip("找不到 20260909 的结算后夹具")
	}
	// ⚠️ 两份必须真的是相邻的两个交易日，否则整条链子说的是别的事。
	if prev.TradingDay.String() != "20260908" || next.TradingDay.String() != "20260909" {
		t.Fatalf("两份夹具的交易日是 %s → %s，应为 20260908 → 20260909",
			prev.TradingDay, next.TradingDay)
	}

	specs := specsFor(t, prev)
	var fields []conformance.Field
	carried := 0
	failedFields := map[string]int{}

	for _, sym := range []string{"SHFE.rb2701", "DCE.m2701"} {
		settle, ok := settlementOf(t, sym)
		if !ok {
			// ⚠️ 大商所的日行情**没打通**（412，probes.md §2）。
			// 缺席就缺席，不拿柜台的数顶上 —— 那会把独立来源悄悄换成同源。
			t.Logf("ⓘ %s 的结算价在上期所日行情里没有（大商所未打通）—— "+
				"⚠️ **跳过**，不拿柜台的 quotes.settlement 顶替："+
				"那会把独立来源悄悄换成同源，而同源核对是同义反复", sym)
			continue
		}
		spec := specs[sym]
		p, err := Carry(prev, sym, types.Speculation, positionDateOf(t, sym), settle, next.TradingDay)
		if err != nil {
			t.Errorf("⚠️ 结转 %s 失败：%v", sym, err)
			continue
		}
		carried++

		oracle := next.Positions[sym]
		last := oracle["last_price"]
		in := view.PositionInput{
			Multiplier: spec.Multiplier,
			LastPrice:  last.Number, HasLast: !last.Absent,
		}
		// 保证金：昨仓的基准仍是昨结算价，而 D+1 日的昨结算价 = D 日的结算价。
		l, s, merr := MarginOf(p, spec.Margin, spec.Multiplier, settle, false,
			margin.PreSettleAll, margin.ByInstrument)
		if merr == nil {
			in.MarginLong, in.MarginShort, in.HasMargin = l, s, true
		} else if !IsNoPosition(merr) {
			t.Errorf("%s 算保证金失败：%v", sym, merr)
		}
		lib, err := view.PositionOf(p, in)
		if err != nil {
			t.Fatal(err)
		}
		fs, errs := ComparePosition(lib, oracle, TriggeredByVolume(oracle))
		for _, e := range errs {
			t.Errorf("⚠️ %s：%v", sym, e)
		}
		r := conformance.Classify(sym, fs, decimal.RequireFromString("0.0000001"))
		t.Logf("%s：对得上 %d / 不建模 %d / 未实现 %d / 失败 %d",
			sym, r.Counts[conformance.Matched], r.Counts[conformance.NotModeled],
			r.Counts[conformance.NotImplemented], r.Counts[conformance.Failed])
		for name, v := range r.Verdicts {
			if v == conformance.Failed {
				failedFields[name]++
				t.Logf("   ❌ %-26s 本库 %-22s 柜台 %s", name,
					fmtVal(lib[name]), fmtOracle(oracle[name]))
			}
		}
		fields = append(fields, fs...)
	}

	if carried == 0 {
		t.Fatal("⚠️ 一个合约都没结转成 —— 本条什么都没验")
	}

	// —— 已知会红的两类，逐个字段登记 ——
	//
	// ⚠️ 与「跨夹具那条」同一条纪律：新出现的失败一律报「第三类」，
	// 不许直接加进这张表。
	// ⚠️ 这张表**贴错过标签**，而错法很值得记住。
	//
	// 原来有 6 个字段（open_cost_*_today/_his、position_cost_*_today）标着
	// 类 B「NoUseHistory 不滚今昨」。但它们**全部出现在 SHFE.rb2701 上**，
	// 而 rb2701 是 UseHistory —— 与 NoUseHistory 一点关系都没有。
	//
	// 真正的 DCE.m2701 因为拿不到大商所的结算价（412 未打通）被 continue 掉了，
	// 所以**类 B 在这条测试里一次都没有被观测到**。
	// 而下面那条「每一类都必须出现」的守卫，正是被这 6 个错误标签喂饱的 ——
	// ⚠️ 一条靠错误分类维持「活着」的守卫，比没有守卫更难发现。
	known := map[string]string{
		// —— A：本库用**结算价** 3163，柜台用**收盘价** 3177（kq_facts 26/37）——
		"position_price_long":    "跨日-A",
		"position_price_short":   "跨日-A",
		"position_cost_long":     "跨日-A", // 成本 = 均价 × 手数 × 乘数，基线不同则成本不同
		"position_cost_long_his": "跨日-A",
		"position_profit_long":   "跨日-A", // 基线不同则持仓盈亏不同
		"position_profit":        "跨日-A",

		// —— D：**今昨拆分柜台不填**（kq_facts 28/33）——
		//
		// ⚠️ 只剩 position_cost_long_today 一个够格：它在 DCE.m2701 上
		// **非零过**（102450 = 3×3415×10），所以「柜台在结算时写这一侧」
		// 是有观测支撑的因果断言。
		//
		// ⚠️ 其余原本标 D 的那几个（open_cost_* 的四个、position_cost_short_*）
		// 全部改成了「跨日-?」—— 它们一次都没有非零过，
		// 而下面那条机械断言当场把它们拎了出来。评审只点名了空头那三个，
		// 机械规则连 open_cost_long_his/_today 一起找到了：
		// **手工分类漏掉的正是自己以为最熟的那几个。**
		"open_cost_long_his":        "跨日-?",
		"open_cost_long_today":      "跨日-?",
		"open_cost_short_his":       "跨日-?",
		"open_cost_short_today":     "跨日-?",
		"position_cost_long_today":  "跨日-D",
		"position_cost_short_his":   "跨日-?",
		"position_cost_short_today": "跨日-?",

		// —— 原来的 E（空仓侧「明确无值」vs 柜台 0）——
		//
		// ⚠️ 全部降级成「跨日-?」。原来的解释是 kq_facts 15（路径依赖），
		// 而**空头昨仓从来没有存在过**（419 条持仓记录里 0 次），
		// 于是「路径依赖」「柜台不填拆分」「压根没有这个仓」三种解释
		// 给出同一个观测 —— 挑其中任何一个都是猜。
		"position_cost_short":   "跨日-?",
		"position_profit_short": "跨日-?",

		// —— B：NoUseHistory 不滚今昨（kq_facts 24）——
		// ⚠️ 留着这几个键是因为**它们确实属于 B**，只是本条现在观测不到 B。
		// ⚠️ volume_short_his 不在其中：它一次都没非零过，机械断言把它降成了「?」。
		"volume_long_today":  "跨日-B",
		"volume_long_his":    "跨日-B",
		"volume_short_today": "跨日-B",
		"volume_short_his":   "跨日-?",
		// 类 C：⚠️ **行情侧还没滚到新交易日**。
		//
		// 柜台的 margin_long = 6631.8，而 6631.8 ÷ (10 × 3 × 0.07) = **3158** ——
		// 那是 20260907 的结算价，也就是**旧的**昨结算价。
		// 本库用 3163（20260908 的结算价，即 D+1 日真正的昨结算价）得到 6642.3。
		//
		// 对上了 quotes.datetime = 2026-09-08 14:59:59.999500：
		// **账户侧已滚到 20260909（trading_day、pre_balance 都变了），
		// 而行情侧仍停在昨天收盘**，保证金取的是行情侧的昨结算价。
		//
		// ⚠️ 这不是规则差异，是**时序**：两侧不同步。
		"margin":      "跨日-C",
		"margin_long": "跨日-C",
	}
	// ⚠️⚠️ **机械断言：从未取到过非零值的字段，不许被指派一个「机制」。**
	//
	// 这一条是评审 F1 的核心，也是唯一能**自动**抓住类 B 那次错标签的东西。
	//
	// 那次的形状是：6 个字段被标成「NoUseHistory 不滚今昨」，而它们全在
	// UseHistory 合约上；错标签活下来的条件是**这批字段的观测撑不起任何机制断言** ——
	// 两边都是 0 时，「柜台不填」「路径依赖」「压根没有这个仓」给出同一个观测。
	// 我把那次的错标签清掉了，**而让它活下来的那个条件还在**。
	//
	// 空头就是现成的例子：419 条持仓记录里 volume_short_his > 0 出现 **0 次**，
	// 空头昨仓从来没有存在过。给 position_cost_short_his 指派「柜台不填拆分」，
	// 是在一个从未被观测过的字段上下机制结论。
	//
	// ⚠️ 所以规则是机械的：`?` 之外的类别都是**因果断言**，
	// 而因果断言要求这个字段至少非零过一次。
	neverNonZero := fieldsNeverNonZero(all)
	for name, class := range known {
		if !neverNonZero[name] || strings.HasSuffix(class, "-?") {
			continue
		}
		t.Errorf("⚠️ 字段 %s 被指派了类别 %q，而它在全部 %d 份夹具里**一次都没有非零过** —— "+
			"两边都是 0 时，几种机制给出同一个观测，此时任何因果类别都是猜的。"+
			"⚠️ 改成「跨日-?」（样本区分不了），或者去取一份能区分它们的样本"+
			"（空头那几个要一次**空头过夜**）", name, class, len(all))
	}

	names := make([]string, 0, len(failedFields))
	for n := range failedFields {
		names = append(names, n)
	}
	sort.Strings(names)
	seen := map[string]int{}
	for _, n := range names {
		c, ok := known[n]
		if !ok {
			t.Errorf("⚠️ **第三类**失败字段 %s（%d 处）—— "+
				"已登记的只有两类：本库结算价 vs 柜台收盘价（kq_facts 26）、"+
				"NoUseHistory 不滚今昨（kq_facts 24）。"+
				"新出现的先查清楚是哪一类，不许直接加进这张表", n, failedFields[n])
			continue
		}
		seen[c]++
	}
	// ⚠️ 只要求**观测得到的**那几类必须出现。
	// 类 B 不在里面 —— 理由不是它被修好了，是本条**根本观测不到它**：
	// 它只出现在 NoUseHistory 合约上，而唯一那个（DCE.m2701）
	// 因为大商所日行情 412 未打通、拿不到结算价，在上面被 continue 掉了。
	// ⚠️ 把 B 留在这个列表里，就会像此前那样被错误标签喂饱。
	for _, c := range []string{"跨日-A", "跨日-C", "跨日-D"} {
		if seen[c] == 0 {
			t.Errorf("⚠️ 失败类 %s 一次都没出现 —— "+
				"要么它被修好了（那就把它从表里删掉并把这条一起改），"+
				"要么本条在空转", c)
		}
	}
	// ⚠️ 反过来卡住 B：它现在**应当**是 0。
	// 哪天它非零了，说明 DCE 真的被结转进来了 —— 那是好消息，但清单要跟着改，
	// 而好消息同样需要有人被通知到。
	if seen["跨日-B"] > 0 {
		t.Errorf("⚠️ 类 B（NoUseHistory 不滚）出现了 %d 次 —— "+
			"本条此前观测不到它（DCE 拿不到结算价被跳过）。"+
			"若是大商所日行情打通了，**去核对本库的 NoUseHistory 结算分支**"+
			"（position.Settle 的 RebaseAll 那一支）并更新本清单", seen["跨日-B"])
	}
	t.Logf("失败归类：A（结算价 vs 收盘价）%d、B（NoUseHistory 不滚，本条观测不到）%d、"+
		"C（行情侧未滚到新交易日）%d、D（今昨拆分柜台不填）%d、?（样本区分不了）%d",
		seen["跨日-A"], seen["跨日-B"], seen["跨日-C"], seen["跨日-D"], seen["跨日-?"])

	// 类 C 是个可证伪的预测，而它**已经被验过了**：
	// 20260909 夜盘 21:00 行情滚到新交易日之后，柜台的 margin_long 从 6631.8
	// （隐含旧昨结 3158）变成 6642.299999999999 —— 正是本库用 3163 算出来的数。
	// 那一类确实是**时序**，不是规则差异。
	//
	// ⚠️ 但本条**不会**因此变绿，而原来写在这里的那句话说它会：
	//
	//	「届时本条会以『类 C 一次都没出现』报红 —— 那是好消息」
	//
	// 那句话是错的。本条比的是两份**钉死的**夹具
	// （status-20260908-8 → status-20260909-2），它们拍摄于行情滚动之前，
	// 里面的时序差异是**那一刻的事实**，永远不会消失。
	// ⚠️ 一条承诺自己将来会红、而实际上永远不会红的注释，比没有注释更坏：
	// 它让人以为有个哨兵守在那里。
	//
	// 真要让这条预测在测试里被验，得拿一份**行情滚动之后**的夹具再比一次 ——
	// 那是另一条测试的事，不是给这条加个条件分支。
	t.Log("ⓘ 类 C（行情侧未滚到新交易日）已于 20260909 夜盘验证为**时序**问题：" +
		"行情滚后柜台 margin_long = 6642.2999…，与本库一致。" +
		"⚠️ 本条**不会**因此变绿 —— 它比的是钉死的两份夹具，那一刻的时序差异是事实")

	// ⚠️ 而**对得上的那些**才是这条测试的正面价值。
	// 其中最要紧的一个单独断言：逐笔对冲基线穿过结算不变。
	all2 := conformance.Classify("跨日合计", fields, decimal.RequireFromString("0.0000001"))
	if got := all2.Verdicts["open_price_long"]; got != conformance.Matched {
		t.Errorf("⚠️ open_price_long 判为「%v」—— "+
			"**逐笔对冲基线穿过结算不变**是本库 Lot 带两条基线这个设计的核心，"+
			"它必须对上", got)
	}
	if n := all2.Counts[conformance.Matched]; n < 20 {
		t.Errorf("⚠️ 跨日对拍只有 %d 个字段对得上 —— 太少，疑似大面积退化", n)
	}
}

func fmtVal(v view.Value) string {
	if v.Presence == view.Present {
		return v.Number.String()
	}
	return v.Presence.String()
}

func fmtOracle(v Value) string {
	if v.Absent {
		return `"-"`
	}
	if v.IsText {
		return v.Text
	}
	return v.Number.String()
}
