package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/dream-until-dawn/futures-position-simulator-go/ctperr"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// sessionItem 是八项校验里的「是否在交易时段内」（order.CheckSession）在语料里的名字。
//
// ⚠️ 本库这一项一直标着**查不了**，而快期那侧**根本不查**
// （kq_facts 48：313 笔没被拒的委托里 217 笔落在时段之外）。
// ⇒ 在快期那个口子上，一个查时段的实现与一个不查的实现**对拍结果相同**，那是盲区不是待办。
// 本命令是它在 CTP 侧的第一次实测。
const sessionItem = "交易时段"

// priorityVerdict 判「同时违反两项时，柜台报的是哪一项」。纯函数：离线可测。
//
// ⚠️ 判据是**码相等**，不是关键字：两项各自单独违反时的码已经在语料里量过
// （`ctperr` 那张表），拿回来的码等于谁，就是谁先。
// ⚠️ 两个都对不上时**必须说出来**而不是挑一个近的 —— 那种情形说明这一笔
// 违反的根本不是我以为的那两项（20260914 `bc` 那次正是）。
func priorityVerdict(got, session ctperr.Code, other ctperr.Code, hasOther bool,
	otherName string) (winner, why string) {

	switch {
	case got == session && hasOther && got == other:
		// ⚠️ 两项的码**撞在一起**时这一对分不开 —— 这不是「都赢了」，是没有判别力。
		return "", fmt.Sprintf("⚠️ **这一对分不开**：%s 与 %s 的码都是 %s", sessionItem, otherName, got)
	case got == session:
		return sessionItem, fmt.Sprintf("拿到 %s = %s 单独违反时的码", got, sessionItem)
	case hasOther && got == other:
		return otherName, fmt.Sprintf("拿到 %s = %s 单独违反时的码", got, otherName)
	case !hasOther:
		return "", fmt.Sprintf("⚠️ **判不了**：%s 单独违反时的码本库没有观测（ctperr 查不到），"+
			"拿到的 %s 归不到任何一项", otherName, got)
	default:
		return "", fmt.Sprintf("⚠️ **谁都没预言到**：拿到 %s，而 %s 是 %s、%s 是 %s —— "+
			"这一笔违反的多半不是我以为的那两项", got, sessionItem, session, otherName, other)
	}
}

// caseReason 把一条用例映射到 ctperr 的拒因。
//
// ⚠️ 与 ctperr 测试里的 reasonOf 是**两份**同形的映射，这是刻意的：
// `ctperr` 在根模块、`cmd/oracle` 是嵌套模块，根模块不能反过来依赖它。
// ⇒ 两份都只认 `Violates` 恰好一项 + `Case` 里的关键词，改一份要记得改另一份。
func caseReason(rc rejectCase) (ctperr.Reason, bool) {
	if len(rc.Violates) != 1 {
		return ctperr.ReasonUnknown, false
	}
	switch rc.Violates[0] {
	case "最小变动价位":
		return ctperr.ReasonPriceTick, true
	case "涨跌停":
		switch {
		case strings.Contains(rc.Name, "涨停"):
			return ctperr.ReasonAboveUpperLimit, true
		case strings.Contains(rc.Name, "跌停"):
			return ctperr.ReasonBelowLowerLimit, true
		}
	case "可平量":
		if rc.Off == def.THOST_FTDC_OF_CloseYesterday {
			return ctperr.ReasonCloseYesterdayExceeds, true
		}
	}
	return ctperr.ReasonUnknown, false
}

// codeOf 把一次回报折成 ctperr.Code；第二个返回值为 false 表示这一笔一个码都没有。
func codeOf(st ctp.OrderState) (ctperr.Code, bool) {
	if st.ErrorID != 0 {
		return ctperr.Code{Space: ctperr.SpaceCTP, Value: st.ErrorID}, true
	}
	if c, ok := msgCode(st.StatusMsg); ok {
		return ctperr.Code{Space: ctperr.SpaceStatusPrefix, Value: c}, true
	}
	return ctperr.Code{}, false
}

