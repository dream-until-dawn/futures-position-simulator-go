package main

import (
	"flag"
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

// longSides 是某合约多头在一个时刻的**今/昨**手数。
//
// # ⚠️ 为什么要自己算，而不是直接读 `YdPosition`
//
// CTP 的持仓记录对「今/昨」有**两种表示**，而哪一种取决于交易所：
//
//	上期所 / 能源中心（UseHistory）   今仓一条 `PositionDate=1`、昨仓一条 `PositionDate=2`
//	大商所 / 郑商所（NoUseHistory）   **可能合成一条** `PositionDate=1`，
//	                                  里面的 `TodayPosition` 是今、`Position − TodayPosition` 是昨
//
// ⚠️ 而 `YdPosition` 是**日初**的昨仓数，在一些实现里**平掉昨仓之后它不减**。
// 拿它当「此刻还剩几手昨仓」，平昨之前与之后会读出同一个数 ——
// **#4 要分的正是这件事**，于是这个字段恰好是最不能信的那一个。
//
// ⇒ 一律按 `今 = Σ TodayPosition`（仅 `PositionDate=1` 的记录）、
// `昨 = Σ Position − 今` 算；`YdPosition` 另记下来，**它动没动本身是一条观测**。
type longSides struct {
	Today, Yd int
	// YdField 是柜台 `YdPosition` 字段之和，**只记不信**。
	YdField int
	// Records 是命中的持仓记录条数：1 = 合成一条，2 = 今昨分开。
	// ⚠️ 它是 #4 的**第一个答案**：若大商所在 CTP 上也从不出现昨仓，
	// 那 #4 在第二个口子上也结构性地测不出来。
	Records int
	// OpenedToday 是柜台 `OpenVolume` 之和：**本交易日**开过几手（当日累计，平掉了也算）。
	//
	// ⚠️ 20260913 评审第二节回复时加的，它分开的是「昨 0」的三种读法：
	//
	//	今 0 昨 0                     种子**不在**（例如被平掉了）       ⇒ 前提，不是结论
	//	今 N 昨 0 且 N ≤ 今天开过的    账上的今仓**都是今天开的**         ⇒ 种子还没跨过结算，前提
	//	今 N 昨 0 且 N > 今天开过的    有一手**不是今天开的**却记作今仓   ⇒ 这才是「柜台不显示昨仓」
	//
	// 此前三种一律报「没有昨仓 ⇒ 结论」，而操作单写着「那就是 #4 的结论，别当失败重跑」——
	// **种子周六若真被平掉了，周一会记下一条假结论。**
	OpenedToday int
}

// longSidesOf 从一次持仓查询里取某合约多头的今/昨。
func longSidesOf(pos map[string]*def.CThostFtdcInvestorPositionField, inst string) longSides {
	var s longSides
	total := 0
	for _, p := range pos {
		if ctp.Text(p.InstrumentID[:]) != inst || p.PosiDirection != def.THOST_FTDC_PD_Long {
			continue
		}
		if int(p.Position) == 0 && int(p.YdPosition) == 0 {
			continue
		}
		s.Records++
		total += int(p.Position)
		s.OpenedToday += int(p.OpenVolume)
		s.YdField += int(p.YdPosition)
		if p.PositionDate == def.THOST_FTDC_PSD_Today {
			s.Today += int(p.TodayPosition)
		}
	}
	s.Yd = total - s.Today
	return s
}

// closeOrderKind 是 #4 这一轮能给出的**全部**结论形状。
type closeOrderKind int

const (
	// coNoPremise 平仓之前不是「今 1、昨 ≥1」⇒ 不判。
	coNoPremise closeOrderKind = iota
	// coNoYesterday 昨仓为 0，而账上有**不是今天开的**多头被记作今仓 ⇒ ⚠️ 这是一条**结论**：
	// 大商所在 CTP 上同样不出现昨仓 ⇒ #4 在第二个口子上也结构性地测不出来。
	coNoYesterday
	// coNotOneLot 平完之后总手数没有恰好少 1 ⇒ 平仓没成、或成了不止一手，不判。
	coNotOneLot
	// coConsumedToday 通用平仓消耗了**今**仓。
	coConsumedToday
	// coConsumedYesterday 通用平仓消耗了**昨**仓。
	coConsumedYesterday
	// coNoSeed 昨仓为 0，而账上的多头**全是今天开的**（或根本没有）⇒ 种子不在或还没跨过结算，
	// **不是结论**。⚠️ 排在 iota 末尾是为了不改前五个取值（破坏清单与测试按取值读）。
	coNoSeed
)

// closeOrderVerdict 按**优先级**判一轮 #4。顺序与 holdVerdict 同一条纪律：
//
//	昨 0 且种子不在/没跨结算（前提）> 昨 0 而有非今天开的今仓（结论）> 前提不成立 > 没平成一手 > 真结论
//
// ⚠️ 它被抽成纯函数是方法论 93 那条的直接应用：
// 20260912 我为了验证一道「防误平」的门，真的跑了一次会误平的命令 ——
// 根因是判据长在要下真单的函数里。**这一轮的判据一开始就不许那样长。**
func closeOrderVerdict(before, after longSides) closeOrderKind {
	if before.Yd == 0 {
		// ⚠️ 「没有昨仓」只有在账上确有一手**不是今天开的**多头时才是结论。
		if before.Today <= before.OpenedToday {
			return coNoSeed
		}
		return coNoYesterday
	}
	if before.Today != 1 {
		return coNoPremise
	}
	if (before.Today+before.Yd)-(after.Today+after.Yd) != 1 {
		return coNotOneLot
	}
	switch {
	case after.Today == before.Today-1 && after.Yd == before.Yd:
		return coConsumedToday
	case after.Yd == before.Yd-1 && after.Today == before.Today:
		return coConsumedYesterday
	}
	// 总数少了 1 而今昨各自的变化对不上任何一种 —— 那不是结论，是读数有问题。
	return coNotOneLot
}

// closeOrderDescribe 把判定翻成人话。⚠️ 与判定分开写，理由同 holdVerdict：
// 判定要能离线测，而人话会随措辞改。
func closeOrderDescribe(k closeOrderKind, before, after longSides) string {
	switch k {
	case coNoYesterday:
		return fmt.Sprintf("⚠️⚠️ **账上没有昨仓**（今 %d、昨 0、本交易日开过 %d、记录 %d 条）—— "+
			"有 %d 手**不是今天开的**多头被记作今仓 ⇒ 大商所在 CTP 上**同样不显示昨仓** ⇒ "+
			"#4 在第二个口子上也结构性地测不出来。⚠️ 这是一条结论，不是失败",
			before.Today, before.OpenedToday, before.Records, before.Today-before.OpenedToday)
	case coNoSeed:
		return fmt.Sprintf("⚠️⚠️ **种子不在，不判**：今 %d、昨 0、本交易日开过 %d —— "+
			"账上的多头**全是今天开的**（或根本没有）。要么种子已经没了，要么它还没跨过结算。"+
			"⚠️ **这不是 #4 的结论**，别记成「柜台不显示昨仓」",
			before.Today, before.OpenedToday)
	case coNoPremise:
		return fmt.Sprintf("⚠️ **前提不成立**：平仓之前是 今 %d / 昨 %d，要 今 1 / 昨 ≥1 —— 不判",
			before.Today, before.Yd)
	case coNotOneLot:
		return fmt.Sprintf("⚠️ **没有恰好平掉一手**：之前 今 %d / 昨 %d，之后 今 %d / 昨 %d —— 不判",
			before.Today, before.Yd, after.Today, after.Yd)
	case coConsumedToday:
		return fmt.Sprintf("✅ 通用平仓消耗的是**今仓**：今 %d→%d、昨 %d→%d",
			before.Today, after.Today, before.Yd, after.Yd)
	case coConsumedYesterday:
		return fmt.Sprintf("✅ 通用平仓消耗的是**昨仓**：今 %d→%d、昨 %d→%d",
			before.Today, after.Today, before.Yd, after.Yd)
	}
	return fmt.Sprintf("⚠️ 认不得的判定 %d", int(k))
}

// genericCloseReq 是 #4 那一笔**判别性**委托。
//
// ⚠️⚠️ **开平标志必须是通用的 `OF_Close`。**
// 用 `CloseToday` 或 `CloseYesterday` 就是**替柜台指定了答案** ——
// 那样跑出来的「消耗了今仓」只是复述了自己发的标志，整轮实验无效，
// **而输出看起来完全正常**。守卫 `TestGenericCloseUsesTheGenericFlag`。
func genericCloseReq(ex, inst string, lowerLimit float64) ctp.OrderReq {
	return ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_Close,
		Volume: 1, LimitPrice: lowerLimit}
}

