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

// runCTPFee 在**若干个不同价位**上各挂一笔**成不了交**的开仓单，
// 记下柜台冻结的手续费，用来定手续费公式的形状。
//
// ⚠️ 它要答的是 `rules_pending` #5 与 #8，而它存在的理由是一个**方法上的**问题：
//
//	20260910 夜盘只有两个点（挂单 @3304 冻 3.309、成交 @3137 收 3.142）。
//	两点解出「按金额 0.0001 + 每手 0.005」，**残差恒为零** ——
//	⚠️ **两点定两参数，那是拟合不是验证。**
//
// ⇒ 三个点起才有余量，余量才是证据。快期那侧当初是在**六个**成交价上核的。
//
// ⚠️ **不需要成交**：挂单就冻手续费。于是这个探针可以做到
// **一笔都不成交**而拿到任意多个点 —— 买单一律挂在**跌停附近**，
// 没人肯那么便宜卖，挂得上、成不了。
func runCTPFee(args []string) error {
	fs := flag.NewFlagSet("ctp-fee", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 SHFE.rb2701（⚠️ 无默认值）")
	points := fs.Int("points", 5, "取几个价位（⚠️ 三个起才有余量，两点是拟合不是验证）")
	tick := fs.Float64("tick", 0, "最小变动价位（⚠️ **无默认值**）。"+
		"不对齐的价位会被交易所以 `48:价格非最小变动价位的倍数` 直接拒掉 —— "+
		"20260910 夜盘第一版没对齐，六个点里**五个没挂上**")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：真实委托的合约必须显式指定")
	}
	if *tick <= 0 {
		return fmt.Errorf("⚠️ -tick 没有默认值：不同品种不一样，猜错的表现是" +
			"「五个点里四个没挂上」，而那看起来像柜台的毛病")
	}
	if *points < 3 {
		return fmt.Errorf("⚠️ -points 至少 3：两点定两参数、残差恒为零，"+
			"那是拟合不是验证（得到 %d）", *points)
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
	logf("[fee] 安全阀：AllowOrder=%v MaxVolume=%d", env.AllowOrder, env.MaxVolume)
	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	low, high := float64(md.LowerLimitPrice), float64(md.LastPrice)
	logf("[fee] 行情 最新=%.2f 涨停=%.2f 跌停=%.2f", high, float64(md.UpperLimitPrice), low)

	ex, inst := ctp.SplitSymbol(*symbol)
	type point struct {
		Px  float64
		Fee float64
	}
	var pts []point
	traded := 0
	// ⚠️ 价位取在**跌停到最新价的下半段**：既拉得开（费率按金额时差别才看得出来），
	// 又全部远低于市价 ⇒ 买单成不了交。
	for i := 0; i < *points; i++ {
		raw := low + float64(i)*(high-low)/(2*float64(*points))
		// ⚠️ 对齐到最小变动价位，**并且向下取** —— 向上可能越过跌停。
		px := math.Floor(raw / *tick) * *tick
		st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
			Volume: 1, LimitPrice: px}, *timeout)
		if err != nil {
			logf("[fee] @%.2f Insert 报错：%v", px, err)
			continue
		}
		if st.VolumeTraded > 0 {
			// ⚠️ 一笔本该挂着的单成交了 —— 账上多了敞口，立刻停下并喊。
			traded++
			return fmt.Errorf("⚠️⚠️ **@%.2f 成交了 %d 手** —— "+
				"「挂得上、成不了」的判据在这一档上不成立，账上有敞口，**立刻跑 ctp-flatten**",
				px, st.VolumeTraded)
		}
		if !st.Alive() {
			logf("[fee] @%.2f **没挂上**（status=%q %s）—— 这一档没有数",
				px, string(st.Status), st.StatusMsg)
			continue
		}
		acct, err := c.Account(*timeout)
		if err != nil {
			return err
		}
		fee := float64(acct.FrozenCommission)
		pts = append(pts, point{px, fee})
		logf("[fee] @%-8.2f 冻结手续费=%.6f  冻结保证金=%.4f",
			px, fee, float64(acct.FrozenMargin))
		if err := c.Cancel(st.OrderRef, ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
			Volume: 1, LimitPrice: px}); err != nil {
			logf("[fee] ⚠️ 撤单失败：%v —— 后面那些点会带上这一笔的冻结额，**停下**", err)
			return err
		}
	}
	logf("")
	if traded > 0 {
		return fmt.Errorf("⚠️⚠️ 有成交，账上有敞口 —— 立刻 ctp-flatten")
	}
	if len(pts) < 3 {
		return fmt.Errorf("⚠️ 只拿到 %d 个点（要 3 个以上）—— **两点定两参数是拟合不是验证**",
			len(pts))
	}
	// 用**前两点**解出两个参数，再拿其余点去**验**它 —— 余量在这里。
	m := (pts[1].Fee - pts[0].Fee) / ((pts[1].Px - pts[0].Px) * 10)
	v := pts[0].Fee - pts[0].Px*10*m
	logf("[fee] 用前两点解：按金额=%.10g  每手=%.10g（⚠️ 乘数按 10 代入，rb 如此）", m, v)
	logf("[fee] ⇒ 拿其余 %d 个点去验它：", len(pts)-2)
	worst := 0.0
	for _, p := range pts[2:] {
		want := p.Px*10*m + v
		d := p.Fee - want
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
		logf("[fee]   @%-8.2f 实测 %.6f  预测 %.6f  残差 %.3g", p.Px, p.Fee, want, p.Fee-want)
	}
	logf("[fee] ⇒ 最大残差 %.3g —— ⚠️ **这个数才是证据**；前两点的残差恒为零，说明不了任何事", worst)
	return nil
}
