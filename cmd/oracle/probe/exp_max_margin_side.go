package probe

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// productOf 从合约代码里切出品种与月份。
//
// 郑商所是大写 + 三位年月（AP610），其余是小写 + 四位年月（rb2701）。
// 本函数只按「字母前缀 / 数字后缀」切，不假设位数——位数假设会在郑商所上错。
func productOf(instrument string) (product, month string) {
	i := 0
	for i < len(instrument) && !unicode.IsDigit(rune(instrument[i])) {
		i++
	}
	return instrument[:i], instrument[i:]
}

// checkSpreadSample 是实验 3 的**样本守卫**。
//
// ⚠️ 它必须存在，而且必须真的会失败。
// 单合约双向持仓时，「按合约合并」与「按品种合并」给出同一个数——
// 两种解释在最常见的样本上完全同值。用单合约跑这条实验，
// 得到的绿色什么都不说明。
//
// 所以：样本不满足「同品种、两个不同月份」时，**实验自身失败**，
// 而不是跑完给一个看起来合理的结论。
func checkSpreadSample(symbols []string) error {
	if len(symbols) != 2 {
		return fmt.Errorf("实验 3 需要**恰好两个**合约，得到 %d 个：%v", len(symbols), symbols)
	}
	var prods, months []string
	for _, s := range symbols {
		ex, inst := splitSymbol(s)
		if ex == "" {
			return fmt.Errorf("合约 %q 缺交易所前缀，应形如 SHFE.rb2701", s)
		}
		p, m := productOf(inst)
		if p == "" || m == "" {
			return fmt.Errorf("合约 %q 切不出品种与月份", s)
		}
		prods = append(prods, ex+"."+p)
		months = append(months, m)
	}
	if !strings.EqualFold(prods[0], prods[1]) {
		return fmt.Errorf("实验 3 要求**同一品种**，得到 %s 与 %s —— "+
			"跨品种测不出大边的合并范围", prods[0], prods[1])
	}
	if months[0] == months[1] {
		return fmt.Errorf("实验 3 要求**两个不同月份**，两个都是 %s。"+
			"单合约上「按合约合并」与「按品种合并」给出同一个数，这样跑等于没跑", months[0])
	}
	return nil
}

