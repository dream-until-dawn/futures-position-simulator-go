package probe

import (
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

	cli := kq.New(kq.Credentials{User: r.Env.KQUser, Password: r.Env.KQPassword, ClientSecret: r.Env.KQClientSecret}, r.Logf)
	r.cli = cli
	defer cli.Close()

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
		return fmt.Errorf("30 秒内未拿到交易截面 —— 这只说明没等到，不说明登录失败；先看上面的 notify")
	}
	r.Logf("[td] 登录成功  trading_day=%s", cli.TradingDay())

	switch exp {
	case "status":
		return r.expStatus(ctx)
	case "margin-price":
		return r.expMarginPrice(ctx)
	case "profit-price":
		return r.expMarginPrice(ctx) // 与实验 1 同一次观测，见该函数说明
	case "max-margin-side":
		return r.expMaxMarginSide(ctx)
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
	acc := cli.Account()
	pos := cli.Positions()

	r.Logf("")
	r.Logf("账户截面 %d 字段", len(acc))
	for _, k := range sortedKeys(acc) {
		if f, ok := kq.Num(acc, k); ok {
			r.Logf("    %-26s %.4f", k, f)
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
	f := kq.Sanitize(cli.Account(), cli.Positions(), cli.TradingDay(),
		time.Now().Format(time.RFC3339), note)

	// 独立复查：与白名单是两套不同原理的机制，因此不会一起失效。
	if err := kq.Scrubbed(f, append(r.Env.Secrets(), cli.AuthID())); err != nil {
		return fmt.Errorf("脱敏自检失败，**不落盘**：%w", err)
	}
	if r.DumpDir == "" {
		r.Logf("(未指定落盘目录，跳过)")
		return nil
	}
	if err := os.MkdirAll(r.DumpDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(r.DumpDir, fmt.Sprintf("%s-%s.json", name, cli.TradingDay()))
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	r.Logf("")
	r.Logf("夹具落盘 %s（%d 字节，未分类字段 %d 个）", path, len(b), len(f.Unclassified))
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
