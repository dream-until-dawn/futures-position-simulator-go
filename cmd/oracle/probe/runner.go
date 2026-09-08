package probe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// Runner 跑一条实验。
type Runner struct {
	Env     Env
	Symbols []string
	DumpDir string
	Logf    func(string, ...any)

	// Every 是 settle-watch 的采样间隔。其余实验不用它。
	Every time.Duration

	// Specs 是合约规格文件的路径（refdata-sync -specs 的产物）。
	//
	// ⚠️ **没有默认值**，用到它的实验读不到就报错。理由是免费行情**不下发**
	// price_tick：q.PriceTick 恒为 0，而 0 是个看起来完全合理的数。
	// 拿它去构造「不是最小变动价位的整数倍」，构造出来的价格是整数倍，
	// 于是那一项**根本没被违反** —— 而实验照样会跑完、照样打印结果。
	Specs string

	cli *kq.Client
}

// Run 按名称分派。
func (r *Runner) Run(ctx context.Context, exp string) error {
	if len(r.Symbols) == 0 {
		r.Symbols = r.Env.Symbols
	}
	if r.DumpDir == "" {
		r.DumpDir = r.Env.DumpDir
	}
	// ⚠️ 相对的落盘目录随 cwd 走。从 cmd/oracle 里跑就会在 cmd/oracle/testdata
	// 下另开一棵夹具树，而那份夹具与主树的同名文件内容不同、无人知晓。
	// 这里不拒绝相对路径（有人确实会从仓库根跑），但要把它解析成绝对路径**说出来**。
	if r.DumpDir != "" && !filepath.IsAbs(r.DumpDir) {
		abs, _ := filepath.Abs(r.DumpDir)
		r.Logf("⚠️ 落盘目录 %q 是相对路径，随 cwd 走。本次实际落到：%s", r.DumpDir, abs)
		r.DumpDir = abs
	}

	if err := r.connect(ctx); err != nil {
		return err
	}
	defer r.cli.Close()

	switch exp {
	case "status":
		return r.expStatus(ctx)
	case "margin-price":
		return r.expMarginPrice(ctx)
	case "profit-price":
		return r.expMarginPrice(ctx) // 与实验 1 同一次观测，见该函数说明
	case "max-margin-side":
		return r.expMaxMarginSide(ctx)
	case "max-margin-lock":
		return r.expMaxMarginLock(ctx)
	case "fee-rates":
		return r.expFeeRates(ctx)
	case "fee-base":
		return r.expFeeBase(ctx)
	case "fee-form":
		return r.expFeeForm(ctx)
	case "frozen":
		return r.expFrozen(ctx)
	case "settle-check":
		return r.expSettleCheck(ctx)
	case "session-check":
		return r.expSessionCheck(ctx)
	case "settle-watch":
		return r.expSettleWatch(ctx)
	case "close-profit-sign":
		return r.expCloseProfitSign(ctx)
	case "reject-tick-vs-limit":
		return r.expRejectTickVsLimit(ctx)
	case "reject-priority":
		return r.expRejectPriority(ctx)
	case "reject-code":
		return r.expRejectCode(ctx)
	case "overnight-setup":
		return r.expOvernightSetup(ctx)
	case "baseline-full":
		return r.expBaselineFull(ctx)
	case "close-order":
		return r.expCloseOrder(ctx)
	case "yd-vs-his":
		return r.expYdVsHis(ctx)
	case "position-date-type":
		return r.expPositionDateType(ctx)
	case "fee-close-history":
		return r.expFeeCloseHistory(ctx)
	case "shape-both-sides":
		return r.expShapeBothSides(ctx)
	case "position-frozen":
		return r.expPositionFrozen(ctx)
	case "fee-predict":
		return r.expFeePredict(ctx)
	case "avg-price":
		return r.expAvgPrice(ctx)
	case "flatten":
		return r.expFlattenAll(ctx)
	default:
		return fmt.Errorf("未实现的实验: %s", exp)
	}
}