// runCTPPriority 量「交易时段 × 另一项」的拒绝优先级。
//
// ⚠️ 它与 `ctp-reject` 的前提**恰好相反**：那条要合约此刻报得进单，这条要报不进
// （盘中休息 10:15–10:30、11:30–13:30，或收盘之后）。
// ⇒ 对照组挂上了就**整轮不跑** —— 那时「同时违反两项」里少了一项，剩下的是 ctp-reject 已经量过的东西。
func runCTPPriority(args []string) error {
	fs := flag.NewFlagSet("ctp-priority", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 SHFE.rb2701（⚠️ 无默认值）")
	out := fs.String("out", "", "语料落盘目录（⚠️ 不给就不跑：判别性的读数不许只打 console）")
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
	logf("[pri] 行情 最新=%.2f 涨停=%.2f 跌停=%.2f",
		float64(md.LastPrice), float64(md.UpperLimitPrice), float64(md.LowerLimitPrice))

	// ⚠️ 对照组在这条命令里是**前提**而不是护栏：它必须**被拒**，
	// 那才证明「此刻这个合约报不进单」这一项确实被违反了。
	ctrl, ctrlErr := runControl(c, ex, inst, md, tk, *timeout, logf)
	obs := []rejectObservation{ctrl}
	session, hasSession := ctpCodeOfObservation(ctrl)
	if ctrlErr == nil {
		return fmt.Errorf("⚠️ **整轮不跑**：对照组那笔合法委托**挂上了** —— "+
			"现在 %s 报得进单，而本命令要的正是报不进的时段"+
			"（盘中休息 10:15–10:30 / 11:30–13:30，或收盘之后）。⇒ 这时候该跑的是 `ctp-reject`", *symbol)
	}
	if !hasSession {
		return fmt.Errorf("⚠️ **整轮不跑**：对照组被拒了但**一个码都没有** —— "+
			"没有它，后面每一笔拿回来的码都比不出先后（%v）", ctrlErr)
	}
	logf("[pri] 对照组被拒 ⇒ %s 这一项确实被违反，它单独的码是 %s", sessionItem, session)

	for i, rc := range rejectCases {
		other, hasOther := caseReason(rc)
		otherCode, hasOtherCode := ctperr.Lookup(types.Exchange(ex), other)
		px := rc.Price(md, tk.Value)
		logf("")
		logf("[pri] %d/%d %s × %s → dir=%q off=%q @%.2f",
			i+1, len(rejectCases), sessionItem, rc.Name, string(rc.Dir), string(rc.Off), px)
		st, insErr := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: rc.Dir, Offset: rc.Off, Volume: 1, LimitPrice: px}, *timeout)
		outcome := "rejected"
		switch {
		case insErr != nil:
			outcome = "error"
			logf("[pri]   ⇒ Insert 报错：%v", insErr)
		case st.VolumeTraded > 0:
			// ⚠️ 报不进单的时段里成交了 —— 那比任何一条优先级都重要，必须喊。
			outcome = "traded"
			logf("[pri]   ⚠️⚠️ **成交了 %d 手** —— 跑 `ctp-flatten -symbol %s`", st.VolumeTraded, *symbol)
		case st.Alive():
			outcome = "resting"
			logf("[pri]   ⇒ **挂上了** —— 与对照组矛盾，这一笔不作数")
			if cerr := c.Cancel(st.OrderRef, ctp.OrderReq{Exchange: ex, Instrument: inst,
				Direction: rc.Dir, Offset: rc.Off, Volume: 1, LimitPrice: px}); cerr != nil {
				logf("[pri]   ⚠️ 撤单失败：%v", cerr)
			}
		default:
			got, hasCode := codeOf(st)
			if !hasCode {
				logf("[pri]   ⇒ 被拒但一个码都没有 —— 这一对判不了")
				break
			}
			winner, why := priorityVerdict(got, session, otherCode, hasOther && hasOtherCode, rc.Name)
			if winner == "" {
				logf("[pri]   ⇒ %s", why)
			} else {
				logf("[pri]   ⇒ **柜台报的是「%s」**（%s）", winner, why)
			}
		}
		// ⚠️ violates 带上**两项**：语料的归类器见到两项就归 ReasonUnknown 并跳过 ——
		// 那是对的，一条同时违反两项的观测本来就不能拿去填「某一项的码」那张表。
		two := rc
		two.Name = sessionItem + " × " + rc.Name
		two.Violates = append([]string{sessionItem}, rc.Violates...)
		obs = append(obs, observe(c.TradingDay(), nowClock(), ex, inst, two, st, outcome, tk))
	}
	logf("")
	if _, werr := writeRejectCorpus(*out, obs, logf); werr != nil {
		return werr
	}
	return nil
}

// ctpCodeOfObservation 从一条语料里取回码 —— 对照组那一条唯一的用途。
func ctpCodeOfObservation(o rejectObservation) (ctperr.Code, bool) {
	switch {
	case o.ErrorID != nil:
		return ctperr.Code{Space: ctperr.SpaceCTP, Value: *o.ErrorID}, true
	case o.ExchangeCode != nil:
		return ctperr.Code{Space: ctperr.SpaceStatusPrefix, Value: *o.ExchangeCode}, true
	}
	return ctperr.Code{}, false
}
