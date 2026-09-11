package main

import (
	"flag"
	"fmt"
	"math"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

// runCTPSlices 造一个**两片、片价不同**的今仓，只平掉其中一手，
// 看柜台把哪一片的成本拿去算平仓盈亏。它答的是 `rules_pending` #13。
//
// ⚠️ 这一条此前是**零观测**，而它零观测的理由是个安全阀：
// `PROBE_MAX_VOLUME=1` 让每笔成交都是 1 手，于是每次平仓恰好只消耗一片，
// **逐片与按均价必然同值** —— 破坏验证把「只取第一片」放进生产路径，
// 全部夹具对拍照样绿。
//
//	⚠️ 而这不需要放开安全阀：**一手一笔、开两笔**，一样是两片。
//	挡住这条实验的从来不是 1 手上限，是我没想到分两笔开。
//
// 三个候选（mult 是合约乘数，q 是平仓成交价）：
//
//	逐片 FIFO  (q − p1) × mult      先开的先平
//	逐片 LIFO  (q − p2) × mult      后开的先平
//	按均价     (q − (p1+p2)/2) × mult
//
// ⚠️ **判别力的前提是 p1 ≠ p2**，否则三个候选给出同一个数。
// 本命令为此等行情走开才开第二腿，并在开完之后**以成交价再核一次** ——
// 等到了不等于成交价就不同，盘口会弹回去。不合格的腿 2 平掉重来。
//
// ⚠️ 而 `-second higher` 那个方向（拆「先开的」与「价高的」这个混淆）
// **不该靠等行情往上走**：20260911 夜盘为此空等了两个窗口共 32 分钟。
// ⇒ `-restfirst` 把腿 1 挂在最新价下方等成交，让价差保证 p2 > p1。
//
// ⚠️ 而它有**两个互相独立的读数**，这是刻意的：
//
//	持仓的 CloseProfit 增量   柜台算出来的盈亏
//	持仓的 OpenCost 减量      柜台冲减掉的那一片的成本
//
// 两个字段答同一个问题。**它们不一致本身就是结论** ——
// 那说明「盈亏按哪一片算」与「成本按哪一片冲」在这个柜台上不是同一件事。
func runCTPSlices(args []string) error {
	fs := flag.NewFlagSet("ctp-slices", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 SHFE.rb2701（⚠️ 无默认值：会真的建两手仓）")
	mult := fs.Float64("multiplier", 0, "合约乘数（⚠️ **无默认值**）。"+
		"⚠️ 猜错的表现是三个候选**整体差一个倍数**，而它们之间的大小关系不变 —— "+
		"于是「命中哪一个」照样打印得出来，且照样是错的")
	tick := fs.Float64("tick", 0, "最小变动价位（⚠️ **无默认值**）：用来判「行情走开了没有」")
	second := fs.String("second", "any", "第二腿必须成交在第一腿的哪一侧：any / higher / lower。"+
		"⚠️ **这个开关是用来拆混淆的**：20260911 夜盘第一个样本是 p1=3104 > p2=3103，"+
		"于是「先开的那片」与「价高的那片」**是同一片** —— 命中 FIFO 的那个数，"+
		"同样能被「柜台先平价高的」解释。要拆开它，得再拍一个 p2 > p1 的样本")
	wait := fs.Duration("wait", 8*time.Minute, "等行情走开的上限。等不到就**不开第二腿**")
	every := fs.Duration("every", 3*time.Second, "等行情时的轮询间隔")
	firstRest := fs.Bool("restfirst", false, "腿1 **挂在低一个价位上等成交**，而不是打卖一。"+
		"⚠️ 它把「p2 > p1」从**赌行情方向**换成**靠买卖价差** ——卖一总在最新价之上或与之齐平，"+
		"于是 p2 必定高出至少一个价位。配 -second higher 用")
	fillWait := fs.Duration("fillwait", 6*time.Minute, "-restfirst 时等腿1 成交的上限。等不到就**撤掉并报错**，不留单")
	dump := fs.String("dump", "", "把**两片俱在、尚未平仓**那一刻的截面落盘到该目录"+
		"（⚠️ CTP 夹具只能落 testdata/ctp/）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：本命令会真的建两手仓")
	}
	if *mult <= 0 {
		return fmt.Errorf("⚠️ -multiplier 没有默认值：rb 是 10、m 是 10、i 是 100、ag 是 15")
	}
	if *tick <= 0 {
		return fmt.Errorf("⚠️ -tick 没有默认值：它决定「走开了没有」的判据")
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
	logf("[sl] 安全阀：AllowOrder=%v MaxVolume=%d", env.AllowOrder, env.MaxVolume)
	ex, inst := ctp.SplitSymbol(*symbol)

	// ⚠️ **前提检查放在最前面，而且不成立就退出。**
	//
	// 20260911 夜盘 ctp-hold 给过一个自信的假结论：它看见占用保证金变了，
	// 就打印「基准是某个动态价，开仓价被否」—— 而变化来自账上多了一条反向腿。
	// **它的判据没写错，错在前提被违反**，而它不会说「我的前提不成立了」，
	// 它只会照常打印。
	//
	// 本命令的前提是：**这个合约这个方向此刻没有今仓**。
	// 否则 OpenCost 的增量里混着别人开的片，p1/p2 全错而数值完全正常。
	pos0, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	if p := longToday(pos0, inst); p != nil && int(p.Position) != 0 {
		return fmt.Errorf("⚠️ **前提不成立，不跑**：%s 上已有 %d 手多头今仓。"+
			"本命令靠 OpenCost 的增量反解片价，账上已有的片会混进来 —— "+
			"先跑 ctp-flatten", *symbol, int(p.Position))
	}

	// ——— 腿 1 ———
	var p1 float64
	if *firstRest {
		p1, err = openOneLotResting(c, ex, inst, *mult, *tick, *fillWait, *timeout, logf)
	} else {
		p1, err = openOneLot(c, ex, inst, *mult, *timeout, logf)
	}
	if err != nil {
		return err
	}
	logf("[sl] 腿1 成交价 p1 = %.4f（由 OpenCost 增量 ÷ 乘数 反解）", p1)

	// ⚠️ 从这里起账上有仓，**任何一条返回路径都要先平干净**。
	defer func() {
		if err := flattenLongToday(c, ex, inst, *timeout, logf); err != nil {
			logf("[sl] ⚠️⚠️ **平不干净，仓留在账上了**：%v", err)
			logf("      去跑 oracle ctp-flatten。⚠️ 留仓与「实验就是要留仓」" +
				"在账户上长得一模一样，差别只在有没有人打算这么做")
		}
	}()

	// ——— 等第二腿能成交在别的价上 ———
	//
	// ⚠️ **等的判据是卖一价，不是最新价。**第一版等的是
	// 「最新价离开 p1 至少两个最小变动价位」—— 而那个条件既**过强**又**跑偏**：
	//
	//	跑偏：腿 2 挂涨停，成交在**卖一**上。最新价走没走，与卖一在哪，是两回事。
	//	过强：卖一只要不等于 p1，两片就已经不同价 —— 一个跳都不用等满。
	//
	// 20260911 夜盘第一次跑就撞上了：rb 在 3103/3104/3105 之间来回，
	// 最新价始终没离开 p1=3104 两个价位，**六分钟等了个空** ——
	// 而同一段时间里卖一在 3104 与 3105 之间换了几十次，每一次都够用。
	want, err := sideWanted(*second)
	if err != nil {
		return err
	}
	logf("[sl] 等行情走到 %.4f 的 %q 一侧（一个最小变动价位就够），上限 %s ——",
		p1, *second, *wait)
	deadline := time.Now().Add(*wait)
	var p2 float64
	tries, askSeen, askDead := 0, 0, 0
	for {
		// ——— 一、等触发 ———
		triggered := false
		for time.Now().Before(deadline) {
			m, err := c.MarketData(*symbol, *timeout)
			if err != nil {
				logf("  行情读不到：%v", err)
				time.Sleep(*every)
				continue
			}
			// ⚠️ **优先看卖一**（腿 2 挂涨停，成交在卖一上），读不到才退回最新价。
			//
			// ⚠️ 20260911 夜盘栽在这里，而栽的方式值得写全：第一版**只看卖一**，
			// 读不到就 `continue` —— 而 CTP 的**行情查询**（`ReqQryDepthMarketData`，
			// 不是订阅）应答里盘口档位并不可靠，`AskVolume1` 常是 0。
			// 于是那个 `continue` **每一轮都命中**，整整两个窗口（12 分 + 20 分）
			// 一次都没触发过。
			//
			//	⚠️ 而日志里它长得和「行情就是没往那边走」一模一样 ——
			//	我差一点把它当成「白银今晚一路下行」写进结论。
			//
			// ⇒ 两半改法：读不到就退回最新价并**说出来**；
			// 而「最新价不是成交价」这个顾虑由下面的**事后核对**兜底 ——
			// 触发只管把我们叫醒，**方向对不对以成交价为准**。
			px, src := float64(m.AskPrice1), "卖一"
			if px <= 0 || px > 1e300 || int(m.AskVolume1) <= 0 {
				px, src = float64(m.LastPrice), "最新价"
				askDead++
			} else {
				askSeen++
			}
			if math.Abs(px-p1) >= *tick && want(px, p1) {
				logf("[sl] %s 走到了 %.4f（距 p1 %.4f，在要的那一侧）", src, px, math.Abs(px-p1))
				triggered = true
				break
			}
			time.Sleep(*every)
		}
		if !triggered {
			return fmt.Errorf("⚠️ **等不到行情走到 %q 一侧，不开第二腿**（已试 %d 次，"+
				"卖一可用 %d 轮 / 读不到 %d 轮）：开了也是两片同价或同侧 ⇒ "+
				"**一个没有判别力、或者拆不开混淆的样本，而它会打印得像个结论**",
				*second, tries, askSeen, askDead)
		}

		// ——— 二、开腿 2，然后**以成交价为准**核一次 ———
		tries++
		p2, err = openOneLot(c, ex, inst, *mult, *timeout, logf)
		if err != nil {
			return err
		}
		logf("[sl] 腿2 成交价 p2 = %.4f（第 %d 次尝试）", p2, tries)
		if p2 != p1 && want(p2, p1) {
			break
		}
		// ⚠️ **这一手不能留**：它要么与腿 1 同价（没有判别力），
		// 要么落在不要的那一侧（拆不开混淆）。留着它样本就废了，
		// 而废样本会照常打印出「✅ 命中」。
		why := "与腿1同价"
		if p2 != p1 {
			why = fmt.Sprintf("落在 %q 的反面", *second)
		}
		logf("[sl] ⚠️ 腿2 %s（p1=%.4f p2=%.4f）—— **平掉它重来**", why, p1, p2)
		if err := flattenOneLongToday(c, ex, inst, *timeout); err != nil {
			return fmt.Errorf("平掉不合格的腿2 失败，账上现在有两片：%w", err)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("⚠️ **窗口用完，没拿到合格的腿2**（试了 %d 次）—— "+
				"不当结论，重跑", tries)
		}
	}

	pos2 := longToday(mustPositions(c, *timeout, logf), inst)
	if pos2 == nil || int(pos2.Position) != 2 {
		return fmt.Errorf("⚠️ 两腿开完之后今仓不是 2 手 —— 前提塌了，不判")
	}
	oc2, ca2, cp2 := float64(pos2.OpenCost), float64(pos2.CloseAmount), float64(pos2.CloseProfit)
	logf("[sl] 两片俱在：Position=%d OpenCost=%.4f PositionCost=%.4f CloseAmount=%.4f CloseProfit=%.4f",
		int(pos2.Position), oc2, float64(pos2.PositionCost), ca2, cp2)

	// ⚠️ **候选写在观测之前**，而且写成公式：q 此刻还没发生。
	// 这不是形式 —— ① 那次之所以算强档，正因为候选是开仓前 4 小时写死的。
	logf("")
	logf("[sl] ⚠️ **平仓之前先写死候选**（q = 平仓成交价，此刻尚未发生）：")
	logf("      逐片 FIFO  (q − %.4f) × %.0f", p1, *mult)
	logf("      逐片 LIFO  (q − %.4f) × %.0f", p2, *mult)
	logf("      按均价     (q − %.4f) × %.0f", (p1+p2)/2, *mult)
	logf("      ⚠️ p1 ≠ p2（差 %.4f）⇒ 三者两两不等 ⇒ **这个样本分得开**", math.Abs(p2-p1))
	logf("")

	if *dump != "" {
		// ⚠️ 走共用的 captureWithQuote：「补不上行情就一份都不落」
		// 这条不变式只能有一个实现。
		fx, err := captureWithQuote(c, *timeout, "ctp-slices：两片俱在、尚未平仓", *symbol)
		if err != nil {
			return err
		}
		secrets := map[string]string{
			"CTP_USER_ID": env.CTPUserID, "CTP_PASSWORD": env.CTPPassword,
			"CTP_BROKER_ID": env.CTPBrokerID, "CTP_APP_ID": env.CTPAppID,
			"CTP_AUTH_CODE": env.CTPAuthCode,
		}
		if _, err := fx.Write(*dump, "ctp-slices", secrets, logf); err != nil {
			return err
		}
	}

	// ——— 只平一手 ———
	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: float64(md.LowerLimitPrice)}, *timeout)
	if err != nil || st.VolumeTraded == 0 {
		return fmt.Errorf("平一手没成交：status=%q %s err=%v", string(st.Status), st.StatusMsg, err)
	}
	pos3 := longToday(mustPositions(c, *timeout, logf), inst)
	if pos3 == nil || int(pos3.Position) != 1 {
		return fmt.Errorf("⚠️ 平完之后今仓不是 1 手 —— 前提塌了，不判")
	}
	oc3, ca3, cp3 := float64(pos3.OpenCost), float64(pos3.CloseAmount), float64(pos3.CloseProfit)
	q := (ca3 - ca2) / *mult
	consumed := oc2 - oc3
	gotProfit := cp3 - cp2
	logf("[sl] 平完一手：OpenCost=%.4f CloseAmount=%.4f CloseProfit=%.4f", oc3, ca3, cp3)
	logf("[sl] ⇒ 平仓成交价 q = (%.4f − %.4f) ÷ %.0f = **%.4f**", ca3, ca2, *mult, q)

	fifo, lifo, avg := sliceProfitCandidates(q, p1, p2, *mult)
	costFirst, costSecond, costAvg := sliceCostCandidates(p1, p2, *mult)

	logf("")
	logf("=== 读数一：柜台算出来的平仓盈亏（CloseProfit 增量）= %.4f ===", gotProfit)
	logf("      逐片 FIFO  %12.4f   %s", fifo, hitMark(gotProfit, fifo))
	logf("      逐片 LIFO  %12.4f   %s", lifo, hitMark(gotProfit, lifo))
	logf("      按均价     %12.4f   %s", avg, hitMark(gotProfit, avg))
	logf("")
	logf("=== 读数二：柜台冲减掉的那一片的成本（OpenCost 减量）= %.4f ===", consumed)
	logf("      第一片 p1×乘数  %12.4f   %s", costFirst, hitMark(consumed, costFirst))
	logf("      第二片 p2×乘数  %12.4f   %s", costSecond, hitMark(consumed, costSecond))
	logf("      均价  ×乘数     %12.4f   %s", costAvg, hitMark(consumed, costAvg))
	logf("")
	logf("⚠️ 两个读数答的是同一个问题。**不一致本身就是结论** ——")
	logf("   那说明「盈亏按哪一片算」与「成本按哪一片冲」在这个柜台上不是同一件事")
	return nil
}

