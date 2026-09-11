package main

import (
	"flag"
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

// runCTPProfit 做一次**以盈利收尾**的往返，用来答 `rules_pending` #11。
//
// ⚠️ 它要分开的是这两个：
//
//	平仓**盈利**算进可用   ⇒ 平完 Available == Balance
//	盈利留到结算才给       ⇒ 平完 Available == Balance − CloseProfit
//
// ⚠️ **而亏损那一格答不了它**：20260910 夜盘已实测 CloseProfit = −100 时
// Available == Balance（亏被立即计入）。但「亏损立即扣、盈利留到结算」
// 是真实存在的做法 —— 两者在亏的样本上给出同一个数。
//
// ⚠️ **本命令一定会平仓**：无论盈亏、无论超时，退出前账上不留仓。
// 那不是礼貌，是条款 —— 20260910 白天那手过夜仓就是这条没写死的代价。
func runCTPProfit(args []string) error {
	fs := flag.NewFlagSet("ctp-profit", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 SHFE.rb2701（⚠️ 无默认值）")
	tick := fs.Float64("tick", 0, "最小变动价位（⚠️ 无默认值）")
	deadline := fs.Duration("deadline", 20*time.Minute,
		"⚠️ **硬截止**：到点无论盈亏一律平掉。留仓不是本命令的选项")
	every := fs.Duration("every", 10*time.Second, "轮询间隔")
	short := fs.Bool("short", false, "开**空**而不是开多（⚠️ 行情在往下走时，空头更可能等到盈利）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" || *tick <= 0 {
		return fmt.Errorf("⚠️ -symbol 与 -tick 都没有默认值")
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
	logf("[pf] 安全阀：AllowOrder=%v MaxVolume=%d", env.AllowOrder, env.MaxVolume)
	ex, inst := ctp.SplitSymbol(*symbol)

	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	// ⚠️ 开仓挂**对自己不利**的那一端 ⇒ 保证成交（本命令**要**成交，与 ctp-fee 相反）：
	// 开多挂涨停、开空挂跌停。写成元组，让「方向与挂价端成对」在语法上成立。
	openDir, openPx := def.TThostFtdcDirectionType(def.THOST_FTDC_D_Buy), float64(md.UpperLimitPrice)
	if *short {
		openDir, openPx = def.TThostFtdcDirectionType(def.THOST_FTDC_D_Sell), float64(md.LowerLimitPrice)
	}
	// ⚠️ 先记下**这一笔之前**的当日累计平仓盈亏。
	//
	// 20260910 夜盘第一版拿账户里的 `CloseProfit` 直接判正负 —— 而它是**当日累计**：
	// 那一轮它是 −120，而这一笔只亏了 −20（先前那手昨仓亏了 −100）。
	// ⇒ **当日已有大亏时，一笔真盈利也会被判成「答不了」。**
	//
	//	⚠️ 一个「累计量」被当成「本次量」用，而它在第一次跑的时候恰好相等
	//	（当日还没有别的平仓）—— 于是第一版看起来完全正常。
	before, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	cp0 := float64(before.CloseProfit)
	logf("[pf] 这一笔之前的当日累计平仓盈亏 = %.4f", cp0)
	st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: openDir, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: openPx}, *timeout)
	if err != nil || st.VolumeTraded == 0 {
		return fmt.Errorf("⚠️ 开仓没成交（status=%q %s err=%v）—— 本轮什么都没发生",
			string(st.Status), st.StatusMsg, err)
	}
	logf("[pf] 已开%s 1 手", map[bool]string{false: "多", true: "空"}[*short])

	// ⚠️ 从**持仓**里读开仓价，不从行情猜：成交价与挂价无关。
	//
	// ⚠️ **要重试**：20260910 夜盘第二轮开空之后立刻查，柜台的持仓快照**还没更新**，
	// 于是读到零、走了作废分支 —— 平掉、干净、如实报「答不了」，
	// 安全路径没问题，**但白白花掉一次往返**。
	//
	//	⚠️ 「成交回报到了」与「持仓查询看得见它」是两件事，中间隔着柜台的一次刷新。
	//	而第一版把它们当成了同一件事 —— 因为多头那一轮**恰好赶上了**。
	entry := 0.0
	for try := 0; try < 5 && entry == 0; try++ {
		if try > 0 {
			time.Sleep(2 * time.Second)
		}
		pos, err := c.Positions(*timeout)
		if err != nil {
			continue
		}
		for _, p := range pos {
			if int(p.Position) > 0 && float64(p.OpenCost) > 0 {
				entry = float64(p.OpenCost) / float64(p.Position) / 10
			}
		}
		if entry == 0 {
			logf("[pf] 持仓快照还没更新（第 %d 次），等 2 秒再看", try+1)
		}
	}
	if entry == 0 {
		logf("[pf] ⚠️ 读不到开仓价 —— **立刻平掉**，本轮作废")
		return flattenNow(c, ex, inst, *timeout, logf, cp0, *short)
	}
	// 多头等涨、空头等跌 —— **判据是「这一笔平掉会赚」**，不是「价格涨了」。
	want := entry + *tick
	if *short {
		want = entry - *tick
	}
	logf("[pf] 开仓价 %.2f —— 等最新价 %s %.2f", entry,
		map[bool]string{false: "≥", true: "≤"}[*short], want)

	// ⚠️ 硬截止用**绝对时刻**，不用轮次：轮次会被一次慢应答拖过收盘。
	stop := time.Now().Add(*deadline)
	for {
		if time.Now().After(stop) {
			logf("[pf] ⚠️ **到硬截止了，无论盈亏平掉**")
			return flattenNow(c, ex, inst, *timeout, logf, cp0, *short)
		}
		q, err := c.MarketData(*symbol, *timeout)
		if err != nil {
			logf("[pf] ⚠️ 取行情失败：%v —— 继续等，但截止不变", err)
			time.Sleep(*every)
			continue
		}
		// ⚠️ **判据用的是「平掉那一笔能成交在哪」，不是最新价。**
		//
		// 20260910 夜盘第四轮：开空 3139，最新价跌到 3138（对空头是 +1 跳），
		// 判据满足、平掉 —— 而这一笔的平仓盈亏是 **0**。
		// 因为买入平仓吃的是**卖一**，那时卖一多半还是 3139。
		//
		//	⚠️ **`LastPrice` 不是你能成交的价** —— 平仓要**穿过买卖价差**，
		//	而「最新价对我有利」与「我平掉能赚」之间，隔着那个价差。
		//
		// ⇒ 多头平仓卖给**买一**、空头平仓买自**卖一**，判据就用那一端。
		// 取不到盘口时退回最新价，并**说出来**（那一轮的判据弱一格）。
		exit := float64(q.BidPrice1)
		side := "买一"
		if *short {
			exit, side = float64(q.AskPrice1), "卖一"
		}
		if exit <= 0 || exit > float64(q.UpperLimitPrice) || exit < float64(q.LowerLimitPrice) {
			exit, side = float64(q.LastPrice), "最新价（⚠️ 盘口取不到，判据弱一格）"
		}
		logf("[pf] %s %.2f（开仓 %.2f，平掉能赚 %+.2f）", side, exit, entry,
			map[bool]float64{false: (exit - entry) * 10, true: (entry - exit) * 10}[*short])
		if (!*short && exit >= want) || (*short && exit <= want) {
			logf("[pf] ⇒ **够了，平掉**（平仓盈亏应当为正）")
			return flattenNow(c, ex, inst, *timeout, logf, cp0, *short)
		}
		time.Sleep(*every)
	}
}

// flattenNow 平掉今仓并把平完那一刻的账户打出来。
//
// ⚠️ 它是 runCTPProfit 的**唯一**出口 —— 每一条 return 都经过它。
// 那是刻意的：一个「有些路径会平、有些不会」的命令，
// 在出事的那条路径上与正常路径长得一模一样。
func flattenNow(c *ctp.Client, ex, inst string, timeout time.Duration,
	logf func(string, ...any), cp0 float64, short bool) error {
	md, err := c.MarketData(ex+"."+inst, timeout)
	if err != nil {
		return fmt.Errorf("⚠️⚠️ 拿不到行情，**仓还在**：%w", err)
	}
	// 平仓同样挂对自己不利的那一端 ⇒ 保证成交：平多发卖挂跌停、平空发买挂涨停。
	closeDir, closePx := def.TThostFtdcDirectionType(def.THOST_FTDC_D_Sell), float64(md.LowerLimitPrice)
	if short {
		closeDir, closePx = def.TThostFtdcDirectionType(def.THOST_FTDC_D_Buy), float64(md.UpperLimitPrice)
	}
	st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: closeDir, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: closePx}, timeout)
	if err != nil || st.VolumeTraded == 0 {
		return fmt.Errorf("⚠️⚠️ **平仓没成交，仓还在账上** —— status=%q %s err=%v",
			string(st.Status), st.StatusMsg, err)
	}
	a, err := c.Account(timeout)
	if err != nil {
		return err
	}
	bal, avail := float64(a.Balance), float64(a.Available)
	// ⚠️ **这一笔的**平仓盈亏 = 当日累计的增量。见 runCTPProfit 里 cp0 的注释。
	cp := float64(a.CloseProfit) - cp0
	logf("[pf] 已平。Balance=%.4f Available=%.4f CurrMargin=%.4f", bal, avail, float64(a.CurrMargin))
	logf("[pf] 当日累计平仓盈亏 %.4f → %.4f，**这一笔 = %.4f**",
		cp0, float64(a.CloseProfit), cp)
	logf("[pf] ⇒ Balance − Available = %.4f", bal-avail)
	switch {
	case cp <= 0:
		logf("[pf] ⚠️ **CloseProfit 不为正（%.4f）—— 这一轮答不了 #11**。"+
			"「盈利算不算进可用」与「亏损算不算」是两件事，别拿这个数去答那个问题", cp)
	case bal-avail == 0:
		logf("[pf] ⇒ ✅ 平仓**盈利**已计入可用（Available == Balance，而 CloseProfit=%.4f>0）", cp)
	default:
		logf("[pf] ⇒ ✅ 平仓盈利**没有**计入可用：差 %.4f（CloseProfit=%.4f）", bal-avail, cp)
	}
	return nil
}
