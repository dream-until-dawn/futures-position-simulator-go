package fixture

import (
	"sort"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// feeRate 是一个品种的手续费口径。
//
// ⚠️ 它**只从一个月份标定**，然后拿去算这个品种的**其余月份** ——
// 那才是非循环的：拿一个合约的费额反解费率、再用它算这个合约，是同义反复。
type feeRate struct {
	product  string
	byMoney  string // 按额费率（乘在「昨结算价 × 乘数」上）
	byVolume string // 按手固定额
	// calibratedOn 是标定用的那个合约；对它自己的预测是循环的，
	// 所以本测试**把它排除在预测断言之外**，只算进「复现」。
	calibratedOn string
	// predictive 报告这个品种有没有第二个月份可预测。
	// ⚠️ 只有一个月份时，任何「对上了」都是循环的，必须明说。
	predictive bool
}

// feeRates 是从 fee-predict 实验的分类结果来的（probes.md §10.2）。
//
// ⚠️ 数值来自实测反解，但**分类**（按额 / 按手）是跨月份判出来的：
// rb 三个月份费额各不相同而费额÷昨结恒定 → 按额；
// m  三个月份费额恒定而昨结各不相同     → 按手。
// i / cu / ag 各只有一个月份 —— **判不了**，这里照实标 predictive=false。
var feeRates = []feeRate{
	{product: "rb", byMoney: "0.00001", byVolume: "0", calibratedOn: "SHFE.rb2701", predictive: true},
	{product: "m", byMoney: "0", byVolume: "1.5", calibratedOn: "DCE.m2701", predictive: true},
	// 下面三个只有一个月份，标定与预测是同一个合约 —— 循环，不进预测断言。
	{product: "i", byMoney: "0.0001", byVolume: "0", calibratedOn: "DCE.i2701"},
	{product: "cu", byMoney: "0.00005", byVolume: "0", calibratedOn: "SHFE.cu2701"},
	{product: "ag", byMoney: "0.00005", byVolume: "0", calibratedOn: "SHFE.ag2702"},
}

func ratesOf(product string) (refdata.CommissionRates, feeRate, bool) {
	for _, r := range feeRates {
		if r.product != product {
			continue
		}
		m := decimal.RequireFromString(r.byMoney)
		v := decimal.RequireFromString(r.byVolume)
		// ⚠️ 平今与开仓同费率、平昨也同 —— 实测九个合约无一例外（kq_facts 18）。
		// 这是**本口子的**事实，不是规则：真实 CTP 上平今常常另有费率。
		// 而它的后果是：平今这个维度在这个口子上**测不出来**，
		// 所以下面对拍里「平今算对了」这句话，判别力是零。
		return refdata.CommissionRates{
			OpenByMoney: m, OpenByVolume: v,
			CloseByMoney: m, CloseByVolume: v,
			CloseTodayByMoney: m, CloseTodayByVolume: v,
		}, r, true
	}
	return refdata.CommissionRates{}, feeRate{}, false
}

// TestFeeAgainstEveryTrade 用**每一笔真实成交**对拍手续费。
//
// ⚠️ 这在成交与行情进夹具之前做不到：
// 费额在成交里，基准价（昨结算价）在行情里，而两者此前都不在夹具中。
//
// 三件事一起验：
//
//	① 基准价是昨结算价而不是成交价  —— 同一笔用两个基准各算一遍，看哪个对
//	② 费率按品种、跨月份可外推      —— 标定一个月份，算另外两个
//	③ 公式本身能重现每一笔          —— 全部成交逐笔比
func TestFeeAgainstEveryTrade(t *testing.T) {
	all := loadAll(t)

	var byPreSettle, byTradePrice []conformance.Field
	predicted, circular, noQuote := 0, 0, 0
	products := map[string]bool{}
	// predictedSyms 是「不是标定用的那个合约」的合约集合 —— 判别力的真实载体。
	predictedSyms := map[string]bool{}
	perLotTrades := 0

	for _, f := range all {
		for _, tr := range f.Trades {
			sym := tr.Instrument.Canonical()
			product, _ := splitProduct(tr.Instrument.Product)
			rates, meta, ok := ratesOf(product)
			if !ok {
				t.Errorf("⚠️ 品种 %s 没有登记费率 —— 它的成交无法对拍", product)
				continue
			}
			pre, hasPre := f.PreSettlement(sym)
			if !hasPre {
				// ⚠️ 缺行情的夹具在这里**跳过**并计数，不去别处找一个数补上。
				noQuote++
				continue
			}
			mult, ok := multipliers[sym]
			if !ok {
				t.Errorf("⚠️ %s 没有登记乘数", sym)
				continue
			}
			m := decimal.RequireFromString(mult)

			// ① 按额基准 = 昨结算价（快期口径，实测）
			wantPre, err := fee.Compute(rates, tr.Offset, pre, m, tr.Volume, decimalx.NoRounding)
			if err != nil {
				t.Errorf("%s：%v", sym, err)
				continue
			}
			// ② 按额基准 = 成交价（CTP 建模口径）
			wantTrade, err := fee.Compute(rates, tr.Offset, tr.Price, m, tr.Volume, decimalx.NoRounding)
			if err != nil {
				t.Errorf("%s：%v", sym, err)
				continue
			}

			name := f.Path + " " + tr.TradeID[:8] + " " + sym
			byPreSettle = append(byPreSettle, conformance.Field{
				Name: name, Library: wantPre, Oracle: tr.Commission, Triggered: true,
			})
			byTradePrice = append(byTradePrice, conformance.Field{
				Name: name, Library: wantTrade, Oracle: tr.Commission, Triggered: true,
			})
			products[product] = true
			if meta.byVolume != "0" {
				perLotTrades++
			}
			if meta.predictive && sym != meta.calibratedOn {
				predicted++
				predictedSyms[sym] = true
			} else {
				circular++
			}
		}
	}

	tol := decimal.RequireFromString("0.0000001")
	rPre := conformance.Classify("全部成交·基准=昨结算价", byPreSettle, tol)
	rTrade := conformance.Classify("全部成交·基准=成交价", byTradePrice, tol)

	t.Logf("成交 %d 笔，覆盖品种 %d 个；其中**非循环预测** %d 笔、标定合约自身 %d 笔、缺行情跳过 %d 笔",
		len(byPreSettle), len(products), predicted, circular, noQuote)
	t.Logf("  基准=昨结算价：对得上 %d，失败 %d",
		rPre.Counts[conformance.Matched], rPre.Counts[conformance.Failed])
	t.Logf("  基准=成交价  ：对得上 %d，失败 %d",
		rTrade.Counts[conformance.Matched], rTrade.Counts[conformance.Failed])

	// —— 判别力守卫 ——
	//
	// ⚠️ 判别力的载体是**被预测到的合约数**，不是成交笔数。
	//
	// 第一版这里卡的是「非循环预测 ≥ 50 笔」，那个数是随手定的：
	// 同一个合约上一百笔成交，仍然只验证了一次「用别处标定的费率算得对」。
	// 真正要问的是「有几个合约是拿别的合约标定出来的费率算对的」。
	syms := make([]string, 0, len(predictedSyms))
	for s := range predictedSyms {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	t.Logf("  非循环预测覆盖的合约：%v", syms)
	if len(predictedSyms) < 2 {
		t.Fatalf("⚠️ 只预测到 %d 个非标定合约 —— "+
			"其余都是「反解出来再算回去」，那是同义反复", len(predictedSyms))
	}
	// 而且要跨品种：全在 rb 上的话，验证的是同一套规则的同一条路径。
	predProducts := map[string]bool{}
	for s := range predictedSyms {
		if i := len(s) - 4; i > 0 {
			p, _ := splitProduct(s[:i])
			predProducts[p] = true
		}
	}
	if len(predProducts) < 2 {
		t.Fatalf("⚠️ 非循环预测只覆盖 %d 个品种 —— "+
			"而两种收法（按额 / 按手）各要至少一个才谈得上「都验过了」", len(predProducts))
	}
	if len(products) < 4 {
		t.Fatalf("⚠️ 只覆盖 %d 个品种 —— 太少", len(products))
	}

	// —— ① 昨结算价这一侧必须全对 ——
	if n := rPre.Counts[conformance.Failed]; n != 0 {
		t.Errorf("⚠️ 以昨结算价为基准仍有 %d 笔对不上：\n%s", n, rPre.Summary())
	}

	// —— ② 成交价这一侧必须**大量对不上** ——
	//
	// ⚠️ 这一条不是「期望失败」的花招，它是判别力本身：
	// 若两个基准算出来一样多，本测试就分不开它们，
	// 「基准是昨结算价」这个结论在本批样本上就没有被验证过。
	failedByTrade := rTrade.Counts[conformance.Failed]
	if failedByTrade == 0 {
		t.Errorf("⚠️ 用**成交价**当基准也全对 —— 两个基准在本批样本上给出同一个答案，" +
			"于是「基准是昨结算价」这句话没有被验证过")
	}
	t.Logf("ⓘ 用成交价当基准时 %d 笔对不上 —— 这正是 kq_facts 4 那条已知偏离，"+
		"此前只有「一买一卖差一个 tick」两笔证据", failedByTrade)

	// —— ③ 按手品种对这条区分没有判别力，明说 ——
	//
	// m 是按手固定额，费额与任何价格都无关，两个基准必然给同一个数。
	// 若不把它单独说出来，上面那个 failedByTrade 会让人以为全部品种都被分开了。
	t.Logf("ⓘ 其中 %d 笔是按手品种 —— 它们对「基准是哪个价」**没有判别力**："+
		"按手费额与任何价格都无关，两个基准必然给同一个数", perLotTrades)
	if perLotTrades == 0 {
		t.Error("⚠️ 一笔按手品种的成交都没有 —— 「两种收法并存」在本批里没被覆盖")
	}
	// ⚠️ 交叉核对：成交价口径下「对得上」的笔数，应当**至少**有按手那么多
	// （按手品种必然对得上）。若少于它，说明按手品种也算错了。
	if matched := rTrade.Counts[conformance.Matched]; matched < perLotTrades {
		t.Errorf("⚠️ 成交价口径下只有 %d 笔对得上，而按手品种就有 %d 笔 —— "+
			"按手费额与价格无关，它们在两个口径下都该对得上", matched, perLotTrades)
	}
}

// splitProduct 从品种代码里去掉可能的月份（本处的 Product 已是纯品种，留个防线）。
func splitProduct(p string) (string, bool) {
	for i := 0; i < len(p); i++ {
		if p[i] >= '0' && p[i] <= '9' {
			return p[:i], false
		}
	}
	return p, true
}

// TestFeeRatesTableIsConsistent 断言费率表本身没有自相矛盾。
func TestFeeRatesTableIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	predictive := 0
	for _, r := range feeRates {
		if seen[r.product] {
			t.Errorf("品种 %s 在费率表里出现两次", r.product)
		}
		seen[r.product] = true
		byMoney := decimal.RequireFromString(r.byMoney)
		byVolume := decimal.RequireFromString(r.byVolume)
		// ⚠️ 两者都非零，或都为零，都说明这条记录没被真正分类过。
		if byMoney.IsZero() == byVolume.IsZero() {
			t.Errorf("⚠️ 品种 %s 的按额 %s 与按手 %s 同时为零或同时非零 —— "+
				"fee-predict 的分类是二选一的，这条记录没被真正分类过",
				r.product, r.byMoney, r.byVolume)
		}
		if r.calibratedOn == "" {
			t.Errorf("⚠️ 品种 %s 没写标定用的合约 —— "+
				"不写的话就分不出哪些预测是循环的", r.product)
		}
		if r.predictive {
			predictive++
		}
	}
	// ⚠️ 至少要有两个品种是可外推的，否则整张表都是同义反复。
	if predictive < 2 {
		t.Errorf("⚠️ 只有 %d 个品种可外推 —— 其余都是「反解再算回去」", predictive)
	}
	// 反向：不该全都标成可外推 —— 实测里 i/cu/ag 各只有一个月份。
	if predictive == len(feeRates) {
		t.Error("⚠️ 全部品种都标成可外推了 —— " +
			"而实测里 i/cu/ag 各只有一个月份，单点上判不了")
	}
}

// TestFeeOffsetsAreAllExercised 断言开平两种标志都被走到过。
//
// ⚠️ 只走开仓的话，pick 里那段「按开平标志选费率档」从未被考验；
// 而它选错档在**本口子上看不出来**（平今与开仓同费率，kq_facts 18）——
// 那正是要把这件事写下来的理由。
func TestFeeOffsetsAreAllExercised(t *testing.T) {
	all := loadAll(t)
	seen := map[types.Offset]int{}
	for _, f := range all {
		for _, tr := range f.Trades {
			seen[tr.Offset]++
		}
	}
	var names []string
	for o, n := range seen {
		names = append(names, o.String())
		_ = n
	}
	sort.Strings(names)
	t.Logf("成交里出现过的开平标志：%v", names)
	if seen[types.Open] == 0 {
		t.Error("⚠️ 一笔开仓都没有")
	}
	if seen[types.CloseToday] == 0 {
		t.Error("⚠️ 一笔平今都没有")
	}
	// ⚠️ 平昨至今零观测 —— 记下来，不是断言失败。
	if seen[types.Close] == 0 && seen[types.CloseYesterday] == 0 {
		t.Logf("ⓘ **一笔平昨都没有**（kq_facts 19）—— " +
			"平昨费率至今零观测，不是「量到了等于平今」。今晚第 8 条实验为此而设")
	} else {
		t.Logf("ⓘ 出现了平昨成交 —— 平昨费率第一次可测，去更新 kq_facts 19")
	}
	t.Logf("⚠️ 而平今与开仓在本口子上同费率（kq_facts 18），"+
		"所以「平今算对了」这句话在本批 %d 笔平今上判别力为零", seen[types.CloseToday])
}

// TestPreSettlementAndMultiplierValidate 直接考验两个取数守卫。
//
// ⚠️ 它们此前**没有覆盖**：破坏验证把 PreSettlement 的校验整段改成恒真，
// 而全部测试照样绿 —— 因为带行情的夹具里 pre_settlement 恰好都是好的。
// 一条从没被走到过的守卫，写没写是一样的。
//
// 而这两个守卫挡的东西很具体：回落到 0 会让保证金变成 0、手续费变成 0，
// 两个都看起来「便宜」，而「便宜」在数上完全合理。
func TestPreSettlementAndMultiplierValidate(t *testing.T) {
	mk := func(v Value) *Fixture {
		return &Fixture{Quotes: map[string]map[string]Value{
			"SHFE.rb2701": {"pre_settlement": v, "volume_multiple": v},
		}}
	}
	cases := []struct {
		name string
		f    *Fixture
		want bool // 期望「取到了」
	}{
		{"正常值", mk(Value{Number: decimal.RequireFromString("3158")}), true},
		{"柜台说无值", mk(Value{Absent: true}), false},
		{"是个字符串", mk(Value{Text: "3158", IsText: true}), false},
		{"零", mk(Value{Number: decimal.Zero}), false},
		{"负数", mk(Value{Number: decimal.RequireFromString("-1")}), false},
		{"没有这个键", &Fixture{Quotes: map[string]map[string]Value{
			"SHFE.rb2701": {}}}, false},
		{"整个合约没有行情", &Fixture{Quotes: map[string]map[string]Value{}}, false},
	}
	if len(cases) != 7 {
		t.Fatalf("用例 %d 条，应为 7 —— 增删了就同步改这个数", len(cases))
	}
	// ⚠️ 判别力：至少要有一个「取到了」和一个「没取到」，
	// 否则这批用例只考验了一侧。
	yes, no := 0, 0
	for _, c := range cases {
		if _, ok := c.f.PreSettlement("SHFE.rb2701"); ok != c.want {
			t.Errorf("⚠️ PreSettlement/%s：得到 %v，应为 %v —— "+
				"回落到 0 会让保证金与手续费都变成 0，而「便宜」在数上完全合理",
				c.name, ok, c.want)
		}
		if _, ok := c.f.Multiplier("SHFE.rb2701"); ok != c.want {
			t.Errorf("⚠️ Multiplier/%s：得到 %v，应为 %v —— "+
				"乘数为零会让所有金额变成 0", c.name, ok, c.want)
		}
		if c.want {
			yes++
		} else {
			no++
		}
	}
	if yes == 0 || no == 0 {
		t.Fatalf("⚠️ 用例只覆盖一侧（取到 %d / 没取到 %d）—— 判别力不足", yes, no)
	}
	// 反向：取到时的值必须是原值，不是某个默认值。
	got, _ := mk(Value{Number: decimal.RequireFromString("3158")}).PreSettlement("SHFE.rb2701")
	if !got.Equal(decimal.RequireFromString("3158")) {
		t.Errorf("⚠️ 取到的昨结算价是 %s，不是 3158", got)
	}
}