// sideWanted 把 -second 翻成一个「第二腿的价可不可以」的判据。
//
// ⚠️ 它对未知取值**报错而不是当成 any**：一个把 "higer" 读成「随便哪边」的开关，
// 会安安静静地拍回一个同侧样本 —— 而同侧样本与拆开了混淆的样本
// **在输出里长得一模一样**，两边都印着「✅ 命中 FIFO」。
func sideWanted(s string) (func(ask, p1 float64) bool, error) {
	switch s {
	case "any":
		return func(ask, p1 float64) bool { return true }, nil
	case "higher":
		return func(ask, p1 float64) bool { return ask > p1 }, nil
	case "lower":
		return func(ask, p1 float64) bool { return ask < p1 }, nil
	}
	return nil, fmt.Errorf("⚠️ -second 只认 any / higher / lower，收到 %q —— "+
		"不猜：猜错了会拍回一个拆不开混淆的样本，而它印出来跟好样本一模一样", s)
}

// sliceProfitCandidates 给出三个候选**平仓盈亏**。
//
// ⚠️ 它被单独拆出来，是因为这三个数**在盘中只算一次、只打印一次**，
// 而算错了的表现是「命中哪一个」变了 —— 不是报错，是换一个结论。
// 拆出来它才有一个不上盘就能跑的判别测试。
func sliceProfitCandidates(q, p1, p2, mult float64) (fifo, lifo, avg float64) {
	return (q - p1) * mult, (q - p2) * mult, (q - (p1+p2)/2) * mult
}

