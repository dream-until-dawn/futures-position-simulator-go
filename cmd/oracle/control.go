package main

import (
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// controlCase 是拒单探针的**对照组**：一笔**完全合法**的委托 —— 价内、整倍数、账上够。
//
// ⚠️ 它要回答的是「这个合约此刻**根本能不能报单**」，而这不是多余的小心：
// 同一类错误已经发生过两次，两次都表现为**照样跑完四条、照样拿到四个码**——
//
//	20260914  bc 的 tick 是 10 而命令里填了 1 ⇒ 「低于跌停」那笔同时违反步长，拿到的是步长的码
//	20260916  CZCE.MA701 / GFEX.si2701 此刻「当前状态禁止报单」⇒ 四条全拿到 26，
//	          而 26 与涨跌停、与最小变动价位都毫无关系
//
// 第二种尤其骗人：四个码都拿到了、一条都没成交、命令打印「跑完 4 条」，
// 而语料里会多出三条**把状态码归给价格类**的机器可读记录。
//
// ⇒ 对照组挂得上 ⇒ 后面那四条的拒因才归得了因；挂不上 ⇒ **整轮不跑**。
var controlCase = rejectCase{
	Name: "对照组：完全合法的一笔（价内、整倍数）",
	Dir:  def.THOST_FTDC_D_Buy, Off: def.THOST_FTDC_OF_Open,
	Price: func(md *def.CThostFtdcDepthMarketDataField, _ float64) float64 {
		// ⚠️ 跌停价：它必定是整倍数、必定在价内，而买单挂在那里**通常成不了交**。
		// 「通常」是实话 —— 跌停封板时买一就在跌停价上，那时会成交。
		// ⇒ 成交那一支单独处理并报错，不当成「对照组通过」。
		return float64(md.LowerLimitPrice)
	},
	Violates: nil,
	Why:      "它什么都不违反 ⇒ 被拒只可能是合约状态、权限、资金这类与本轮无关的原因",
}

// controlVerdict 按对照组那一笔的结局判**这一轮能不能继续**。纯函数：离线可测。
//
// ⚠️ 四种结局的处置完全不同，而它们在「命令没崩」这个层面上长得一样：
//
//	挂上了    对照组通过 —— 后面四条的码归得了因（继续，但要**撤掉**这笔单）
//	被拒了    ⚠️ **整轮不跑**：一笔合法的单都报不进去，后面四条拿到的码与它们的名字无关
//	成交了    ⚠️ **整轮不跑**，且账上有敞口 —— 必须喊出来
//	报错了    ⚠️ **整轮不跑**：连结局都不知道，比被拒更不能往下走
func controlVerdict(symbol string, st ctp.OrderState, insertErr error) (proceed bool, cancel bool, err error) {
	switch {
	case insertErr != nil:
		return false, false, fmt.Errorf("⚠️ **整轮不跑**：对照组那笔合法委托发不出去（%v）—— "+
			"连它的结局都不知道，后面四条拿到什么码都归不了因", insertErr)
	case st.VolumeTraded > 0:
		return false, false, fmt.Errorf("⚠️⚠️ **整轮不跑，且账上有敞口**：对照组那笔挂在跌停价的买单**成交了 %d 手** —— "+
			"多半是跌停封板（买一就在跌停价上）。立刻跑 `ctp-flatten -symbol %s`", st.VolumeTraded, symbol)
	case st.Alive():
		return true, true, nil
	default:
		return false, false, fmt.Errorf("⚠️ **整轮不跑**：对照组那笔**完全合法**的委托被拒了"+
			"（status=%q ErrorID=%d 前缀码 %s）—— 这个合约此刻根本报不进单，"+
			"后面四条拿到的码与「涨跌停」「最小变动价位」都无关，"+
			"而它们会以机器可读的形式进语料。⇒ 换合约、换时段，或者如实记下「这个交易所此刻拍不了」",
			string(st.Status), st.ErrorID, codeText(st.StatusMsg))
	}
}

// codeText 把前缀码渲染成可读的一小段；没有前缀码时说「无」。
//
// ⚠️ **只取码，不取原话**：StatusMsg 是柜台自由文本，按纪律不进库；
// 这里是给人看的错误消息，同样只放码 —— 免得有人把这条消息里的原话抄进语料。
func codeText(statusMsg string) string {
	if code, ok := msgCode(statusMsg); ok {
		return fmt.Sprintf("%d", code)
	}
	return "无"
}

// runControl 发出对照组那一笔并判定；挂上了就撤掉。
func runControl(c *ctp.Client, ex, inst string, md *def.CThostFtdcDepthMarketDataField,
	tk tickUsed, timeout time.Duration, logf func(string, ...any)) (rejectObservation, error) {

	px := controlCase.Price(md, tk.Value)
	logf("[rej] 对照组 → dir=%q off=%q @%.2f（%s）",
		string(controlCase.Dir), string(controlCase.Off), px, controlCase.Why)
	req := ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: controlCase.Dir, Offset: controlCase.Off, Volume: 1, LimitPrice: px}
	st, insErr := c.Insert(req, timeout)
	proceed, cancel, err := controlVerdict(ex+"."+inst, st, insErr)
	outcome := "rejected"
	switch {
	case insErr != nil:
		outcome = "error"
	case st.VolumeTraded > 0:
		outcome = "traded"
	case st.Alive():
		outcome = "resting"
	}
	// ⚠️ 对照组**自己也是一条观测**：它被拒时拿到的那个码，正是「这个交易所此刻禁止报单」
	// 这件事唯一的机器可读证据。只写在提交正文里，下一个人还会再撞一次。
	obs := observe(c.TradingDay(), nowClock(), ex, inst, controlCase, st, outcome, tk)
	if cancel {
		logf("[rej] 对照组挂上了 —— 撤掉它，后面四条的码归得了因")
		if cerr := c.Cancel(st.OrderRef, req); cerr != nil {
			logf("[rej] ⚠️ 对照组撤单失败：%v —— 簿上还留着一笔，**手工撤**", cerr)
		}
	}
	if !proceed {
		return obs, err
	}
	return obs, nil
}
