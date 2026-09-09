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
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/dream-until-dawn/futures-position-simulator-go/conformance/fixture"
	"github.com/shopspring/decimal"
)

func usage() {
	fmt.Fprint(os.Stderr, `oracle —— 判定真值的那一方

用法:
  oracle probe -exp <名称> [-symbols a,b] [-env 路径]
  oracle whitelist                 打印脱敏白名单，供评审逐键核对
  oracle ctp-params [-env 路径]    ⚠️ **CTP/SimNow 侧**：查经纪商交易参数
                                   （simnow_pending#9）。只读，不下单。
                                   ⚠️ 查到的是**声明**不是**行为**，见 docs/ctp-oracle.md 第 3 节
  oracle status                    只读：登录并打印账户与持仓截面
  oracle conformance -specs <合约规格.json> -rules <实测规则.json>
                                   **连着柜台**取此刻的截面，与本库逐字段比
                                   ⚠️ 只读，不下单；与主模块那批离线对拍走同一套比对代码

实验名称（当日即可跑）:
  status           连通性自检（做到协议层登录，不是 TCP 层）
  reject-code      实验 6：报单被拒时柜台的原话
  reject-priority  拒绝优先级：一笔单同时违反两项时柜台报哪一个（⚠️ 需要 -specs）
  reject-tick-vs-limit  越涨停 vs 非整数倍：固定越界幅度只扫价格零头（⚠️ 需要 -specs）
  reject-tradable  合约不可交易 vs 价格类校验：谁先被报出来（⚠️ 需要 -specs）
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
  fee-close-history  平昨那一档（⚠️ 真平仓，吃掉一手昨仓；需要昨仓≥2 手）

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
  shape-both-sides  把一个有昨仓的合约摆成「多头今昨并存 + 空头今仓」——
                   ⚠️ 真建仓（对手价成交），它造的是别的实验缺的前提
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
	case "ctp-hold":
		if err := runCTPHold(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	case "ctp-flatten":
		if err := runCTPFlatten(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	case "ctp-roundtrip":
		if err := runCTPRoundTrip(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	case "ctp-order":
		if err := runCTPOrder(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	case "ctp-dup":
		if err := runCTPDup(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	case "ctp-params":
		if err := runCTPParams(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	case "conformance":
		if err := runConformance(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
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
	specs := fs.String("specs", "", "合约规格文件路径（reject-priority 用它取最小变动价位；⚠️ 无默认值）")
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
		Specs:   *specs,
		Every:   *every,
		Logf:    func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}
	return r.Run(ctx, *exp)
}

// runConformance 是**连着柜台**的逐字段对拍。
//
// ⚠️ 它与主模块里那批离线对拍的分工写在 conformance 包的包注释里。
// 这里只强调一句：本命令**只读**，不下任何单。
func runConformance(args []string) error {
	fs := flag.NewFlagSet("conformance", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	specsPath := fs.String("specs", "", "合约规格快照（refdata-sync -specs 的产物）")
	rulesPath := fs.String("rules", "", "实测规则（保证金率与 PositionDateType）")
	carryPath := fs.String("carry", "", "前一交易日的夹具 —— 没有它，今天之前开的仓一律比不了")
	settlePath := fs.String("settle", "", "**交易所**给的结算价（cmd/settlement -out 的产物）")
	symbols := fs.String("symbols", "", "要订阅行情的合约，逗号分隔；留空则用 .env 的")
	timeout := fs.Duration("timeout", 90*time.Second, "整体超时")
	// ⚠️ 没有默认路径：**落盘是一次显式的决定**。这条命令平时只读、不落盘
	// （LiveFixtureJSON 的注释写着理由：实时那条路不该悄悄往夹具树里加东西）。
	// 而要把一次实时对拍**当作产物交给评审**时，报告之外还需要那一刻的原始截面 ——
	// 「我验产物，不验你的转述」是这个仓库一贯的做法，对实时那条路也该成立。
	dump := fs.String("dump", "", "把这次用到的**实时截面**落盘到该目录（⚠️ 无默认值）")
	_ = fs.Parse(args[2:])

	// ⚠️ 两份规则数据都**必须显式给**，没有默认路径。
	//
	// 一个默认路径会让人以为「跑起来了就是对的」，而它可能指向一份
	// 过期的、或者别的交易日的快照 —— 那时对拍照样跑完，
	// 只是每一个金额都基于错的乘数或费率。
	if *specsPath == "" || *rulesPath == "" {
		return fmt.Errorf("-specs 与 -rules 都必须给 —— " +
			"⚠️ 不设默认路径：默认值会让人以为「跑起来了就是对的」，" +
			"而它可能指向一份过期的或别的交易日的快照，" +
			"那时对拍照样跑完，只是每个金额都基于错的乘数或费率")
	}
	specsFile, err := os.Open(*specsPath)
	if err != nil {
		return err
	}
	defer specsFile.Close()
	specs, err := fixture.LoadSpecs(specsFile)
	if err != nil {
		return err
	}
	rulesFile, err := os.Open(*rulesPath)
	if err != nil {
		return err
	}
	defer rulesFile.Close()
	rules, err := conformance.LoadMeasuredRules(rulesFile)
	if err != nil {
		return err
	}
	built := conformance.BuildSpecs(specs, rules)
	fmt.Printf("规则数据：字典 %d 个合约、实测保证金率 %d 个品种、"+
		"实测 PositionDateType %d 个合约 → 可对拍 %d 个合约\n",
		len(specs), len(rules.MarginByProduct), len(rules.PositionDate), len(built))

	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	var syms []string
	for _, s := range strings.Split(*symbols, ",") {
		if s = strings.TrimSpace(s); s != "" {
			syms = append(syms, s)
		}
	}
	if len(syms) == 0 {
		syms = env.Symbols
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	r := &probe.Runner{
		Env: env, Symbols: syms,
		Logf: func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}
	raw, err := r.LiveFixtureJSON(ctx, syms)
	if err != nil {
		return err
	}
	if *dump != "" {
		path, derr := probe.WriteFixtureJSON(*dump, "live-conformance", raw,
			func(f string, a ...any) { fmt.Printf(f+"\n", a...) })
		if derr != nil {
			// ⚠️ 落盘失败**就是失败**，不降级成警告：这条路存在的理由
			// 就是产出可核对的证据，产不出来时说「跑过了」等于回到转述。
			return fmt.Errorf("落盘实时截面：%w", derr)
		}
		fmt.Printf("实时截面已落盘 %s\n", path)
	}
	carry, err := loadCarry(*carryPath, *settlePath)
	if err != nil {
		return err
	}
	res, err := conformance.Compare(raw, built, carry)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Print(res.String())

	// ⚠️ 有失败就用非零退出码：这条命令会被脚本/CI 调用，
	// 而一个永远返回 0 的对拍工具，与不跑它没有区别。
	// ⚠️ **只在第三类上返回非零**：已登记的口子差异每次都在，
	// 拿它们让命令失败，会让这个工具永远红 —— 而永远红与永远绿一样会被无视。
	if len(res.NovelFailures) > 0 {
		return fmt.Errorf("⚠️ 有 %d 个**第三类**失败（新出现、不在已知差异登记表里）：%s",
			len(res.NovelFailures), strings.Join(res.NovelFailures, " "))
	}
	return nil
}

// loadCarry 读「前一交易日的夹具」与「交易所结算价」。
//
// ⚠️ 两个都不给时返回 nil —— 那是合法的（只对拍当日开的仓），
// 而 Compare 会把够不着的合约**记数报出来**，不会假装比过了。
//
// ⚠️ 只给一个是**错误**，不是「用一半」：结转要两样齐全，
// 缺一样时静默降级会让人以为结转跑过了。
func loadCarry(fixturePath, settlePath string) (*conformance.Carry, error) {
	if fixturePath == "" && settlePath == "" {
		return nil, nil
	}
	if fixturePath == "" || settlePath == "" {
		return nil, fmt.Errorf("-carry 与 -settle 必须**成对**给 —— " +
			"结转要「前一日的持仓与成交」和「交易所的结算价」两样齐全；" +
			"⚠️ 只给一样时静默降级，会让人以为结转跑过了")
	}
	pf, err := os.Open(fixturePath)
	if err != nil {
		return nil, err
	}
	defer pf.Close()
	prev, err := fixture.Load(pf, filepath.Base(fixturePath))
	if err != nil {
		return nil, fmt.Errorf("读前一日夹具：%w", err)
	}
	sf, err := os.Open(settlePath)
	if err != nil {
		return nil, err
	}
	defer sf.Close()
	var raw struct {
		Source      string `json:"source"`
		TradingDay  string `json:"trading_day"`
		Note        string `json:"note"`
		Settlements []struct {
			Instrument    string `json:"instrument"`
			Settlement    string `json:"settlement"`
			PreSettlement string `json:"pre_settlement"`
			Close         string `json:"close"`
		} `json:"settlements"`
	}
	if err := json.NewDecoder(sf).Decode(&raw); err != nil {
		return nil, fmt.Errorf("读交易所结算价：%w", err)
	}
	// ⚠️ 结算价那份文件的交易日必须与前一日夹具**一致**。
	// 拿错一天的结算价不会有任何动静 —— 数看起来完全正常，只是错了一天。
	if raw.TradingDay != prev.TradingDay.String() {
		return nil, fmt.Errorf("⚠️ 结算价文件是交易日 %s，而前一日夹具是 %s —— "+
			"拿错一天的结算价不会有任何动静：数看起来完全正常，只是错了一天",
			raw.TradingDay, prev.TradingDay)
	}
	m := map[string]decimal.Decimal{}
	for _, r := range raw.Settlements {
		d, err := decimal.NewFromString(r.Settlement)
		if err != nil || !d.IsPositive() {
			continue // ⚠️ 非正的结算价不放进去：它会让逐日盯市把持仓算成归零
		}
		m[r.Instrument] = d
	}
	if len(m) == 0 {
		return nil, fmt.Errorf("⚠️ 结算价文件里一个可用的结算价都没有")
	}
	fmt.Printf("结转输入：前一日夹具 %s（交易日 %s）、交易所结算价 %d 个合约\n",
		filepath.Base(fixturePath), prev.TradingDay, len(m))
	return &conformance.Carry{Prev: prev, Settlement: m}, nil
}

// runCTPParams 查 SimNow 的经纪商交易参数（simnow_pending#9）。
//
// ⚠️ 它**只读**：连接、认证、登录、确认结算单、查询，不下单。
// 报单要等 docs/ctp-oracle.md 的 P3。
func runCTPParams(args []string) error {
	fs := flag.NewFlagSet("ctp-params", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	timeout := fs.Duration("timeout", 40*time.Second, "整条链路的超时")
	dump := fs.String("dump", "", "落盘目录（⚠️ **无默认值**；CTP 夹具要落 testdata/ctp/，"+
		"别落进 testdata/probes —— 那是天勤 DIFF 的语料，混进去**不会报错**）")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	c := ctp.New(ctp.Credentials{
		Front:    env.CTPTdFront,
		BrokerID: env.CTPBrokerID,
		UserID:   env.CTPUserID,
		Password: env.CTPPassword,
		AppID:    env.CTPAppID,
		AuthCode: env.CTPAuthCode,
	}, func(f string, a ...any) { fmt.Printf(f+"\n", a...) })
	defer c.Close()

	if err := c.Connect(*timeout); err != nil {
		return err
	}
	if *dump != "" {
		// ⚠️ 落盘这一支**先脱敏再写**，且凭据复查在 Write 里面做 ——
		// 一个「记得先查一下」的约定，与没有这道检查在出事那天是一样的。
		fx, err := c.Capture(*timeout, "ctp-params + 账户 + 持仓")
		if err != nil {
			return err
		}
		secrets := map[string]string{
			"CTP_USER_ID": env.CTPUserID, "CTP_PASSWORD": env.CTPPassword,
			"CTP_BROKER_ID": env.CTPBrokerID, "CTP_APP_ID": env.CTPAppID,
			"CTP_AUTH_CODE": env.CTPAuthCode,
		}
		if _, err := fx.Write(*dump, "ctp-status", secrets,
			func(f string, a ...any) { fmt.Printf(f+"\n", a...) }); err != nil {
			return err
		}
		fmt.Printf("  账户字段 %d 个、持仓 %d 条、去掉的键 %d 个\n",
			len(fx.Account), len(fx.Positions), len(fx.Dropped))
		return nil
	}

	p, err := c.BrokerParams(*timeout)
	if err != nil {
		return err
	}
	fmt.Println()
	fmt.Println("经纪商交易参数（simnow_pending#9）")
	fmt.Printf("    MarginPriceType          %q  %s\n", string(p.MarginPriceType), marginPriceTypeName(p.MarginPriceType))
	fmt.Printf("    Algorithm                %q  %s\n", string(p.Algorithm), algorithmName(p.Algorithm))
	fmt.Printf("    AvailIncludeCloseProfit  %q  %s\n", string(p.AvailIncludeCloseProfit), includeCloseProfitName(p.AvailIncludeCloseProfit))
	fmt.Println()
	fmt.Println("⚠️ 这是**声明**不是**行为**：查到的是柜台配置成什么，")
	fmt.Println("   而「它是否真按这个算」要有持仓才验得了。见 docs/ctp-oracle.md 第 3 节。")
	return nil
}

// 下面三个把 CTP 的字符枚举翻成人看得懂的话。
//
// ⚠️ 每一个都带 default：一个没见过的取值必须**显式说出来**，
// 而不是打印一个空字符串 —— 后者与「这一档没有名字」长得一样。
func marginPriceTypeName(v def.TThostFtdcMarginPriceTypeType) string {
	switch v {
	case def.THOST_FTDC_MPT_PreSettlementPrice:
		return "昨结算价"
	case def.THOST_FTDC_MPT_SettlementPrice:
		return "今结算价"
	case def.THOST_FTDC_MPT_AveragePrice:
		return "均价"
	case def.THOST_FTDC_MPT_OpenPrice:
		return "开仓价"
	}
	return "⚠️ 没见过的取值"
}

func algorithmName(v def.TThostFtdcAlgorithmType) string {
	switch v {
	case def.THOST_FTDC_AG_All:
		return "浮盈浮亏都计算"
	case def.THOST_FTDC_AG_OnlyLost:
		return "只计浮动亏损"
	case def.THOST_FTDC_AG_OnlyGain:
		return "只计浮动盈利"
	case def.THOST_FTDC_AG_None:
		return "都不计"
	}
	return "⚠️ 没见过的取值"
}

func includeCloseProfitName(v def.TThostFtdcIncludeCloseProfitType) string {
	switch v {
	case def.THOST_FTDC_ICP_Include:
		return "可用**包含**平仓盈利"
	case def.THOST_FTDC_ICP_NotInclude:
		return "可用**不含**平仓盈利"
	}
	return "⚠️ 没见过的取值"
}

// ctpValve 从 .env 造 CTP 侧的下单安全阀。
//
// ⚠️ **它是 CTP 侧唯一构造 Valve 的地方**，理由与 kq 那侧的 guard() 相同：
// 这个模块栽过「抽了纯函数、配了测试、忘了接线」的跟头，而**接线那一步
// 在被省略时是不可见的**。守卫 TestCTPValveCarriesEnv 从这里出发验到拒绝。
//
// ⚠️ Protected 目前为空：过夜种子在快期那侧，SimNow 账户是空的。
// 空着不等于不接 —— 接线本身要被验着，见那条守卫。
func ctpValve(env probe.Env, legs []safety.ProtectedLeg) safety.Valve {
	return safety.Valve{
		AllowOrder: env.AllowOrder,
		MaxVolume:  env.MaxVolume,
		Protected:  legs,
	}
}

// runCTPOrder 是 docs/ctp-oracle.md 的 P3：**一次完整的报单往返**。
//
//	查行情拿涨跌停 → 发一笔挂得上但成不了的限价单 → 确认它挂着 → 撤单 → 确认释放
//
// ⚠️ 验收是「账户回到起点，差额只应是手续费」。而挂单不成交**不产生手续费**，
// 所以这一轮的差额应当是 **0**；⚠️ 若不是 0，那本身是一个观测，不是失败。
func runCTPOrder(args []string) error {
	fs := flag.NewFlagSet("ctp-order", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 SHFE.rb2701（⚠️ 无默认值）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：一笔真实委托的合约必须显式指定")
	}
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
	c := ctp.New(ctp.Credentials{
		Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID,
		Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode,
	}, logf)
	c.Valve = ctpValve(env, nil)
	defer c.Close()

	if err := c.Connect(*timeout); err != nil {
		return err
	}
	logf("[P3] 安全阀：AllowOrder=%v MaxVolume=%d", env.AllowOrder, env.MaxVolume)

	before, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	logf("[P3] 起点  balance=%.4f available=%.4f 冻结保证金=%.4f 手续费=%.4f",
		float64(before.Balance), float64(before.Available),
		float64(before.FrozenMargin), float64(before.Commission))

	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	ex, inst := ctp.SplitSymbol(*symbol)
	req := ctp.OrderReq{
		Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: ctp.FarPrice(md, def.THOST_FTDC_D_Buy),
	}
	logf("[P3] 行情  最新=%.2f 涨停=%.2f 跌停=%.2f  ⇒ 买开挂在跌停 %.2f（挂得上、成不了）",
		float64(md.LastPrice), float64(md.UpperLimitPrice), float64(md.LowerLimitPrice),
		req.LimitPrice)

	st, err := c.Insert(req, *timeout)
	if err != nil {
		return fmt.Errorf("报单：%w", err)
	}
	logf("[P3] 报单回报  status=%q 挂着=%v  %s", string(st.Status), st.Alive(), st.StatusMsg)
	if !st.Alive() {
		// ⚠️ 没挂上就没有可撤的东西 —— 这一轮**没有验到往返**，要说清楚而不是当成功。
		return fmt.Errorf("⚠️ 这笔单没有挂上（status=%q %s）—— "+
			"**本轮没有验到完整往返**，撤单那一步根本走不到", string(st.Status), st.StatusMsg)
	}

	held, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	logf("[P3] 挂着时  available=%.4f 冻结保证金=%.4f 冻结手续费=%.4f",
		float64(held.Available), float64(held.FrozenMargin), float64(held.FrozenCommission))

	if err := c.Cancel(st.OrderRef, req); err != nil {
		return err
	}
	time.Sleep(2 * time.Second)

	after, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	dBal := float64(after.Balance) - float64(before.Balance)
	dFroz := float64(after.FrozenMargin) - float64(before.FrozenMargin)
	logf("[P3] 撤单后  balance=%.4f（差 %+.4f）冻结保证金=%.4f（差 %+.4f）",
		float64(after.Balance), dBal, float64(after.FrozenMargin), dFroz)
	fmt.Println()
	switch {
	case dBal == 0 && dFroz == 0:
		fmt.Println("⇒ ✅ 完整往返：报单 → 挂上 → 撤单 → **账户回到起点**（挂单不成交，差额应为 0）")
	default:
		// ⚠️ 不判失败：差额本身是观测。判失败会让一个真实现象被读成 bug。
		fmt.Printf("⇒ ⚠️ 往返走完了，但账户**没有回到起点**：结存差 %+.4f、冻结差 %+.4f\n", dBal, dFroz)
		fmt.Println("   这是一个**观测**，不是失败 —— 去查它为什么不为零，别直接改判据")
	}
	return nil
}

// runCTPRoundTrip 是 P4 的第一步：**一笔真正成交的往返**。
//
//	买开（涨停价，保证成交）→ 拍持仓 → 卖平今（跌停价，保证成交）→ 拍账户
//
// ⚠️ 它与 P3 的区别是**会真的建仓**。因此三条安全设计写在这里：
//
//	① 手数固定 1，且仍然过安全阀（MaxVolume=1）
//	② 平仓用 CloseToday —— SHFE 当日仓**必须**平今，用 Close 会被拒
//	③ ⚠️ 平仓失败时**大声报出来并给出手工处理指令**，绝不静默返回
//
// ⚠️ 为什么用涨跌停价而不是市价：市价单各交易所支持程度不一、成交价不可控，
// 而这一轮要量的正是**成交价**与它推出来的保证金基准 —— 输入必须可复现。
func runCTPRoundTrip(args []string) error {
	fs := flag.NewFlagSet("ctp-roundtrip", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 SHFE.rb2701（⚠️ 无默认值）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	dump := fs.String("dump", "", "落盘目录（⚠️ 无默认值；CTP 夹具要落 testdata/ctp）")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：一笔会**真的成交**的委托，合约必须显式指定")
	}
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
	c := ctp.New(ctp.Credentials{
		Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID,
		Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode,
	}, logf)
	c.Valve = ctpValve(env, nil)
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}

	before, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	logf("[RT] 起点  balance=%.4f 手续费累计=%.4f 占用保证金=%.4f",
		float64(before.Balance), float64(before.Commission), float64(before.CurrMargin))

	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	ex, inst := ctp.SplitSymbol(*symbol)

	// —— 买开：挂涨停，保证成交 ——
	open := ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: float64(md.UpperLimitPrice)}
	logf("[RT] 买开 1 手 @%.2f（涨停，保证成交；⚠️ 实际成交价由对手价决定）", open.LimitPrice)
	st, err := c.Insert(open, *timeout)
	if err != nil {
		return fmt.Errorf("买开：%w", err)
	}
	if st.VolumeTraded == 0 {
		return fmt.Errorf("⚠️ 买开没有成交（status=%q %s）—— 本轮没有建成仓，"+
			"**不要**当成失败去改判据，先查为什么没成", string(st.Status), st.StatusMsg)
	}
	logf("[RT] 已成交 %d 手  status=%q", st.VolumeTraded, string(st.Status))

	held, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	pos, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	logf("[RT] 建仓后  占用保证金=%.4f 手续费累计=%.4f 持仓盈亏=%.4f",
		float64(held.CurrMargin), float64(held.Commission), float64(held.PositionProfit))
	for k, p := range pos {
		logf("       %s  今仓=%d 昨仓=%d 开仓成本=%.2f 持仓成本=%.2f 占用=%.2f 昨结=%.2f",
			k, int(p.TodayPosition), int(p.YdPosition), float64(p.OpenCost),
			float64(p.PositionCost), float64(p.UseMargin), float64(p.PreSettlementPrice))
	}
	if *dump != "" {
		if fx, err := c.Capture(*timeout, "P4 第一步：建仓后的截面"); err == nil {
			secrets := map[string]string{"CTP_USER_ID": env.CTPUserID,
				"CTP_PASSWORD": env.CTPPassword, "CTP_APP_ID": env.CTPAppID,
				"CTP_AUTH_CODE": env.CTPAuthCode}
			if _, err := fx.Write(*dump, "ctp-held", secrets, logf); err != nil {
				logf("⚠️ 建仓截面落盘失败：%v", err)
			}
		}
	}

	// —— 卖平今：挂跌停，保证成交 ——
	//
	// ⚠️ SHFE 当日仓必须 CloseToday。用 Close 会被拒（「平昨手数超过昨仓持仓量」），
	// 而被拒之后仓还在 —— 那正是下面这段大声报错要处理的情形。
	close := ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: float64(md.LowerLimitPrice)}
	logf("[RT] 卖平今 1 手 @%.2f（跌停，保证成交）", close.LimitPrice)
	cs, err := c.Insert(close, *timeout)
	if err != nil || cs.VolumeTraded == 0 {
		// ⚠️ 到这里说明**账上还留着一手多仓**。不静默返回。
		return fmt.Errorf("⚠️⚠️ **平仓没有成交，账上还留着 1 手 %s 多头今仓** —— "+
			"status=%q %s err=%v。"+
			"请手工平掉，或重跑本命令的平仓段。**不要就这么走开**", *symbol,
			string(cs.Status), cs.StatusMsg, err)
	}
	logf("[RT] 平仓已成交 %d 手", cs.VolumeTraded)

	after, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	dBal := float64(after.Balance) - float64(before.Balance)
	dFee := float64(after.Commission) - float64(before.Commission)
	logf("")
	logf("[RT] 终点  balance=%.4f（差 %+.4f）  手续费累计=%.4f（差 %+.4f）占用保证金=%.4f",
		float64(after.Balance), dBal, float64(after.Commission), dFee, float64(after.CurrMargin))
	logf("")
	logf("⇒ ⚠️ 验收：差额应当**只是手续费与平仓盈亏**。")
	logf("   结存差 %+.4f，其中手续费 %+.4f ⇒ 平仓盈亏 %+.4f", dBal, -dFee, dBal+dFee)
	if float64(after.CurrMargin) != 0 {
		logf("⚠️⚠️ **占用保证金不为零（%.4f）—— 账上可能还有仓，去查**", float64(after.CurrMargin))
	}
	return nil
}

// runCTPFlatten 只做一件事：**把账上的今仓平掉**。
//
// ⚠️ 它单独成一个命令，而不是别的流程里的一段 —— 理由是 20260910 夜盘那次事故：
// 成交往返的平仓段被拒（OrderRef 不递增），**仓留在了账上**，
// 而当时能用来收拾的只有「重跑整个往返」，那会再建一手。
//
// **一个只会缩小敞口的动作，应当随时能单独执行。**
func runCTPFlatten(args []string) error {
	fs := flag.NewFlagSet("ctp-flatten", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
	c := ctp.New(ctp.Credentials{
		Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID,
		Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode,
	}, logf)
	c.Valve = ctpValve(env, nil)
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}
	pos, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	n := 0
	for key, p := range pos {
		today := int(p.TodayPosition)
		if today == 0 {
			continue
		}
		n++
		symbol := ctp.Text(p.ExchangeID[:]) + "." + ctp.Text(p.InstrumentID[:])
		// 平多发卖、平空发买。⚠️ 方向取反在这里做一次，不散在调用处。
		var dir def.TThostFtdcDirectionType = def.THOST_FTDC_D_Sell
		if p.PosiDirection == def.THOST_FTDC_PD_Short {
			dir = def.THOST_FTDC_D_Buy
		}
		md, err := c.MarketData(symbol, *timeout)
		if err != nil {
			return fmt.Errorf("⚠️ 拿不到 %s 的行情，**仓还在**：%w", symbol, err)
		}
		// 平仓挂对自己不利的那一端（涨跌停价），保证成交。
		//
		// ⚠️ 中间短暂改成过「最新价 ± 20」，那是在 `ErrorID=22` 的真因没查清时
		// 用来绕开它的 —— 而绕开一个没查清的坑，不等于查清了它。
		// 真因是报单引用的格式（probes.md §6.8），与挂价无关，所以退回涨跌停价：
		// **它保证落在合法区间内**，而「最新价 ± 20」在行情急动时会冲出涨跌停，
		// 于是平仓被拒 —— 而平仓被拒的后果是**敞口留在账上**。
		px := float64(md.LowerLimitPrice)
		if dir == def.THOST_FTDC_D_Buy {
			px = float64(md.UpperLimitPrice)
		}
		ex, inst := ctp.SplitSymbol(symbol)
		logf("[flat] %s 今仓 %d 手（%s）→ 平今 @%.2f", key, today, string(p.PosiDirection), px)
		st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: dir, Offset: def.THOST_FTDC_OF_CloseToday,
			Volume: today, LimitPrice: px}, *timeout)
		if err != nil || st.VolumeTraded == 0 {
			return fmt.Errorf("⚠️⚠️ **%s 没平掉，仓还在** —— status=%q %s err=%v",
				symbol, string(st.Status), st.StatusMsg, err)
		}
		logf("[flat] 已平 %d 手", st.VolumeTraded)
	}
	if n == 0 {
		logf("[flat] 没有今仓可平")
	}
	after, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	logf("[flat] 之后  balance=%.4f 占用保证金=%.4f 手续费累计=%.4f",
		float64(after.Balance), float64(after.CurrMargin), float64(after.Commission))
	if float64(after.CurrMargin) != 0 {
		return fmt.Errorf("⚠️⚠️ 占用保证金仍为 %.4f —— **账上还有仓**", float64(after.CurrMargin))
	}
	return nil
}

// runCTPHold 建一手仓，**盯着保证金看它随什么动**，然后平掉。
//
// # ⚠️ 它要分开的那两个候选
//
// 20260910 第一次成交后算出：占用保证金 ÷ (成交价 × 乘数) = **16.0000%**，
// 而昨结算价给出 15.9646%（非整数）⇒ 昨结算价被否。
//
// ⚠️ **但那份样本分不开「开仓价」与「今结算价」** —— 当时两者恰好都是 3157。
//
//	开仓价    建仓那一刻定死，**之后不动**
//	今结算价  盘中随行情走
//
// ⇒ 判别方法：**持仓不动，看行情走**。若占用保证金一直不变 ⇒ 基准是开仓价；
// 若它跟着行情变 ⇒ 基准是某个动态价。
//
// ⚠️ 这一条不需要新的建模，只需要**时间**：所以它做成「建仓 → 轮询 → 平仓」。
func runCTPHold(args []string) error {
	fs := flag.NewFlagSet("ctp-hold", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约（⚠️ 无默认值）")
	rounds := fs.Int("rounds", 6, "轮询次数")
	every := fs.Duration("every", 20*time.Second, "轮询间隔")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：会真的建仓")
	}
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
	c := ctp.New(ctp.Credentials{
		Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID,
		Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode,
	}, logf)
	c.Valve = ctpValve(env, nil)
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}
	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	ex, inst := ctp.SplitSymbol(*symbol)
	st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: float64(md.UpperLimitPrice)}, *timeout)
	if err != nil || st.VolumeTraded == 0 {
		return fmt.Errorf("建仓没成交：status=%q %s err=%v", string(st.Status), st.StatusMsg, err)
	}
	logf("[hold] 建仓成交 %d 手", st.VolumeTraded)

	logf("")
	logf("%-8s %10s %10s %10s %12s %12s", "时刻", "最新价", "今结算", "昨结", "占用保证金", "持仓盈亏")
	var first, last float64
	for i := 0; i < *rounds; i++ {
		if i > 0 {
			time.Sleep(*every)
		}
		m, err := c.MarketData(*symbol, *timeout)
		if err != nil {
			logf("  行情读不到：%v", err)
			continue
		}
		pos, err := c.Positions(*timeout)
		if err != nil {
			logf("  持仓读不到：%v", err)
			continue
		}
		for _, p := range pos {
			if int(p.TodayPosition) == 0 {
				continue
			}
			um := float64(p.UseMargin)
			if first == 0 {
				first = um
			}
			last = um
			logf("%-8s %10.2f %10.2f %10.2f %12.2f %12.2f",
				time.Now().Format("15:04:05"), float64(m.LastPrice),
				float64(p.SettlementPrice), float64(p.PreSettlementPrice),
				um, float64(p.PositionProfit))
		}
	}
	logf("")
	switch {
	case first != 0 && first == last:
		logf("⇒ ⚠️ 占用保证金**全程不变**（%.2f）", first)
		logf("   若期间行情有过变动 ⇒ 基准是**建仓时定死的价**（开仓价），今结算价被否")
		logf("   ⚠️ 若行情也没动 ⇒ **这一轮什么都没分开**，别当结论")
	default:
		logf("⇒ ⚠️ 占用保证金**变了**：%.2f → %.2f ⇒ 基准是某个**动态价**，开仓价被否", first, last)
	}
	logf("")
	logf("[hold] 平仓 ——")
	cs, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: float64(md.LowerLimitPrice)}, *timeout)
	if err != nil || cs.VolumeTraded == 0 {
		return fmt.Errorf("⚠️⚠️ **平仓没成交，账上还留着 1 手 %s 多头今仓** —— "+
			"status=%q %s err=%v。跑 `ctp-flatten` 收拾", *symbol, string(cs.Status), cs.StatusMsg, err)
	}
	logf("[hold] 已平 %d 手", cs.VolumeTraded)
	return nil
}