// sliceCostCandidates 给出三个候选**被冲减掉的那一片的成本**。
//
// ⚠️ **这里的括号纯属可读性，不是修 bug。**写这个探针时我一度断定
// `(p1+p2)/2**mult` 会被 Go 解析成 `(p1+p2) / (2 * *mult)`（乘数跑到分母上），
// 加了括号、写了注释、还配了一条测试说它来自「当场犯的一次」。
// **那个错从来没发生过**：`/` 与 `*` 同优先级左结合，两种写法是同一个表达式
// （实测 31010 == 31010）。原来的代码本来就是对的。
//
//	⚠️ 抓到它的**不是复查，是设计破坏验证的过程**：我在想「改成什么能让
//	那条测试红」，而答案是「没有改法」—— 两种写法编译成同一棵树。
//	**零层不成立在事前就露了出来**，见 silent-risks 方法论 85。
//
// 守卫 `TestSliceCostCandidateIsAveragePriceTimesMultiplier`（它守的是
// 算式本身，不是任何一次事故）。
func sliceCostCandidates(p1, p2, mult float64) (first, second, avg float64) {
	return p1 * mult, p2 * mult, ((p1 + p2) / 2) * mult
}

// hitMark 只报「对得上 / 差多少」，不替调用方下判断。
//
// ⚠️ 容差取 1e-6 而不是 0：这些数经过 float64 的加减，
// 而**一个因浮点尾数而报「都不命中」的判定，与一个真的都不命中的判定长得一样**。
func hitMark(got, want float64) string {
	if math.Abs(got-want) < 1e-6 {
		return "✅ 命中"
	}
	return fmt.Sprintf("❌ 差 %.4f", got-want)
}