// expMaxMarginSide 是实验 3：单向大边按品种还是按合约合并。
//
// 做法：同品种两个不同月份，一个建多头、一个建空头，然后看账户的 margin 合计
// 等于两条腿之和，还是等于较大的一条。
//
//	合计 ≈ 多 + 空        → 不走大边，或大边只按合约合并（本例两条腿在不同合约上）
//	合计 ≈ max(多, 空)    → 大边**按品种合并**
//
// ⚠️ 它阻塞 margin 包的合并范围，而跨期套利的保证金可能因此差一倍。
func (r *Runner) expMaxMarginSide(ctx context.Context) error {
	if err := checkSpreadSample(r.Symbols); err != nil {
		return err
	}
	cli := r.cli
	long, short := r.Symbols[0], r.Symbols[1]

	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(r.Symbols...); err != nil {
		return err
	}
	if cli.OpenLots() > 0 {
		return fmt.Errorf("账户已有持仓，实验 3 要求从空仓开始")
	}

	r.Logf("")
	r.Logf("== 实验 3：单向大边按品种还是按合约合并 ==")
	r.Logf("  样本守卫通过：%s（多） / %s（空），同品种、不同月份", long, short)

	acc0 := cli.Account()
	m0 := kq.MustNum(acc0, "margin")

	if _, err := r.openOneLot(long, kq.Buy); err != nil {
		return err
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		return kq.MustNum(cli.PositionOf(long), "volume_long_today") > 0
	}) {
		return fmt.Errorf("多头腿成交后未见持仓")
	}
	mLongOnly := kq.MustNum(cli.Account(), "margin")
	legLong := kq.MustNum(cli.PositionOf(long), "margin_long")
	r.Logf("  建多头腿后：账户 margin=%.2f  该腿 margin_long=%.2f", mLongOnly, legLong)

	if _, err := r.openOneLot(short, kq.Sell); err != nil {
		_ = r.flatten(long, kq.Buy)
		return err
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		return kq.MustNum(cli.PositionOf(short), "volume_short_today") > 0
	}) {
		_ = r.flatten(long, kq.Buy)
		return fmt.Errorf("空头腿成交后未见持仓")
	}

	// 让截面稳定一拍再读，避免读到只更新了一半的中间态。
	cli.WaitTrade(3 * time.Second)
	accBoth := cli.Account()
	mBoth := kq.MustNum(accBoth, "margin")
	legLong2 := kq.MustNum(cli.PositionOf(long), "margin_long")
	legShort := kq.MustNum(cli.PositionOf(short), "margin_short")
	r.Logf("  两腿齐备：账户 margin=%.2f（多头腿字段 %.2f，空头腿字段 %.2f）",
		mBoth, legLong2, legShort)

	if err := r.dump("exp3-max-margin-side", "实验 3：同品种两月份多空对锁，看 margin 合并范围"); err != nil {
		return err
	}

	// ⚠️ 第三次采样：把多头腿平掉，量**空头腿单独**占多少。
	//
	// 不能拿持仓字段 margin_short 当「空头腿单独值」用：如果大边在持仓字段上
	// 就已经生效，那个字段本身就是合并后的数，用它去验证「有没有合并」是
	// 同义反复。三次账户级采样（只多 / 两腿 / 只空）不依赖持仓字段怎么报。
	if err := r.flatten(long, kq.Buy); err != nil {
		return fmt.Errorf("平多头腿失败，第三次采样取不到，**账户可能仍有持仓**：%w", err)
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		return kq.MustNum(cli.PositionOf(long), "volume_long_today") == 0
	}) {
		_ = r.flatten(short, kq.Sell)
		return fmt.Errorf("多头腿报了平成但持仓未归零，截面不可信")
	}
	cli.WaitTrade(3 * time.Second)
	mShortOnly := kq.MustNum(cli.Account(), "margin")

	// 收尾放在判定**之前**：判定里任何一条 return 都不该把仓留在账上。
	if err := r.flatten(short, kq.Sell); err != nil {
		r.Logf("  ⚠️ 收尾平空头腿失败：%v", err)
	}

	sum := mLongOnly + mShortOnly
	max := mLongOnly
	if mShortOnly > max {
		max = mShortOnly
	}
	r.Logf("")
	r.Logf("  三次账户级采样：")
	r.Logf("    只有多头腿  margin = %.2f", mLongOnly)
	r.Logf("    只有空头腿  margin = %.2f", mShortOnly)
	r.Logf("    两腿齐备    margin = %.2f", mBoth)
	r.Logf("    两者之和           = %.2f  （离它 %+.2f）", sum, mBoth-sum)
	r.Logf("    两者较大者         = %.2f  （离它 %+.2f）", max, mBoth-max)

	// ⚠️ 三次采样之间隔着下单与撮合，价格会动。若保证金按最新价算，
	// 几个 tick 的漂移会进到残差里。容差按较大者的 1% 定：
	// 两个候选相差约一整条腿（≈max），1% 与之相差两个数量级，
	// 不会把两个候选混起来，也不会被行情漂移误判成「第三种算法」。
	tol := 0.01 * max
	if tol < 0.01 {
		tol = 0.01
	}
	r.Logf("    判定容差           = %.2f", tol)
	r.Logf("")

	// —— 样本守卫：跑完才能查的那部分 ——
	//
	// ⚠️ 「之和」与「较大者」同值的充要条件是**某一边为 0**，不是两边相等：
	// 两边都等于 X 时之和是 2X、较大者是 X，判别力完好。
	if !scopeDiscriminating(mLongOnly-0.01, mShortOnly-0.01) {
		return fmt.Errorf("⚠️ 有一条腿单独持有时账户 margin 为 0（只多=%.2f 只空=%.2f）——"+
			"该腿没建上。此时「之和」与「较大者」恒等，**判据不成立**，本次结论作废",
			mLongOnly, mShortOnly)
	}

	// —— 顺带记一条：持仓字段与账户口径是什么关系 ——
	legSum := legLong2 + legShort
	if abs(mBoth-legSum) < tol {
		r.Logf("  【字段语义】两腿齐备时 账户 margin == 两腿持仓字段之和（%.2f）", legSum)
	} else {
		r.Logf("  【字段语义】两腿齐备时 账户 margin(%.2f) ≠ 两腿持仓字段之和(%.2f)，差 %+.2f",
			mBoth, legSum, mBoth-legSum)
	}

	// —— 主判据 ——
	r.Logf("")
	switch {
	case abs(mBoth-sum) < tol:
		r.Logf("  结论：两腿齐备的 margin ≈ 两者之和 → **不按品种合并大边**")
		r.Logf("        （该品种可能未启用单向大边，或大边只在同一合约内生效）")
	case abs(mBoth-max) < tol:
		r.Logf("  结论：两腿齐备的 margin ≈ 两者较大者 → **单向大边按品种合并**")
		r.Logf("        ⚠️ 这意味着 margin 包必须跨合约、按品种聚合后再算，")
		r.Logf("           而不是逐合约算完相加。")
	default:
		r.Logf("  ⚠️ 两个候选都对不上（离之和 %+.2f，离较大者 %+.2f，容差 %.2f）——",
			mBoth-sum, mBoth-max, tol)
		r.Logf("     说明还有第三种算法，或账户 margin 里含有本实验没考虑的成分。")
		r.Logf("     原始截面已落盘，需人工看。**本实验不收敛**，不得写进规则文档的实测栏。")
	}

	// 【口径差】双向持仓下再测一次。
	r.Logf("")
	r.Logf("  【口径差】两腿齐备时 balance=%.4f ctp_balance=%.4f 差=%.6f",
		kq.MustNum(accBoth, "balance"), kq.MustNum(accBoth, "ctp_balance"),
		kq.MustNum(accBoth, "balance")-kq.MustNum(accBoth, "ctp_balance"))

	if n := cli.OpenLots(); n > 0 {
		return fmt.Errorf("⚠️ 实验结束但账户仍有 %.0f 手持仓，请手工处理", n)
	}
	_ = m0
	return nil
}

// scopeDiscriminating 报告「两者之和」与「两者较大者」这两个候选，
// 在这组保证金上分不分得开。
//
// ⚠️ 分不开的充要条件是**某一边为 0**，不是两边相等。
// 两边都等于 X 时之和是 2X、较大者是 X，判别力完好。
// 这条曾经被写成「两腿相等则判据不成立」——那是个指向错误原因的守卫：
// 它真正会触发的场合是「有一条腿没建上」，却会去报「样本选得不好」。
func scopeDiscriminating(a, b float64) bool {
	return a > 0 && b > 0
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
