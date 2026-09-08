// Command oracle 是本项目的「真值来源」工具。
//
// oracle = test oracle，**判定真值的那一方**，不是数据库。
//
// 它是一个**独立嵌套模块**（自带 go.mod）：连柜台要 WebSocket 客户端与 CTP 绑定，
// 后者还自带数十 MB 二进制。若把它们写进主模块，即便使用者从不引用本工具，
// 依赖仍会出现在他们的模块图里——而这件事**不会有任何报错**，
// 是使用者 `go get` 之后才在自己的模块图里看见。
//
// 两个子命令：
//
//	oracle probe        跑判别实验，产出证据
//	oracle conformance  逐字段对拍（v0.2.0 起）
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

func usage() {
	fmt.Fprint(os.Stderr, `oracle —— 判定真值的那一方

用法:
  oracle probe -exp <名称> [-symbols a,b] [-env 路径]
  oracle whitelist                 打印脱敏白名单，供评审逐键核对
  oracle status                    只读：登录并打印账户与持仓截面

实验名称（当日即可跑）:
  status           连通性自检（做到协议层登录，不是 TCP 层）
  reject-code      实验 6：报单被拒时柜台的原话
  max-margin-side  实验 3：单向大边按品种还是按合约合并（需两个同品种不同月份合约）
  max-margin-lock  实验 3b：同一合约双向持仓下，单向大边启没启用
  margin-price     实验 1/2 今仓版：只能排除「连续重估」这一个候选
  profit-price     margin-price 的另一面，同一次观测（见 expMarginPrice 说明）
  flatten          把账户平回空仓

手续费:
  fee-rates        快期模拟是全局一个口径，还是分品种的真实费率
  fee-form         分开两种收法：每手固定额 与 按昨结算价比例
  fee-base         按额那档的基准价是成交价还是昨结算价
  fee-predict      把「昨结算价 × 乘数 × 品种费率」变成一次可证伪的预测

挂单与冻结:
  frozen           账户侧：一笔挂得住的委托冻结了什么，怎么进 Available
  position-frozen  持仓侧：一笔挂着的平仓单冻的是 volume_*_frozen_today 还是 _his
                   ⚠️ 与 frozen 不是一回事，前者量 FrozenMargin

结算与时段（判「发生了没有」，不猜）:
  settle-check     第 0 步：结算到底发生了没有，今昨仓滚了没有
  session-check    settle-check 的对照组：跨时段边界但不跨结算
  settle-watch     定时采样，只在被盯的字段真的变了时记一次（配 -every）

需要昨仓（先在前一交易日跑 overnight-setup，等一次结算）:
  overnight-setup  建立过夜种子
  baseline-full    实验 1b/2 完整版：margin 与 position_profit 各自的价格基线
  close-order      实验 4：NoUseHistory 合约的平仓消耗顺序
  yd-vs-his        实验 7：volume_long_yd 与 volume_long_his 的差别
  position-date-type  这个合约区不区分今昨仓（只能靠柜台行为测，字典里没有）
  avg-price        让加权均价第一次被真正考验（需多笔不同价的开仓）
  close-profit-sign  构造一笔为正的平仓盈亏，分开 CTP 的两种符号约定

尚未实现:
  fee-rounding     实验 5：手续费取整口径 —— 需要 CTP 的六个费率，天勤只给「每手」
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "whitelist":
		fmt.Print(kq.WhitelistReport())
	case "probe", "status":
		if err := runProbe(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	case "conformance":
		fmt.Fprintln(os.Stderr, "conformance 从 v0.2.0 起提供，现在还没有实现可对拍")
		os.Exit(2)
	default:
		usage()
		os.Exit(2)
	}
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet(args[1], flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	exp := fs.String("exp", "status", "实验名称")
	symbols := fs.String("symbols", "", "实验用合约，逗号分隔，形如 SHFE.rb2601")
	// ⚠️ 默认值必须是空串。
	//
	// 它原先默认 "testdata/probes"，于是 Runner 里那句
	// 「DumpDir 为空才回落到 .env」永远不成立 —— .env 的 PROBE_DUMP_DIR
	// 从来没被读过。我为了修「相对路径另开夹具树」把 .env 改成绝对路径，
	// 改完毫无效果，而**唯一发现这件事的是那条机械守卫**
	// （根包 TestNoStrayFixtureTrees）。一个什么也没改的修复，
	// 在日志里和一个生效的修复长得一模一样。
	dump := fs.String("dump", "", "夹具落盘目录（留空则用 .env 的 PROBE_DUMP_DIR）")
	timeout := fs.Duration("timeout", 90*time.Second, "整体超时（settle-watch 用它当观察时长）")
	every := fs.Duration("every", 5*time.Second, "settle-watch 的采样间隔")
	if args[1] == "status" {
		_ = fs.Parse(args[2:])
		*exp = "status"
	} else {
		_ = fs.Parse(args[2:])
	}

	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var syms []string
	for _, s := range strings.Split(*symbols, ",") {
		if s = strings.TrimSpace(s); s != "" {
			syms = append(syms, s)
		}
	}

	r := &probe.Runner{
		Env:     env,
		Symbols: syms,
		DumpDir: *dump,
		Every:   *every,
		Logf:    func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}
	return r.Run(ctx, *exp)
}