// longToday 取某合约的**多头今仓**记录。
//
// ⚠️ 三个维度一个都不能少（20260911 修的洞：持仓缓存的键此前只有合约名，
// 于是同一合约的多空互相覆盖，而覆盖是静默的）。
func longToday(pos map[string]*def.CThostFtdcInvestorPositionField,
	inst string) *def.CThostFtdcInvestorPositionField {
	for _, p := range pos {
		if ctp.Text(p.InstrumentID[:]) != inst {
			continue
		}
		if p.PosiDirection != def.THOST_FTDC_PD_Long {
			continue
		}
		if p.PositionDate != def.THOST_FTDC_PSD_Today {
			continue
		}
		return p
	}
	return nil
}

func mustPositions(c *ctp.Client, timeout time.Duration,
	logf func(string, ...any)) map[string]*def.CThostFtdcInvestorPositionField {
	pos, err := c.Positions(timeout)
	if err != nil {
		logf("[sl] ⚠️ 查持仓失败：%v", err)
		return nil
	}
	return pos
}

// openOneLot 开一手多头今仓（**打卖一，立即成交**），并由 OpenCost 增量反解成交价。
//
// ⚠️ 为什么不直接读成交回报的价：OrderState 里没有成交价这一项，
// 而补它要动回调层。**OpenCost 的增量给的是同一个数，且它来自持仓本身** ——
// 这条路顺带让「柜台认为这一片值多少」与「我以为它值多少」变成同一个读数。
func openOneLot(c *ctp.Client, ex, inst string, mult float64,
	timeout time.Duration, logf func(string, ...any)) (float64, error) {
	md, err := c.MarketData(ex+"."+inst, timeout)
	if err != nil {
		return 0, err
	}
	before := openCostOf(c, inst, timeout, logf)
	// ⚠️ 挂涨停 ⇒ 一定成交（本命令要的是真持仓，不是挂单）。
	st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: float64(md.UpperLimitPrice)}, timeout)
	if err != nil || st.VolumeTraded == 0 {
		return 0, fmt.Errorf("开一手没成交：status=%q %s err=%v",
			string(st.Status), st.StatusMsg, err)
	}
	return filledPrice(c, inst, before, mult, timeout, logf)
}