// closeOrderStage 是 #4 三份落盘的阶段。与 sliceStage 同一条理由：注记由阶段派生，
// 调用点不写中文 ⇒「注记对调」在语法上说不出口。
type closeOrderStage int

const (
	coStageSeedOnly   closeOrderStage = iota + 1 // ① 尚未开今仓：种子所在的那一刻
	coStageBothSides                             // ② 今 1 + 昨 ≥1，通用平仓之前
	coStageAfterClose                            // ③ 通用平仓之后
)

// tradesExpected：① 落在开今仓之前，当日本合约还没有成交；②③ 都在开仓之后。
func (s closeOrderStage) tradesExpected() bool { return s != coStageSeedOnly }

func (s closeOrderStage) note() string {
	switch s {
	case coStageSeedOnly:
		return "ctp-closeorder ①：尚未开今仓，只有跨过结算的种子（柜台可能记作昨仓，也可能记作今仓）"
	case coStageBothSides:
		return "ctp-closeorder ②：今 1 + 昨 ≥1，**通用平仓之前**"
	case coStageAfterClose:
		return "ctp-closeorder ③：通用平仓（OF_Close）一手**之后**"
	}
	return fmt.Sprintf("⚠️ ctp-closeorder：认不得的阶段 %d", int(s))
}

