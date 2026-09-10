package main

import (
	"flag"
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

// rejectCase 是一次**故意打成非法**的报单。
//
// ⚠️ 每一条都必须**双重安全**：既非法、**即使被柜台放过也成不了交**。
// 判据只有一条 —— 买单挂在**远低于市价**处、卖单挂在**远高于市价**处：
//
//	买 @ 低价   没人肯那么便宜卖 ⇒ 挂着不成交
//	卖 @ 高价   没人肯那么贵买   ⇒ 挂着不成交
//
// ⚠️ 反过来的组合（买挂高、卖挂低）**会当场成交** —— 而一个只想被拒的探针
// 变成一次真实开仓，在日志里与成功的探针长得一模一样。
// 这正是 `TestRestingPriceMatchesDirection` 在 ctp-order 上钉的那件事，
// 而本文件的清单是**数据**不是代码，所以判据写在这里、由 TestRejectCasesAreNonExecutable 钉。
type rejectCase struct {
	Name string
	Dir  def.TThostFtdcDirectionType
	Off  def.TThostFtdcOffsetFlagType
	// Price 相对涨跌停的偏移，由 priceOf 解释。用相对量是因为涨跌停每天都变。
	Price func(md *def.CThostFtdcDepthMarketDataField) float64
	Why   string
}

// rejectCases 是清单本身。⚠️ 只增不改：一条被实测过的用例改掉之后，
// 它此前那次观测就没有对应的输入了。
var rejectCases = []rejectCase{
	{
		Name: "非最小变动价位整倍数",
		Dir:  def.THOST_FTDC_D_Buy, Off: def.THOST_FTDC_OF_Open,
		Price: func(md *def.CThostFtdcDepthMarketDataField) float64 { return float64(md.LowerLimitPrice) + 0.5 },
		Why:   "rb 的最小变动价位是 1，挂 .5 应当被拒；且是**买单挂在跌停附近**，放过了也成不了交",
	},
	{
		Name: "低于跌停",
		Dir:  def.THOST_FTDC_D_Buy, Off: def.THOST_FTDC_OF_Open,
		Price: func(md *def.CThostFtdcDepthMarketDataField) float64 { return float64(md.LowerLimitPrice) - 1 },
		Why:   "越界应当被拒；且是**买单挂在跌停之下**，放过了也成不了交",
	},
	{
		Name: "高于涨停",
		Dir:  def.THOST_FTDC_D_Sell, Off: def.THOST_FTDC_OF_Open,
		Price: func(md *def.CThostFtdcDepthMarketDataField) float64 { return float64(md.UpperLimitPrice) + 1 },
		Why:   "越界应当被拒；且是**卖单挂在涨停之上**，放过了也成不了交",
	},
	{
		Name: "平仓位不足（账上无仓时平昨）",
		Dir:  def.THOST_FTDC_D_Sell, Off: def.THOST_FTDC_OF_CloseYesterday,
		Price: func(md *def.CThostFtdcDepthMarketDataField) float64 { return float64(md.UpperLimitPrice) },
		Why:   "账上没有昨仓时平昨应当被拒；且是**卖单挂在涨停**，放过了也成不了交",
	},
}

// runCTPReject 把 rejectCases 逐条发出去，记下柜台给的**数值错误码**。
//
// ⚠️ 它是 `rules_pending` #6 的那一半：快期的拒因**文案**已实测（§9），
// 而 CTP 的**数值错误码**一个都没有，于是 `ctperr` 包的取值**不得填**——
// 那个包至今未开始，卡的就是这里。
//
// ⚠️ 本命令**不判断哪个码对应哪种拒因**，它只如实记录「这个输入 → 这个码」。
// 归类是另一件事，要有多个样本之后才做。
func runCTPReject(args []string) error {
	fs := flag.NewFlagSet("ctp-reject", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 SHFE.rb2701（⚠️ 无默认值）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：真实委托的合约必须显式指定")
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
	logf("[rej] 安全阀：AllowOrder=%v MaxVolume=%d", env.AllowOrder, env.MaxVolume)

	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	logf("[rej] 行情 最新=%.2f 涨停=%.2f 跌停=%.2f",
		float64(md.LastPrice), float64(md.UpperLimitPrice), float64(md.LowerLimitPrice))

	ex, inst := ctp.SplitSymbol(*symbol)
	traded := 0
	for i, rc := range rejectCases {
		px := rc.Price(md)
		logf("")
		logf("[rej] %d/%d %s → dir=%q off=%q @%.2f", i+1, len(rejectCases),
			rc.Name, string(rc.Dir), string(rc.Off), px)
		st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: rc.Dir, Offset: rc.Off, Volume: 1, LimitPrice: px}, *timeout)
		switch {
		case err != nil:
			logf("[rej]   ⇒ Insert 报错：%v", err)
		case st.VolumeTraded > 0:
			// ⚠️ 一条只想被拒的探针**成交了** —— 那是账上多了敞口，必须喊。
			traded++
			logf("[rej]   ⚠️⚠️ **成交了 %d 手** —— 这条用例的「成不了交」判据不成立，"+
				"账上现在有敞口，**跑 ctp-flatten**", st.VolumeTraded)
		case st.Alive():
			// 挂上了 = 没被拒。撤掉，并说清这条用例没验到它要验的东西。
			logf("[rej]   ⇒ **没被拒，挂上了**（status=%q）—— 这条用例此刻不非法，撤单", string(st.Status))
			if err := c.Cancel(st.OrderRef, ctp.OrderReq{Exchange: ex, Instrument: inst,
				Direction: rc.Dir, Offset: rc.Off, Volume: 1, LimitPrice: px}); err != nil {
				logf("[rej]   ⚠️ 撤单失败：%v", err)
			}
		default:
			logf("[rej]   ⇒ status=%q  **ErrorID=%d**  %s", string(st.Status), st.ErrorID, st.StatusMsg)
		}
	}
	logf("")
	if traded > 0 {
		return fmt.Errorf("⚠️⚠️ **有 %d 条用例成交了**，账上有敞口 —— 立刻跑 `ctp-flatten`", traded)
	}
	logf("[rej] ⇒ 跑完 %d 条，**没有任何一条成交**（这是本命令的安全前提，不是运气）",
		len(rejectCases))
	return nil
}
