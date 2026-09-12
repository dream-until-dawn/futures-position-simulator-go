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
	// Price 由涨跌停与**最小变动价位**一起算出来。用相对量是因为涨跌停每天都变；
	// ⚠️ **带 tick 是 20260911 夜盘补的，而它补的是一次真实的误标**：
	// 原先偏移写死成 ±0.5 / ±1（注释里明写「rb 的最小变动价位是 1」）。
	// 拿它去跑 `INE.bc2611`（tick = **10**）时，「低于跌停 @87899」这一笔
	// **同时**违反了步长，柜台报的是步长那个码（48），
	// **而输出上标着「低于跌停」** —— 一条用例测的东西与它的名字不是一回事。
	//
	//	⚠️ 更糟的是它看起来成功了：命令打印「跑完 4 条，没有任何一条成交」，
	//	四条都拿到了码，**而其中两条的码属于另一种拒因**。
	//
	// ⇒ 越界那两条改成 ±tick（仍是整倍数），非整倍数那条改成 +tick/2。
	Price func(md *def.CThostFtdcDepthMarketDataField, tick float64) float64
	// Violates 是这次输入**确知违反**的那几项。⚠️ 它不是 Name 的复述：
	// Name 是标签，本栏是**判据** —— 一条同时违反两项的输入，
	// 拿到的码归不给其中任何一项（20260911 `bc` 那次正是）。
	Violates []string
	Why      string
}