// runCTPCloseOrder 答 `rules_pending` #4：**NoUseHistory 交易所上，今昨都在时，
// 通用平仓标志消耗哪一边**。
//
// # ⚠️ 它是写周一操作单时才发现缺的
//
// #4 要一笔**通用** `OF_Close`，而此前没有任何 CTP 命令发得出它 ——
// `ctp-order` 只有显式的 `-close`（平今）与 `-closeyd`（平昨），
// 那等于**替柜台指定了答案**。若不写操作单，这件事要到周一 21:00 才发现。
//
// # 流程
//
//	0 前提：该合约多头**今 0、昨 ≥1**（跨过结算的种子）
//	         ⚠️ 若昨仓为 0 —— 有非今天开的今仓 ⇒ 结论（柜台不显示昨仓），落一份就停；
//	         账上多头全是今天开的（或没有）⇒ 种子不在，**报错不判**
//	1 落 ①   2 买开一手 ⇒ 今 1 + 昨 ≥1   3 落 ②
//	4 **通用平仓**一手（`genericCloseReq`）   5 落 ③   6 判定
//	收尾：只用 `CloseToday` 平掉**今**仓，昨仓（种子的剩余）不碰
func runCTPCloseOrder(args []string) error {
	fs := flag.NewFlagSet("ctp-closeorder", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 DCE.m2701（⚠️ 无默认值：会真的开一手、平一手）")
	dump := fs.String("dump", "", "三份截面落盘目录（⚠️ 只能是仓库根下的 testdata/ctp —— 落别处会被拒，不是只写在这句里）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	cleanup := fs.Bool("cleanup", false, "**只收尾**：只用平今单平掉该合约多头的今仓，不开仓、不做实验。"+
		"⚠️ 20260913 评审第二轮补的：保护期内（20260914/15）第 1 步的收尾若失败，遗留的今仓"+
		"`ctp-flatten -symbol <合约>` 平不掉（按保护跳过），而本命令是唯一豁免保护的 —— 没有这个模式就只能把整个实验重跑一遍。"+
		"⚠️ 分不清今仓里有没有种子时**拒绝动手**（见 cleanupVerdict）")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	// ⚠️ CTP 夹具只有一个家（仓库根下的 testdata/ctp）；落错在此之前不报错，理由见 dumpdir.go
	if *dump != "" {
		abs, err := ctpDumpDir(*dump)
		if err != nil {
			return err
		}
		*dump = abs
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：本命令会真的开一手、平一手")
	}
	if *dump == "" && !*cleanup {
		// ⚠️ 不落盘就不跑：#13 那次的教训是「判别力躺在一个没拍下来的瞬间里」。
		return fmt.Errorf("⚠️ -dump 没有给 —— 本命令**不许只打 console**：" +
			"#13 被整条打回，正是因为判别性的读数只进了日志、没进夹具")
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
	// ⚠️ 唯一的豁免：理由见 ctpValveExempt。
	c.Valve = ctpValveExempt(env, "#4 要在种子所在的合约上开一手、通用平一手，而受保护腿不分今昨", logf)
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}
	ex, inst := ctp.SplitSymbol(*symbol)
	if *cleanup {
		return closeOrderCleanup(c, ex, inst, *timeout, logf)
	}

	// ——— 0 前提（排在任何委托之前）———
	pos0, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	s0 := longSidesOf(pos0, inst)
	logf("[co] 开始：今 %d / 昨 %d / 本交易日开过 %d（YdPosition 字段 %d，记录 %d 条）",
		s0.Today, s0.Yd, s0.OpenedToday, s0.YdField, s0.Records)
	if s0.Yd == 0 {
		// ⚠️ 判定**必须经 closeOrderVerdict**，不在这里自己下结论 ——
		// 上一版在这里直接报 coNoYesterday，种子不在时也照报（见 longSides.OpenedToday）。
		k := closeOrderVerdict(s0, s0)
		if k != coNoYesterday {
			return fmt.Errorf("%s", closeOrderDescribe(k, s0, s0))
		}
		// ⚠️ 这一支**先落盘再返回**：「柜台不显示昨仓」是 #4 的一条结论，要有夹具。
		if err := dumpSlices(c, env, *dump, *timeout, *symbol, coStageSeedOnly, logf); err != nil {
			return err
		}
		logf("%s", closeOrderDescribe(k, s0, s0))
		return nil
	}
	if s0.Today != 0 {
		return fmt.Errorf("⚠️ **前提不成立，不跑**：%s 上已有 %d 手多头今仓 —— "+
			"本命令要自己开那一手今仓，账上已有的会让「消耗了哪一边」读不清。"+
			"⚠️ **别用 `ctp-flatten -symbol %s`** —— 它会去平昨仓（种子）：受保护的交易日里安全阀会拦下，"+
			"过了那几天就真的平掉。"+
			"要么用平今单只平今仓，要么等明天", *symbol, s0.Today, *symbol)
	}

	// ——— 收尾：注册在第一笔委托之前（R6 那条留仓路径的教训）———
	defer func() {
		if err := closeTodayOnly(c, ex, inst, *timeout, logf); err != nil {
			logf("[co] ⚠️⚠️ **今仓没平干净，仓留在账上了**：%v", err)
		}
	}()

	if err := dumpSlices(c, env, *dump, *timeout, *symbol, coStageSeedOnly, logf); err != nil {
		return err
	}

	// ——— 2 买开一手 ———
	if _, err := openOneLot(c, ex, inst, 1, *timeout, logf); err != nil {
		return err
	}
	pos1, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	s1 := longSidesOf(pos1, inst)
	logf("[co] 开今一手之后：今 %d / 昨 %d（YdPosition 字段 %d，记录 %d 条）", s1.Today, s1.Yd, s1.YdField, s1.Records)
	if err := dumpSlices(c, env, *dump, *timeout, *symbol, coStageBothSides, logf); err != nil {
		return err
	}

	// ——— 4 通用平仓一手 ———
	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	st, err := c.Insert(genericCloseReq(ex, inst, float64(md.LowerLimitPrice)), *timeout)
	if err != nil || st.VolumeTraded == 0 {
		return fmt.Errorf("通用平仓没成交：status=%q %s err=%v", string(st.Status), st.StatusMsg, err)
	}
	pos2, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	s2 := longSidesOf(pos2, inst)
	logf("[co] 通用平仓之后：今 %d / 昨 %d（YdPosition 字段 %d，记录 %d 条）", s2.Today, s2.Yd, s2.YdField, s2.Records)
	if err := dumpSlices(c, env, *dump, *timeout, *symbol, coStageAfterClose, logf); err != nil {
		return err
	}

	k := closeOrderVerdict(s1, s2)
	logf("")
	logf("%s", closeOrderDescribe(k, s1, s2))
	if s2.YdField != s1.YdField {
		logf("ⓘ YdPosition 字段 %d→%d **动了** —— 在这个柜台上它不是日初静态值", s1.YdField, s2.YdField)
	} else {
		logf("ⓘ YdPosition 字段始终是 %d —— ⚠️ 若消耗的是昨仓而它没动，那正是「不信这个字段」的理由", s1.YdField)
	}
	return nil
}

// closeTodayOnly 用 **CloseToday** 平掉该合约多头的**今**仓，昨仓一手不碰。
//
// ⚠️ 不复用 `flattenLongToday`：那个函数按 `PositionDate == Today` 的记录取 `Position`，
// 而大商所可能把今昨**合成一条** `PositionDate=1` 的记录 —— 它会把昨仓也算进去。
// 这里只按 `longSidesOf` 算出的**今**手数下单。
func closeTodayOnly(c *ctp.Client, ex, inst string, timeout time.Duration,
	logf func(string, ...any)) error {
	yd0 := -1
	for i := 0; i < 5; i++ {
		pos, err := c.Positions(timeout)
		if err != nil {
			return err
		}
		s := longSidesOf(pos, inst)
		if yd0 < 0 {
			yd0 = s.Yd
		}
		done, err := closeTodayStep(yd0, s)
		if err != nil {
			return err
		}
		if done {
			logf("[co] 收尾：今仓已清空（昨 %d 手保留）", s.Yd)
			return nil
		}
		md, err := c.MarketData(ex+"."+inst, timeout)
		if err != nil {
			return err
		}
		st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_CloseToday,
			Volume: 1, LimitPrice: float64(md.LowerLimitPrice)}, timeout)
		if err != nil || st.VolumeTraded == 0 {
			return fmt.Errorf("平今没成交：status=%q %s err=%v", string(st.Status), st.StatusMsg, err)
		}
	}
	return fmt.Errorf("⚠️ 平了 5 次今仓还没清空 —— 停手，去看账户")
}

