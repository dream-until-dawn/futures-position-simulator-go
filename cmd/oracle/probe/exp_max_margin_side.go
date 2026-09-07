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

	sum := legLong2 + legShort
	max := legLong2
	if legShort > max {
		max = legShort
	}

	r.Logf("  两腿齐备：账户 margin=%.2f", mBoth)
	r.Logf("    多头腿 margin_long =%.2f（%s）", legLong2, long)
	r.Logf("    空头腿 margin_short=%.2f（%s）", legShort, short)
	r.Logf("    两腿之和           =%.2f", sum)
	r.Logf("    两腿较大者         =%.2f", max)
	r.Logf("")

	const eps = 0.01
	switch {
	case sum == max:
		// ⚠️ 两个候选同值 —— 判据不成立，必须显式说出来而不是随便挑一个。
		r.Logf("  ⚠️ **判据不成立**：两腿保证金恰好相等，「之和」与「较大者」同值。")
		r.Logf("     换一个两腿保证金明显不等的样本（不同月份价差大，或一腿多开一手）重跑。")
	case abs(mBoth-sum) < eps:
		r.Logf("  结论：账户 margin ≈ 两腿之和 → **不按品种合并大边**")
		r.Logf("        （该品种可能未启用单向大边，或大边只在同一合约内生效）")
	case abs(mBoth-max) < eps:
		r.Logf("  结论：账户 margin ≈ 两腿较大者 → **单向大边按品种合并**")
		r.Logf("        ⚠️ 这意味着 margin 包必须跨合约、按品种聚合后再算，")
		r.Logf("           而不是逐合约算完相加。")
	default:
		r.Logf("  ⚠️ 两个候选都对不上（差 %.4f / %.4f）—— 说明还有第三种算法，",
			mBoth-sum, mBoth-max)
		r.Logf("     或者账户 margin 里含有本实验没考虑的成分。原始截面已落盘，需人工看。")
	}

	// 顺带把口径差在**双向持仓**下再测一次。
	r.Logf("")
	r.Logf("  【口径差】balance=%.4f ctp_balance=%.4f 差=%.6f",
		kq.MustNum(accBoth, "balance"), kq.MustNum(accBoth, "ctp_balance"),
		kq.MustNum(accBoth, "balance")-kq.MustNum(accBoth, "ctp_balance"))

	if err := r.dump("exp3-max-margin-side", "实验 3：同品种两月份多空对锁，看 margin 合并范围"); err != nil {
		return err
	}

	// 收尾：两条腿都平掉。即便一条失败也要试另一条，并如实报告。
	e1 := r.flatten(long, kq.Buy)
	e2 := r.flatten(short, kq.Sell)
	if e1 != nil || e2 != nil {
		return fmt.Errorf("⚠️ 收尾平仓未完成：%v / %v", e1, e2)
	}
	_ = m0
	return nil
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