// expStatus 是只读连通性自检。
//
// ⚠️ 它**做到协议层登录**，不是 TCP 层。理由是实测教训：
// 本机连 CTP 前置时，30000–30015 十六个端口全部 accept —— 主机前面有代理，
// TCP 层测试对任何端口都返回成功，毫无判别力。
// 一个总是返回「成功」的检查，和一个正确的检查，在成功样本上长得一模一样。
func (r *Runner) expStatus(ctx context.Context) error {
	cli := r.cli

	// ⚠️ 先等截面**静默**再读。
	//
	// DIFF 是增量 merge patch：登录后字段是一批批到的，
	// 早读一拍就会读到半截截面。实测踩过一次——持仓 4 手而 margin 打印成 0.0000。
	// 0 看起来像个合法数值，不像「还没到」，所以这种错不会自己暴露。
	cli.WaitTrade(1500 * time.Millisecond)
	for i := 0; i < 6 && cli.WaitTrade(700*time.Millisecond); i++ {
	}
	acc := cli.Account()
	pos := cli.Positions()

	// ⚠️ 订阅**被观察合约**的行情，让夹具自足。
	//
	// 昨结算价同时是手续费基准、保证金基准与逐日盯市基线，而它不在业务截面里 ——
	// 不订阅的话，这份夹具存下来之后重算这三样都要去别处找一个数补进来，
	// 而「别处」意味着那个数不属于这份证据，可以被换掉而没人发现。
	if syms := observedSymbols(cli); len(syms) > 0 {
		if err := cli.ConnectQuote(ctx); err != nil {
			// ⚠️ 行情连不上不该让只读自检整个失败，但**必须说出来**：
			// 少了行情的夹具是一份不自足的夹具，而它长得和自足的一样。
			r.Logf("")
			r.Logf("⚠️ 行情网关连不上（%v）—— 本份夹具将**不含行情**，", err)
			r.Logf("   也就是昨结算价缺席，拿它重算手续费/保证金/盯市基线都补不上。")
		} else if err := cli.SubscribeQuotes(syms...); err != nil {
			r.Logf("⚠️ 订阅行情失败：%v —— 本份夹具将不含行情", err)
		} else {
			ready := 0
			for _, sym := range syms {
				if _, ok := cli.WaitQuoteReady(sym, 15*time.Second); ok {
					ready++
				}
			}
			r.Logf("")
			r.Logf("行情已订阅 %d 个合约，就绪 %d 个", len(syms), ready)
			if ready < len(syms) {
				// ⚠️ 缺席本身就是记录，不去补一个空壳。
				r.Logf("⚠️ 有 %d 个合约的行情没就绪 —— 它们在夹具里会**缺席**，"+
					"而缺席比一份填了默认值的行情诚实", len(syms)-ready)
			}
		}
	}

	r.Logf("")
	r.Logf("账户截面 %d 字段", len(acc))
	for _, k := range sortedKeys(acc) {
		if f, ok := kq.Num(acc, k); ok {
			r.Logf("    %-26s %.4f", k, f)
		}
	}

	// ⚠️ 有持仓却 margin=0，是**自相矛盾的截面**，不是一个观测值。
	// 单独看它长得像「这些仓不占保证金」，那是个会被当真的读数。
	if lots := cli.OpenLots(); lots > 0 {
		if m, ok := kq.Num(acc, "margin"); ok && m == 0 {
			r.Logf("")
			r.Logf("⚠️ **截面自相矛盾**：持仓 %.0f 手而 margin=0。", lots)
			r.Logf("   这几乎肯定是读到了尚未收齐的增量截面，**不要拿这一份做任何判定**。")
		}
	}

	// ⚠️ 空仓时 balance == ctp_balance 什么都不证明：
	// 空仓时任何两种权益口径都相等。必须建仓后再比。
	bal, hasBal := kq.Num(acc, "balance")
	ctpBal, hasCTP := kq.Num(acc, "ctp_balance")
	if hasBal && hasCTP {
		if len(pos) == 0 {
			r.Logf("")
			r.Logf("⚠️  balance=%.4f  ctp_balance=%.4f  —— **空仓，相等不是证据**", bal, ctpBal)
			r.Logf("    空仓时任何两种权益口径都相等。这一项要等建仓后才有判别力。")
		} else {
			r.Logf("")
			r.Logf("balance=%.4f  ctp_balance=%.4f  差=%.6f  （有持仓，此比较有效）",
				bal, ctpBal, bal-ctpBal)
		}
	}

	r.Logf("")
	r.Logf("持仓 %d 个", len(pos))
	for _, sym := range sortedKeys(pos) {
		p, _ := pos[sym].(map[string]any)
		r.Logf("    %-20s %d 字段  多今%.0f/多昨%.0f 空今%.0f/空昨%.0f",
			sym, len(p),
			kq.MustNum(p, "volume_long_today"), kq.MustNum(p, "volume_long_his"),
			kq.MustNum(p, "volume_short_today"), kq.MustNum(p, "volume_short_his"))
	}
	r.Logf("委托 %d 条，成交 %d 条", len(cli.Orders()), len(cli.Trades()))

	if len(r.Symbols) > 0 {
		if err := cli.ConnectQuote(ctx); err != nil {
			return err
		}
		if err := cli.SubscribeQuotes(r.Symbols...); err != nil {
			return err
		}
		r.Logf("")
		for _, s := range r.Symbols {
			q, ok := cli.WaitQuoteReady(s, 20*time.Second)
			if !ok {
				r.Logf("    %-18s 行情未就绪（可能非交易时段，或合约代码写错）", s)
				continue
			}
			r.Logf("    %-18s 乘数=%.0f tick=%.4f 最新=%.2f 涨停=%.2f 跌停=%.2f 昨结=%.2f 每手保证金=%.2f 每手手续费=%.4f",
				s, q.VolumeMultiple, q.PriceTick, q.LastPrice, q.UpperLimit, q.LowerLimit,
				q.PreSettlement, q.MarginPerLot, q.CommissionPerLot)
		}
	}

	return r.dump("status", "只读连通性自检")
}