// rejectCases 是清单本身。⚠️ 只增不改：一条被实测过的用例改掉之后，
// 它此前那次观测就没有对应的输入了。
var rejectCases = []rejectCase{
	{
		Name: "非最小变动价位整倍数",
		Dir:  def.THOST_FTDC_D_Buy, Off: def.THOST_FTDC_OF_Open,
		Price: func(md *def.CThostFtdcDepthMarketDataField, tick float64) float64 {
			return float64(md.LowerLimitPrice) + tick/2
		},
		Violates: []string{"最小变动价位"},
		Why:      "半个最小变动价位一定不是整倍数（不论 tick 多大）；且是**买单挂在跌停附近**，放过了也成不了交",
	},
	{
		Name: "低于跌停",
		Dir:  def.THOST_FTDC_D_Buy, Off: def.THOST_FTDC_OF_Open,
		Price: func(md *def.CThostFtdcDepthMarketDataField, tick float64) float64 {
			return float64(md.LowerLimitPrice) - tick
		},
		Violates: []string{"涨跌停"},
		Why:      "越界应当被拒，而**减一个 tick 仍是整倍数** ⇒ 只违反越界这一项；且是**买单挂在跌停之下**，放过了也成不了交",
	},
	{
		Name: "高于涨停",
		Dir:  def.THOST_FTDC_D_Sell, Off: def.THOST_FTDC_OF_Open,
		Price: func(md *def.CThostFtdcDepthMarketDataField, tick float64) float64 {
			return float64(md.UpperLimitPrice) + tick
		},
		Violates: []string{"涨跌停"},
		Why:      "越界应当被拒，而**加一个 tick 仍是整倍数** ⇒ 只违反越界这一项；且是**卖单挂在涨停之上**，放过了也成不了交",
	},
	{
		Name: "平仓位不足（账上无仓时平昨）",
		Dir:  def.THOST_FTDC_D_Sell, Off: def.THOST_FTDC_OF_CloseYesterday,
		Price: func(md *def.CThostFtdcDepthMarketDataField, tick float64) float64 {
			return float64(md.UpperLimitPrice)
		},
		Violates: []string{"可平量"},
		Why:      "账上没有昨仓时平昨应当被拒；且是**卖单挂在涨停**，放过了也成不了交",
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
	tick := fs.Float64("tick", 0, "最小变动价位（⚠️ **无默认值**）。"+
		"⚠️ 猜错的表现不是报错，是**一条用例测到了另一种拒因**，而输出上仍标着原来那个名字")
	out := fs.String("out", "", "把观测落成**机器可读**的语料到该目录（留空则只打日志）。"+
		"⚠️ 20260912 评审判为必修：此前这些码**只活在提交正文里**，而 ctperr 要开工得先让它变成数据。"+
		"⚠️ 语料**只收码不收原话** —— StatusMsg 是柜台自由文本，按纪律不进库")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *tick <= 0 {
		return fmt.Errorf("⚠️ -tick 没有默认值：bc 是 10、rb 是 1、sc 是 0.1 —— " +
			"猜错了本命令**照样跑完四条、照样拿到四个码**，而其中两条的码属于另一种拒因")
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
	var obs []rejectObservation
	// ⚠️ 每一条用例都要产出一条观测，**包括没被拒的那些** ——
	// 否则语料里只剩成功的那几条，而那看起来覆盖得很齐。
	record := func(rc rejectCase, st ctp.OrderState, outcome string) {
		o := rejectObservation{
			TradingDay: c.TradingDay(), Exchange: ex, Instrument: inst,
			Case: rc.Name, Violates: rc.Violates,
			Offset: string(rc.Off), Outcome: outcome, Source: "probe",
		}
		// ⚠️ 两个码位都用指针：**「缺」与「零」必须分得开**。
		// `ErrorID == 0` 恰恰是价格类拒单的常态（它们不经过 RspInfo）。
		if st.ErrorID != 0 {
			id := st.ErrorID
			o.ErrorID = &id
		}
		if code, ok := msgCode(st.StatusMsg); ok {
			o.ExchangeCode = &code
		}
		obs = append(obs, o)
	}
	for i, rc := range rejectCases {
		px := rc.Price(md, *tick)
		logf("")
		logf("[rej] %d/%d %s → dir=%q off=%q @%.2f", i+1, len(rejectCases),
			rc.Name, string(rc.Dir), string(rc.Off), px)
		st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: rc.Dir, Offset: rc.Off, Volume: 1, LimitPrice: px}, *timeout)
		switch {
		case err != nil:
			logf("[rej]   ⇒ Insert 报错：%v", err)
			record(rc, st, "error")
		case st.VolumeTraded > 0:
			// ⚠️ 一条只想被拒的探针**成交了** —— 那是账上多了敞口，必须喊。
			traded++
			logf("[rej]   ⚠️⚠️ **成交了 %d 手** —— 这条用例的「成不了交」判据不成立，"+
				"账上现在有敞口，**跑 ctp-flatten**", st.VolumeTraded)
			record(rc, st, "traded")
		case st.Alive():
			// 挂上了 = 没被拒。撤掉，并说清这条用例没验到它要验的东西。
			logf("[rej]   ⇒ **没被拒，挂上了**（status=%q）—— 这条用例此刻不非法，撤单", string(st.Status))
			if err := c.Cancel(st.OrderRef, ctp.OrderReq{Exchange: ex, Instrument: inst,
				Direction: rc.Dir, Offset: rc.Off, Volume: 1, LimitPrice: px}); err != nil {
				logf("[rej]   ⚠️ 撤单失败：%v", err)
			}
			record(rc, st, "resting")
		default:
			logf("[rej]   ⇒ status=%q  **ErrorID=%d**  %s", string(st.Status), st.ErrorID, st.StatusMsg)
			record(rc, st, "rejected")
		}
	}
	logf("")
	// ⚠️ **落盘排在「有成交就报错」之前**：成交了也要把观测留下来 ——
	// 那一轮恰恰是最该留证的一轮，而按错误路径提前返回会把它丢掉。
	// 同 `ctp-order` 那条「拍失败不许提前返回」的理由。
	var corpusErr error
	if *out != "" {
		if _, err := writeRejectCorpus(*out, obs, logf); err != nil {
			corpusErr = err
		}
	}
	if traded > 0 {
		return fmt.Errorf("⚠️⚠️ **有 %d 条用例成交了**，账上有敞口 —— 立刻跑 `ctp-flatten`", traded)
	}
	if corpusErr != nil {
		return corpusErr
	}
	logf("[rej] ⇒ 跑完 %d 条，**没有任何一条成交**（这是本命令的安全前提，不是运气）",
		len(rejectCases))
	return nil
}
