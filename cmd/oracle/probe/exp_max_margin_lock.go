package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// checkLockSample 是实验 3b 的**样本守卫**。
//
// 3b 问的问题与 3 不同：3 问「合并范围是品种还是合约」，
// 3b 问「单向大边**到底启没启用**」。
// 后者只能在**同一个合约**上双向持仓来问——跨合约的样本回答不了它，
// 因为「跨合约不合并」既可能是「按合约合并」，也可能是「根本没大边」。
func checkLockSample(symbols []string) error {
	if len(symbols) != 1 {
		return fmt.Errorf("实验 3b 需要**恰好一个**合约（同合约双向持仓），得到 %d 个：%v",
			len(symbols), symbols)
	}
	if ex, _ := splitSymbol(symbols[0]); ex == "" {
		return fmt.Errorf("合约 %q 缺交易所前缀，应形如 SHFE.rb2701", symbols[0])
	}
	return nil
}

// expMaxMarginLock 是实验 3b：同一合约双向持仓下，单向大边启没启用。
//
// ⚠️ 它是实验 3 的**必要补充**，不是重复。
// 实验 3 测出「跨合约按品种不合并」之后，仍有两种解释：
//
//	(a) 大边启用了，但只在**同一合约**内合并
//	(b) 该品种**根本没启用**大边
//
// 这两种解释在跨合约样本上给出同一个数。分开它们只能靠同合约双向：
//
//	合计 ≈ 多 + 空      → (b) 大边未启用
//	合计 ≈ max(多, 空)  → (a) 大边启用，合并范围 = 合约
//
// 判据同样用**三次账户级采样**（只多 / 双向 / 只空），不依赖持仓字段怎么报——
// 若大边在持仓字段上就已生效，拿字段之和去验证「有没有合并」是同义反复。
func (r *Runner) expMaxMarginLock(ctx context.Context) error {
	if err := checkLockSample(r.Symbols); err != nil {
		return err
	}
	cli := r.cli
	sym := r.Symbols[0]

	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}
	if cli.OpenLots() > 0 {
		return fmt.Errorf("账户已有持仓，实验 3b 要求从空仓开始")
	}

	r.Logf("")
	r.Logf("== 实验 3b：同合约双向持仓，单向大边启没启用 ==")
	r.Logf("  样本：%s 同时持多 1 手、空 1 手", sym)

	if _, err := r.openOneLot(sym, kq.Buy); err != nil {
		return err
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		return kq.MustNum(cli.PositionOf(sym), "volume_long_today") > 0
	}) {
		return fmt.Errorf("多头腿成交后未见持仓")
	}
	cli.WaitTrade(2 * time.Second)
	mLongOnly := kq.MustNum(cli.Account(), "margin")
	r.Logf("  只有多头腿：账户 margin=%.2f", mLongOnly)

	if _, err := r.openOneLot(sym, kq.Sell); err != nil {
		_ = r.flatten(sym, kq.Buy)
		return err
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		return kq.MustNum(cli.PositionOf(sym), "volume_short_today") > 0
	}) {
		_ = r.flatten(sym, kq.Buy)
		return fmt.Errorf("空头腿成交后未见持仓")
	}
	cli.WaitTrade(3 * time.Second)

	accBoth := cli.Account()
	mBoth := kq.MustNum(accBoth, "margin")
	p := cli.PositionOf(sym)
	fLong := kq.MustNum(p, "margin_long")
	fShort := kq.MustNum(p, "margin_short")
	fMargin := kq.MustNum(p, "margin")
	volLong := kq.MustNum(p, "volume_long_today")
	volShort := kq.MustNum(p, "volume_short_today")

	// ⚠️ 先确认双向持仓真的建成了。若柜台把第二笔当成了平仓，
	// 这里会是 1/0 而不是 1/1 —— 那样后面的数全都在答另一个问题。
	if volLong < 1 || volShort < 1 {
		_ = r.flatten(sym, kq.Buy)
		_ = r.flatten(sym, kq.Sell)
		return fmt.Errorf("⚠️ 未建成双向持仓（多今 %.0f 手，空今 %.0f 手）——"+
			"柜台可能把 OPEN 当成了平仓，或本账户是单向持仓模式。本次结论作废",
			volLong, volShort)
	}
	r.Logf("  双向齐备：多今 %.0f 手 / 空今 %.0f 手  账户 margin=%.2f", volLong, volShort, mBoth)
	r.Logf("    持仓字段 margin_long=%.2f margin_short=%.2f margin=%.2f", fLong, fShort, fMargin)

	if err := r.dump("exp3b-max-margin-lock", "实验 3b：同合约双向持仓，看单向大边启没启用"); err != nil {
		return err
	}

	// 第三次采样：平掉多头腿，量**空头腿单独**占多少。
	if err := r.flatten(sym, kq.Buy); err != nil {
		return fmt.Errorf("平多头腿失败，第三次采样取不到，**账户可能仍有持仓**：%w", err)
	}
	if !cli.WaitUntil(20*time.Second, func() bool {
		return kq.MustNum(cli.PositionOf(sym), "volume_long_today") == 0
	}) {
		_ = r.flatten(sym, kq.Sell)
		return fmt.Errorf("多头腿报了平成但持仓未归零，截面不可信")
	}
	cli.WaitTrade(3 * time.Second)
	mShortOnly := kq.MustNum(cli.Account(), "margin")

	// 收尾放在判定**之前**：判定里任何一条 return 都不该把仓留在账上。
	if err := r.flatten(sym, kq.Sell); err != nil {
		r.Logf("  ⚠️ 收尾平空头腿失败：%v", err)
	}

	sum := mLongOnly + mShortOnly
	max := mLongOnly
	if mShortOnly > max {
		max = mShortOnly
	}
	tol := 0.01 * max
	if tol < 0.01 {
		tol = 0.01
	}
	r.Logf("")
	r.Logf("  三次账户级采样：")
	r.Logf("    只有多头腿  margin = %.2f", mLongOnly)
	r.Logf("    只有空头腿  margin = %.2f", mShortOnly)
	r.Logf("    双向齐备    margin = %.2f", mBoth)
	r.Logf("    两者之和           = %.2f  （离它 %+.2f）", sum, mBoth-sum)
	r.Logf("    两者较大者         = %.2f  （离它 %+.2f）", max, mBoth-max)
	r.Logf("    判定容差           = %.2f", tol)
	r.Logf("")

	if !scopeDiscriminating(mLongOnly-0.01, mShortOnly-0.01) {
		return fmt.Errorf("⚠️ 有一条腿单独持有时账户 margin 为 0（只多=%.2f 只空=%.2f）——"+
			"该腿没建上。此时「之和」与「较大者」恒等，**判据不成立**，本次结论作废",
			mLongOnly, mShortOnly)
	}

	switch {
	case abs(mBoth-sum) < tol:
		r.Logf("  结论：双向齐备的 margin ≈ 两者之和 → **单向大边未启用**")
		r.Logf("        合上实验 3（跨合约也是之和），本柜台上 %s 完全不走大边：", sym)
		r.Logf("        margin 包按合约逐条算完相加即可，SideScope 这个参数在本样本上无效果。")
		r.Logf("        ⚠️ 「本柜台本品种未启用」≠「大边规则不存在」——")
		r.Logf("           交易所层面 rb 是有大边的，这里量到的是**快期模拟怎么实现**。")
	case abs(mBoth-max) < tol:
		r.Logf("  结论：双向齐备的 margin ≈ 两者较大者 → **单向大边启用，合并范围 = 合约**")
		r.Logf("        合上实验 3（跨合约是之和），两条实验一起把范围钉死在「合约」。")
	default:
		r.Logf("  ⚠️ 两个候选都对不上（离之和 %+.2f，离较大者 %+.2f，容差 %.2f）——",
			mBoth-sum, mBoth-max, tol)
		r.Logf("     说明还有第三种算法。原始截面已落盘，需人工看。")
		r.Logf("     **本实验不收敛**，不得写进规则文档的实测栏。")
	}

	r.Logf("")
	r.Logf("  【口径差】双向持仓时 balance=%.4f ctp_balance=%.4f 差=%.6f",
		kq.MustNum(accBoth, "balance"), kq.MustNum(accBoth, "ctp_balance"),
		kq.MustNum(accBoth, "balance")-kq.MustNum(accBoth, "ctp_balance"))

	if n := cli.OpenLots(); n > 0 {
		return fmt.Errorf("⚠️ 实验结束但账户仍有 %.0f 手持仓，请手工处理", n)
	}
	return nil
}