// dump 把当前截面脱敏后落盘。
//
// ⚠️ 脱敏在**落盘之前**做。先落原始盘再擦，原始文件已经上过磁盘、
// 可能已经进过 git index —— 一次 git add -A 就够了。
func (r *Runner) dump(name, note string) error {
	cli := r.cli
	// ⚠️ 行情只留**被观察到的合约**：持仓里出现过的、或成交里出现过的。
	// 整份行情有几万个合约，而夹具是证据不是数据库。
	f := kq.Sanitize(cli.Account(), cli.Positions(), cli.Trades(),
		observedQuotes(cli), cli.Orders(), cli.TradingDay(),
		time.Now().Format(time.RFC3339), note)

	// 独立复查：与白名单是两套不同原理的机制，因此不会一起失效。
	secrets := append(r.Env.Secrets(), kq.Secret{Name: "authID", Value: cli.AuthID()})
	if bs := kq.BlindSpots(secrets); len(bs) > 0 {
		// ⚠️ 明说查不了什么，免得「没报错」被读成「都查过了」。
		r.Logf("  ⓘ 独立复查的盲区：%v —— 这几个值太短，在夹具里搜它们只会撞上价格数字，", bs)
		r.Logf("    没有判别力。它们靠白名单挡，不靠这一层。")
	}
	if err := kq.Scrubbed(f, secrets); err != nil {
		return fmt.Errorf("脱敏自检失败，**不落盘**：%w", err)
	}
	if r.DumpDir == "" {
		r.Logf("(未指定落盘目录，跳过)")
		return nil
	}
	if err := os.MkdirAll(r.DumpDir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	path, err := freeFixturePath(r.DumpDir, name, cli.TradingDay(), b, r.Logf)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	r.Logf("")
	// ⚠️ 打**绝对**路径。相对路径的落盘目录随 cwd 走，从 cmd/oracle 里跑
	// 会在 cmd/oracle/testdata 下另开一份，日志里只写 "testdata\probes\..."
	// 看不出它落在哪棵树上——踩过一次，多出一份没人知道的重复夹具。
	shown := path
	if abs, err := filepath.Abs(path); err == nil {
		shown = abs
	}
	r.Logf("夹具落盘 %s（%d 字节，未分类字段 %d 个）", shown, len(b), len(f.Unclassified))
	return nil
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// freeFixturePath 挑一个不会**静默覆盖**已有证据的落盘路径。
//
// ⚠️ 起因是一次真实的静默数据丢失：同一天里 fee-form 跑了两趟，
// 一趟豆粕一趟螺纹——两个**不同的样本**，得出的还是两个**不同的结论**。
// 文件名只带「实验名 + 交易日」，第二趟把第一趟整份盖掉了，没有任何提示。
//
// 覆盖本身不总是错的：同一条实验重跑一遍、想要最新那份，是常见需求。
// 错的是**分不出这两种情形还一律覆盖**。所以这里按内容判断：
//
//	文件不存在        → 就用这个名字
//	存在且内容一致    → 就用这个名字（重跑得到同样的数，覆盖它没有损失）
//	存在且内容不同    → 换一个带序号的名字，并**吼出来**
func freeFixturePath(dir, name, tradingDay string, content []byte, logf func(string, ...any)) (string, error) {
	base := filepath.Join(dir, fmt.Sprintf("%s-%s", name, tradingDay))
	for i := 1; i <= 99; i++ {
		p := base + ".json"
		if i > 1 {
			p = fmt.Sprintf("%s-%d.json", base, i)
		}
		old, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			if i > 1 {
				logf("⚠️ 同名夹具已存在且**内容不同**，本份另存为 %s", filepath.Base(p))
				logf("   （同一天同一实验跑了不同的样本？两份都是证据，不该互相覆盖）")
			}
			return p, nil
		}
		if err != nil {
			return "", err
		}
		if bytes.Equal(old, content) {
			return p, nil // 内容一致，覆盖它没有损失
		}
	}
	return "", fmt.Errorf("%s 已有 99 份同名夹具，先清理再跑", base)
}

// observedQuotes 挑出「本次截面里出现过的合约」的行情。
//
// ⚠️ 判据是**持仓或成交里出现过**，不是「订阅过」：
// 订阅过而没持仓也没成交的合约，它的行情对这份证据没有用；
// 而持仓里有、却因为没订阅所以没行情的合约，会在这里**缺席** ——
// 那是一个真实的缺口，缺席本身就是它的记录，不去补一个空壳。
func observedQuotes(cli *kq.Client) map[string]any {
	all, _ := kq.Dig(cli.QuoteSnapshot(), "quotes").(map[string]any)
	out := map[string]any{}
	for _, sym := range observedSymbols(cli) {
		if q, ok := all[sym]; ok {
			out[sym] = q
		}
	}
	return out
}

// observedSymbols 列出本次截面里出现过的合约，升序。
//
// ⚠️ **委托里的合约也算**。这一条是补的：原先只数持仓与成交，
// 于是一份「只下过单、全被拒、没有持仓也没有成交」的截面
// （reject 系列的实验正是这个形状）落盘时**整个行情段是空的**。
// 后果不是报错，是下游对拍**静默跳过**那份夹具 ——
// 而「跳过了一份」与「比过了一份且一致」在汇总行里长得一模一样。
// 20260909 的 exp-reject-tick-vs-limit-20260909-3.json 就是这么丢掉
// DCE.i2701 的昨结算价的，那份夹具因此永远比不了金额侧冻结。
func observedSymbols(cli *kq.Client) []string {
	want := map[string]bool{}
	for sym := range cli.Positions() {
		want[sym] = true
	}
	for _, raw := range cli.Orders() {
		o, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ex, _ := o["exchange_id"].(string)
		inst, _ := o["instrument_id"].(string)
		if ex != "" && inst != "" {
			want[ex+"."+inst] = true
		}
	}
	for _, raw := range cli.Trades() {
		t, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ex, _ := t["exchange_id"].(string)
		inst, _ := t["instrument_id"].(string)
		if ex != "" && inst != "" {
			want[ex+"."+inst] = true
		}
	}
	out := make([]string, 0, len(want))
	for sym := range want {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out
}

// LiveFixtureJSON 连上柜台，取一份**此刻的**截面，脱敏后 marshal 成夹具 JSON。
//
// ⚠️ 它走的是与 dump **完全同一条**制备路径（Sanitize → BlindSpots → Scrubbed），
// 只是不落盘。分成两条路会让实时对拍与存档证据看到不同的字节，
// 而那种分歧的表现是「同一个柜台在两条路上给出不同的判定」。
//
// ⚠️ **脱敏照做，即使不落盘**：这份字节会进日志、进报告、可能被贴出去。
// 「反正不写文件」不是跳过脱敏的理由 —— 泄漏的是内容，不是文件。
func (r *Runner) LiveFixtureJSON(ctx context.Context, symbols []string) ([]byte, error) {
	if err := r.connect(ctx); err != nil {
		return nil, err
	}
	defer r.cli.Close()
	if len(symbols) > 0 {
		if err := r.cli.ConnectQuote(ctx); err != nil {
			return nil, err
		}
		if err := r.cli.SubscribeQuotes(symbols...); err != nil {
			return nil, err
		}
		for _, s := range symbols {
			// ⚠️ 行情没到齐就往下走，昨结算价会缺 —— 而它是手续费基准、
			// 保证金基准与逐日盯市基线。等不到就说出来，不静默继续。
			if _, ok := r.cli.WaitQuoteReady(s, 20*time.Second); !ok {
				r.Logf("⚠️ %s 行情未就绪 —— 该合约的昨结算价会缺，"+
					"保证金那一层比不了（不是「比对通过」）", s)
			}
		}
	}
	// ⚠️ 先等截面静默再读。DIFF 是增量 merge patch，早读一拍会读到半截截面，
	// 而半截截面里的 0 看起来像一个合法数值。
	r.cli.WaitTrade(1500 * time.Millisecond)

	f := kq.Sanitize(r.cli.Account(), r.cli.Positions(), r.cli.Trades(),
		observedQuotes(r.cli), r.cli.Orders(), r.cli.TradingDay(),
		time.Now().Format(time.RFC3339),
		"实时对拍取的截面（oracle conformance），**未落盘**")

	secrets := append(r.Env.Secrets(), kq.Secret{Name: "authID", Value: r.cli.AuthID()})
	if bs := kq.BlindSpots(secrets); len(bs) > 0 {
		r.Logf("  ⓘ 独立复查的盲区：%v —— 这几个值太短，没有判别力；靠白名单挡", bs)
	}
	if err := kq.Scrubbed(f, secrets); err != nil {
		return nil, fmt.Errorf("脱敏自检失败，**不输出**：%w", err)
	}
	return json.Marshal(f)
}

// connect 建连接并登录。
//
// ⚠️ 抽成方法是因为**有两条路要用它**：Run（跑实验）与 LiveFixtureJSON
// （实时对拍取截面）。写两份连接逻辑，两份就会漂移 ——
// 而漂移的表现是「实验能连上、对拍连不上」，或者反过来，
// 且两边的错误信息各说各话。
//
// ⚠️ 它**不**负责 Close：谁开的谁关，调用方 defer。
func (r *Runner) connect(ctx context.Context) error {
	cli := kq.New(kq.Credentials{
		User: r.Env.KQUser, Password: r.Env.KQPassword,
		ClientSecret: r.Env.KQClientSecret,
	}, r.Logf)
	r.cli = cli
	if err := cli.Auth(ctx); err != nil {
		return err
	}
	if err := cli.ConnectTrade(ctx); err != nil {
		return err
	}
	// ⚠️ 登录成功与否看**状态**，不看回调。等 trade 截面出现即为登录成功。
	if !cli.WaitUntil(30*time.Second, func() bool { return cli.TradingDay() != "" }) {
		for _, n := range cli.Notifies() {
			r.Logf("  notify code=%d level=%s %s", n.Code, n.Level, n.Content)
		}
		return fmt.Errorf("30 秒内未拿到交易截面 —— " +
			"这只说明没等到，不说明登录失败；先看上面的 notify")
	}
	r.Logf("[td] 登录成功  trading_day=%s", cli.TradingDay())
	return nil
}
