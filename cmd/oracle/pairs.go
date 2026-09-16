package main

import (
	"flag"
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/dream-until-dawn/futures-position-simulator-go/ctperr"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// pairCase 是一笔**恰好违反两项**的委托。两项各自单独违反时的码都已在语料里量过，
// 于是回来的码等于谁，就是谁先。
//
// ⚠️ 与 rejectCases 分开立着（那张清单「只增不改」，且它每条只违反一项）：
// 一条清单同时承担「单项的码」与「两项的先后」会让 ctperr 的归类器读不清 ——
// `violates` 恰好一项才归得了类。
type pairCase struct {
	Name     string
	Dir      def.TThostFtdcDirectionType
	Off      def.TThostFtdcOffsetFlagType
	Price    func(md *def.CThostFtdcDepthMarketDataField, tick float64) float64
	A, B     ctperr.Reason
	AName    string
	BName    string
	Violates []string
	// Why 说清这一笔为什么**恰好**违反那两项 —— 多违反一项，回来的码就归不了因。
	Why string
	// Control 为真表示这一对**已经量过**，本轮拿它当正对照：
	// 复现不出已知答案时，同一轮里其余几对一个都不作数。
	Control bool
	// Predict 是**事前登记**的预言（空表示没有预言）。
	// ⚠️ 事后再说「我本来就觉得会这样」不算预言，所以它写死在代码里、随提交一起落盘。
	Predict string
}

// pairCases：⚠️ 第一条是**正对照**，必须排在最前。
var pairCases = []pairCase{
	{
		Name: "最小变动价位 × 涨跌停（正对照，CTP 上已量过）",
		Dir:  def.THOST_FTDC_D_Buy, Off: def.THOST_FTDC_OF_Open,
		Price: func(md *def.CThostFtdcDepthMarketDataField, tick float64) float64 {
			return float64(md.LowerLimitPrice) - tick/2
		},
		A: ctperr.ReasonPriceTick, AName: "最小变动价位",
		B: ctperr.ReasonBelowLowerLimit, BName: "低于跌停",
		Violates: []string{"最小变动价位", "涨跌停"},
		Why:      "跌停价减半个 tick：既不是整倍数，又在跌停之下；买单挂在跌停之下，放过了也成不了交",
		Control:  true,
		Predict:  "最小变动价位（20260911 夜盘在 INE 上量到 48，不是 50）",
	},
	{
		Name: "可平量 × 最小变动价位",
		Dir:  def.THOST_FTDC_D_Sell, Off: def.THOST_FTDC_OF_CloseYesterday,
		Price: func(md *def.CThostFtdcDepthMarketDataField, tick float64) float64 {
			return float64(md.LowerLimitPrice) + tick/2
		},
		A: ctperr.ReasonCloseYesterdayExceeds, AName: "可平量",
		B: ctperr.ReasonPriceTick, BName: "最小变动价位",
		Violates: []string{"可平量", "最小变动价位"},
		Why:      "账上无仓时平昨 + 跌停价加半个 tick（不是整倍数，但仍在价内）",
		Predict:  "可平量 —— 按 20260916 量到的分层：可平量走 CTP 前置那一层（CTP 码空间、无交易所委托号），价格类在再往后那一层。⚠️ 这与快期那侧相反（表第 2 行：最小变动价位先）",
	},
	{
		Name: "可平量 × 涨跌停",
		Dir:  def.THOST_FTDC_D_Sell, Off: def.THOST_FTDC_OF_CloseYesterday,
		Price: func(md *def.CThostFtdcDepthMarketDataField, tick float64) float64 {
			return float64(md.LowerLimitPrice) - tick
		},
		A: ctperr.ReasonCloseYesterdayExceeds, AName: "可平量",
		B: ctperr.ReasonBelowLowerLimit, BName: "低于跌停",
		Violates: []string{"可平量", "涨跌停"},
		Why:      "账上无仓时平昨 + 跌停价减一个 tick（仍是整倍数 ⇒ 只违反越界这一项）",
		Predict:  "可平量 —— 同上。⚠️ 与快期那侧相反（表第 3 行：涨跌停先）",
	},
}

// runCTPPairs 量「两项都不是时段」的拒绝优先级。
//
// ⚠️ 前提与 `ctp-priority` 相反：这条要合约此刻**报得进单**（对照组必须挂上），
// 否则时段那一项会混进来，变成三项同时违反。
func runCTPPairs(args []string) error {
	fs := flag.NewFlagSet("ctp-pairs", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 SHFE.rb2701（⚠️ 无默认值）")
	out := fs.String("out", "", "语料落盘目录（⚠️ 不给就不跑）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：会真的发出几笔（预期全被拒）委托")
	}
	if *out == "" {
		return fmt.Errorf("⚠️ -out 没有给 —— 本命令不许只打 console：判别力在几个码上")
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
	c.Valve = ctpValve(env)
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}
	ex, inst := ctp.SplitSymbol(*symbol)

	tk, err := resolveTick(c, *symbol, 0, *timeout, logf)
	if err != nil {
		return err
	}
	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	logf("[pair] 行情 最新=%.2f 涨停=%.2f 跌停=%.2f",
		float64(md.LastPrice), float64(md.UpperLimitPrice), float64(md.LowerLimitPrice))

	ctrl, cerr := runControl(c, ex, inst, md, tk, *timeout, logf)
	obs := []rejectObservation{ctrl}
	if cerr != nil {
		if _, werr := writeRejectCorpus(*out, obs, logf); werr != nil {
			logf("[pair] ⚠️ 语料落盘失败：%v", werr)
		}
		return fmt.Errorf("%w\n⚠️ 本命令要的是**报得进单**的时段 —— 报不进的时段该跑 `ctp-priority`", cerr)
	}

	controlHeld := false
	for i, pc := range pairCases {
		aCode, hasA := ctperr.Lookup(types.Exchange(ex), pc.A)
		bCode, hasB := ctperr.Lookup(types.Exchange(ex), pc.B)
		px := pc.Price(md, tk.Value)
		logf("")
		logf("[pair] %d/%d %s → dir=%q off=%q @%.2f（%s）",
			i+1, len(pairCases), pc.Name, string(pc.Dir), string(pc.Off), px, pc.Why)
		if pc.Predict != "" {
			logf("[pair]   事前登记的预言：**%s**", pc.Predict)
		}
		st, insErr := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: pc.Dir, Offset: pc.Off, Volume: 1, LimitPrice: px}, *timeout)
		outcome := "rejected"
		winner := ""
		switch {
		case insErr != nil:
			outcome = "error"
			logf("[pair]   ⇒ Insert 报错：%v", insErr)
		case st.VolumeTraded > 0:
			outcome = "traded"
			logf("[pair]   ⚠️⚠️ **成交了 %d 手** —— 跑 `ctp-flatten -symbol %s`", st.VolumeTraded, *symbol)
		case st.Alive():
			outcome = "resting"
			logf("[pair]   ⇒ **挂上了** —— 这一笔此刻不非法，它没验到任何东西，撤单")
			if xerr := c.Cancel(st.OrderRef, ctp.OrderReq{Exchange: ex, Instrument: inst,
				Direction: pc.Dir, Offset: pc.Off, Volume: 1, LimitPrice: px}); xerr != nil {
				logf("[pair]   ⚠️ 撤单失败：%v", xerr)
			}
		default:
			got, hasCode := codeOf(st)
			if !hasCode {
				logf("[pair]   ⇒ 被拒但一个码都没有 —— 这一对判不了")
				break
			}
			var why string
			winner, why = pairVerdict(got,
				side{Code: aCode, Name: pc.AName, Has: hasA},
				side{Code: bCode, Name: pc.BName, Has: hasB})
			if winner == "" {
				logf("[pair]   ⇒ %s", why)
			} else {
				logf("[pair]   ⇒ **柜台报的是「%s」**（%s）", winner, why)
			}
		}
		if pc.Control {
			controlHeld = winner == pc.AName
			if !controlHeld {
				// ⚠️ 正对照复现不出已知答案 ⇒ 后面几对**一个都不作数**，但观测照落盘：
				// 「复现不出来」本身是这一轮最有信息的东西。
				logf("[pair] ⚠️⚠️ **正对照复现不出已知答案**（应为「%s」，得到「%s」）—— "+
					"本轮其余各对一律不作数", pc.AName, winner)
			}
		}
		rc := rejectCase{Name: pc.Name, Dir: pc.Dir, Off: pc.Off, Violates: pc.Violates, Why: pc.Why}
		obs = append(obs, observe(c.TradingDay(), nowClock(), ex, inst, rc, st, outcome, tk))
	}
	logf("")
	if _, werr := writeRejectCorpus(*out, obs, logf); werr != nil {
		return werr
	}
	if !controlHeld {
		return fmt.Errorf("⚠️ 正对照没有复现已知答案 —— 观测已落盘，但本轮不给结论")
	}
	return nil
}