// openOneLotResting 把腿 1 **挂在低一个价位上等成交**，而不是打卖一。
//
// # ⚠️ 它存在的理由：把「p2 > p1」从**赌行情方向**变成**靠买卖价差**
//
// #13 的第一个样本 p1 > p2 ⇒「先开的那片」与「价高的那片」是同一片，
// 混淆拆不开。要拆它得有一个 p2 > p1 的样本，而两腿都打卖一时，
// 那**完全取决于这段时间行情往哪边走** —— 20260911 夜盘为此空等了
// 12 分钟（rb）+ 20 分钟（ag）**两个窗口，一个样本都没拿到**。
//
//	⚠️ 而「等不到」这件事本身没有上限：它取决于行情，
//	**而行情不会因为我这边等着就转向**。
//
// ⇒ 换个造法：腿 1 挂 `最新价 − 一个最小变动价位`**等别人卖给我**，
// 腿 2 照旧打卖一。卖一总在最新价之上或与之齐平
// ⇒ **p2 ≥ p1 + 一个价位，由价差保证，与行情走向无关。**
//
// ⚠️ 代价是它**可能挂不上成交**：那时不留单 —— 撤掉并报错，
// 因为一笔留在柜台上的挂单会在下一次运行里变成「账上已有今仓」，
// 而那时的前提检查会拦下整条实验，**看起来像是别的毛病**。
func openOneLotResting(c *ctp.Client, ex, inst string, mult, tick float64,
	fillWait, timeout time.Duration, logf func(string, ...any)) (float64, error) {
	md, err := c.MarketData(ex+"."+inst, timeout)
	if err != nil {
		return 0, err
	}
	px := float64(md.LastPrice) - tick
	if px <= float64(md.LowerLimitPrice) {
		return 0, fmt.Errorf("⚠️ 挂价 %.4f 已到跌停 —— 不挂", px)
	}
	before := openCostOf(c, inst, timeout, logf)
	req := ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: px}
	st, err := c.Insert(req, timeout)
	if err != nil {
		return 0, err
	}
	logf("[sl] 腿1 挂在 %.4f（最新 %.4f − 一个价位）等成交，上限 %s ——",
		px, float64(md.LastPrice), fillWait)
	deadline := time.Now().Add(fillWait)
	for st.VolumeTraded == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		if s, ok := c.Order(st.OrderRef); ok {
			st = s
		}
	}
	if st.VolumeTraded == 0 {
		if err := c.Cancel(st.OrderRef, req); err != nil {
			return 0, fmt.Errorf("⚠️⚠️ 挂单没成交**而且撤不掉**，它还在柜台上：%w", err)
		}
		return 0, fmt.Errorf("⚠️ 腿1 挂在 %.4f 等了 %s 没成交，已撤 —— "+
			"不当结论，重跑（或把 -fillwait 调大）", px, fillWait)
	}
	return filledPrice(c, inst, before, mult, timeout, logf)
}