// closeTodayStep 是 closeTodayOnly 每一轮发单前的判定：done = 今仓已清空；err = 必须停手。
//
// ⚠️⚠️ **昨仓一旦少了，立刻停手** —— 20260914 日盘开跑前对着真实账户形状重读收尾时补的。
//
// 收尾在「昨仓还在」时也会发平今单（例如通用平仓没成交、提前返回，defer 照跑）。
// 而**大商所上一笔平今单会不会吃到合成记录里的昨仓，没有实测过**。若它吃了：
//
//	平今一手之后   今仍是 1（新开的那手还在）、昨 1 → 0（种子没了）
//	上一版         「今 ≠ 0」⇒ 再发一笔平今 ⇒ 新开的那手也平掉 ⇒ 打印「今仓已清空」，**正常返回**
//
// ⇒ 种子没了，而输出上一个字都不提。这里在每一轮开头比一次：昨仓比进入收尾时少 ⇒ 报错停手，
// 而且这件事**本身是 #4 的一条观测**（平今标志在大商所消耗了昨仓），要记下来。
func closeTodayStep(yd0 int, s longSides) (done bool, err error) {
	if s.Yd < yd0 {
		return false, fmt.Errorf("⚠️⚠️ **收尾平今之后昨仓从 %d 变成 %d —— 平今单吃掉了昨仓（种子）**。停手，去看账户。"+
			"⚠️ 这一条本身是 #4 的观测：大商所上 CloseToday 标志消耗了昨仓，**记进 §13，别当成收尾故障重跑**", yd0, s.Yd)
	}
	return s.Today == 0, nil
}

// cleanupVerdict 判 `ctp-closeorder -cleanup` 能不能动手；nil = 可以。
//
// ⚠️ 收尾用的是平今单（closeTodayOnly 按「今」手数发 CloseToday）。它只在**今仓里确定没有种子**时才安全：
//
//	今 0                              没有要收的，不动手（返回 errNothingToClean）
//	昨 ≥1                             种子作为昨仓单独可见 ⇒ 今仓全是新开的，可以
//	昨 0 且 今 ≤ 本交易日开过的         今仓都是今天开的（#4 消耗了昨仓之后就是这样）⇒ 可以
//	昨 0 且 今 > 本交易日开过的         有一手**不是今天开的**却记作今仓 —— 种子可能就在里面 ⇒ **拒绝**
//
// ⚠️ 与第 1 步收尾同一个前提：第 1 步只在「昨 ≥1」时才开仓，所以它的 defer 本来就只在第二行的情形下跑。
// ⚠️ 仍有一格分不开（登记，不假装守住）：种子被记作今仓 **且** 今天另开过又平掉过 ——
// `OpenVolume` 是当日累计，会把「开过又平掉的」也算进去，于是第三行误判为可以。
func cleanupVerdict(s longSides) error {
	switch {
	case s.Today == 0:
		return errNothingToClean
	case s.Yd >= 1:
		return nil
	case s.Today <= s.OpenedToday:
		return nil
	}
	return fmt.Errorf("⚠️⚠️ **拒绝收尾**：今 %d、昨 0、本交易日开过 %d —— 有 %d 手**不是今天开的**却记作今仓，"+
		"种子可能就在里面，而平今单会不会吃到它正是 #4 要答的问题。去看账户，手工决定",
		s.Today, s.OpenedToday, s.Today-s.OpenedToday)
}

// errNothingToClean 是「今仓为 0，没有要收的」。它不是事故，调用方据此正常退出。
var errNothingToClean = fmt.Errorf("今仓为 0，没有要收的")

// closeOrderCleanup 是 `ctp-closeorder -cleanup` 的全部动作：查持仓 → cleanupVerdict → closeTodayOnly。
func closeOrderCleanup(c *ctp.Client, ex, inst string, timeout time.Duration, logf func(string, ...any)) error {
	pos, err := c.Positions(timeout)
	if err != nil {
		return err
	}
	s := longSidesOf(pos, inst)
	logf("[co] -cleanup：今 %d / 昨 %d / 本交易日开过 %d（记录 %d 条）", s.Today, s.Yd, s.OpenedToday, s.Records)
	if err := cleanupVerdict(s); err != nil {
		if err == errNothingToClean {
			logf("[co] -cleanup：%v", err)
			return nil
		}
		return err
	}
	return closeTodayOnly(c, ex, inst, timeout, logf)
}