// openCostOf 读该合约多头今仓当前的 OpenCost；没有持仓时是 0。
func openCostOf(c *ctp.Client, inst string, timeout time.Duration,
	logf func(string, ...any)) float64 {
	if p := longToday(mustPositions(c, timeout, logf), inst); p != nil {
		return float64(p.OpenCost)
	}
	return 0
}

// filledPrice 由 OpenCost 的增量反解刚成交那一手的价。
func filledPrice(c *ctp.Client, inst string, before, mult float64,
	timeout time.Duration, logf func(string, ...any)) (float64, error) {
	p := longToday(mustPositions(c, timeout, logf), inst)
	if p == nil {
		return 0, fmt.Errorf("⚠️ 开完之后查不到今仓 —— 不猜价，直接停")
	}
	return (float64(p.OpenCost) - before) / mult, nil
}

// flattenOneLongToday 只平**一手**多头今仓 —— 用来退掉一个不合格的腿 2。
//
// ⚠️ 它与 flattenLongToday 是两件事：后者是收尾、清空；
// 这一个是**撤回一步**，账上还得留着腿 1。用错了会把腿 1 也平掉，
// 而那之后的一切读数仍然会打印得很正常。
func flattenOneLongToday(c *ctp.Client, ex, inst string, timeout time.Duration) error {
	md, err := c.MarketData(ex+"."+inst, timeout)
	if err != nil {
		return err
	}
	st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: float64(md.LowerLimitPrice)}, timeout)
	if err != nil || st.VolumeTraded == 0 {
		return fmt.Errorf("平一手没成交：status=%q %s err=%v",
			string(st.Status), st.StatusMsg, err)
	}
	return nil
}

// flattenLongToday 把该合约的多头**今仓**清干净。⚠️ 它不碰昨仓：
// 本命令造的片全是今天开的，而账上可能有别的实验留的过夜仓。
func flattenLongToday(c *ctp.Client, ex, inst string,
	timeout time.Duration, logf func(string, ...any)) error {
	for i := 0; i < 5; i++ {
		p := longToday(mustPositions(c, timeout, logf), inst)
		if p == nil || int(p.Position) == 0 {
			logf("[sl] 今仓已清空")
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
			return fmt.Errorf("平仓没成交：status=%q %s err=%v",
				string(st.Status), st.StatusMsg, err)
		}
	}
	return fmt.Errorf("⚠️ 平了 5 手还没清空 —— 停手，去看账户")
}
