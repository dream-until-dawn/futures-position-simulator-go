// Package safety 是下单的安全阀，**与协议无关**。
//
// # 为什么它可以与协议无关，而客户端不行
//
// [docs/ctp-oracle.md] 第 4 节写明 `kq` 与 `ctp` 两个客户端**并列、不做统一抽象**：
// 硬抽一个 Client 接口会得到一个两边都不像的中间层，
// 而它会把「这两个口子本来就不一样」这个最要紧的事实藏起来。
//
// ⚠️ 安全阀是**例外**，而例外的理由要说清楚，否则它就是那条原则的第一个缺口：
//
//	客户端抽象的是**协议**   —— 字段名、回调模型、错误模型，两边一处都不同
//	安全阀判断的是**意图**   —— 哪个合约、哪个方向、开还是平、几手、什么价
//
// 「别超过一手」「这条腿不许平」这两句话里**没有一个字**与线格式有关。
// 于是这里定义自己的枚举，各协议**映射进来**——而那个映射点正好是
// 「两边不一样」被写下来的地方，不是被藏起来的地方。
//
// # ⚠️ 它守的东西是不可逆的
//
// 探针在真实柜台上操作。即便是模拟资金，一次写错参数也会把账户状态搞脏到
// 后续实验没法做（过夜种子那一类是**当天不可再生**的）。
package safety

import "fmt"

// Side 是**持仓**方向，不是委托方向。
//
// ⚠️ 两者相反：平掉一个 Short 持仓要发 Buy 委托。这一处写错，
// 安全阀会掉个个 —— 放过真正危险的那一笔，转去拦一笔无关的，
// 而**两种表现都不像 bug**。
type Side uint8

const (
	// SideUnknown 是零值：谁也不匹配。
	//
	// ⚠️ 零值不选 Long 也不选 Short：一个「默认拦多头」的零值，
	// 会让忘了填方向的声明**看起来在保护什么**。
	SideUnknown Side = iota
	Long
	Short
)

func (s Side) String() string {
	switch s {
	case Long:
		return "多头"
	case Short:
		return "空头"
	}
	return "未指定"
}

// Intent 是一笔委托里**安全阀需要知道的全部**。
//
// ⚠️ 它刻意**不是**任何一个协议的委托结构：没有交易所字段名、没有价格类型、
// 没有时间条件。多一个字段，就多一处「两个协议怎么映射」的疑问，
// 而安全阀不该有那种疑问。
type Intent struct {
	// Symbol 是 "SHFE.rb2701" 这样的全称。
	Symbol string
	// Closing 为真表示这一笔是**平仓**（含平今）。
	//
	// ⚠️ 判据是「**是不是已识别的平仓**」，不是「是不是开仓」。
	// 这个方向是被一次真实故障翻过来的：原先写 `Offset == Open` 才拦，
	// 而 Offset 的零值是空串，于是任何未设置或拼错的开平标志**都绕过了上限**。
	// 现在的失败方向朝着「多拦一次」——那会立刻被看见。
	Closing bool
	// ClosesSide 是这一笔**平的是哪个方向的持仓**；非平仓时无意义。
	//
	// ⚠️ 由调用方做方向取反，不在这里猜：取反规则是协议的事
	// （BUY 平空 / SELL 平多），而这里不认协议。
	ClosesSide Side
	// Volume 是手数。
	Volume int
	// LimitPrice 是限价。
	LimitPrice float64
	// Desc 是给错误信息用的人类可读描述，形如 "SHFE.rb2701 BUY/CLOSE 1手 @3300"。
	Desc string
}

// ProtectedLeg 是一条**今天不许平**的持仓腿。
//
// ⚠️ 它存在的理由是：一句写在文档里的「别平掉它」不是守卫。
// 20260909 发现 roadmap 写着「别在结算前把这些仓平掉」，而全仓唯一一处
// 机器可读的「账上该有什么」（seedPlan）里**一个字都没提到**那一手空今仓。
type ProtectedLeg struct {
	Symbol string
	// Side 是**持仓**方向。
	Side Side
	// TradingDay 限定这条保护只在哪个交易日生效。
	//
	// ⚠️ 不是可选的：一条没有到期日的保护会在种子早已用掉之后继续拦着
	// 正当的收尾平仓 —— 那与 MaxVolume 当初拦住收尾平仓是同一个故障。
	TradingDay string
	// Why 会原样出现在拦下时的错误里。
	Why string
}

// blocks 判一笔委托是不是在平这条腿。
func (l ProtectedLeg) blocks(in Intent, tradingDay string) bool {
	if l.Symbol != in.Symbol || !in.Closing {
		return false
	}
	// ⚠️ tradingDay 为空表示**还不知道今天是哪天**（截面还没回来）。
	// 那时候必须照拦：这一步的失败方向要朝着「多拦一次」。
	// 反过来写（不知道就放行）在真账户上是这样发生的 ——
	// 连上柜台、截面还没到、而收尾平仓已经跑了。
	if tradingDay != "" && l.TradingDay != tradingDay {
		return false
	}
	return l.Side == in.ClosesSide
}

// Valve 是安全阀本身。
type Valve struct {
	// AllowOrder 对应 .env 的 PROBE_ALLOW_ORDER。
	AllowOrder bool
	// MaxVolume 对应 .env 的 PROBE_MAX_VOLUME，**开仓**单笔手数上限。
	MaxVolume int
	// Protected 是今天不许平的持仓腿。
	Protected []ProtectedLeg
	// TradingDay 是柜台报的当前交易日；空串表示还不知道，见 blocks。
	TradingDay string
}

// Check 校验一笔委托是否被放行。
//
// ⚠️ MaxVolume 放过的是**已识别的平仓**，不是「非开仓」。
//
// 上限原先对所有委托一视同仁，结果是：一次实验意外建到 2 手，收尾平仓要发
// 2 手的单，被 MaxVolume=1 挡下 —— **账上留着仓，平不掉**。
// 一个用来防止扩大风险的守卫，反过来阻止了缩小风险。
//
// ⚠️ 而修那次故障时写成了「是开仓才拦」，那是个洞：Offset 的零值是空串，
// 任何未设置或拼错的开平标志都绕过了上限。
// **人在修「阀门太紧」的故障时，会本能地往松了调，而松的方向恰好是危险的方向。**
func (v Valve) Check(in Intent) error {
	if !v.AllowOrder {
		return fmt.Errorf("下单被安全阀拦下：PROBE_ALLOW_ORDER 未开启（%s）", in.Desc)
	}
	// ⚠️ 受保护的腿排在手数与限价之前：它拦的是**不可再生**的东西，
	// 而后面几条拦的是重发一笔就能修好的参数错。
	for _, l := range v.Protected {
		if l.blocks(in, v.TradingDay) {
			return fmt.Errorf("下单被安全阀拦下：这一笔会平掉**受保护的持仓腿** %s %s —— %s"+
				"（保护在交易日 %q 生效，当前 %q）。"+
				"⚠️ 确实要平就把它从清单里划掉并说明理由，不要绕过安全阀（%s）",
				l.Symbol, l.Side, l.Why, l.TradingDay, v.TradingDay, in.Desc)
		}
	}
	if in.Volume <= 0 {
		return fmt.Errorf("手数必须为正，得到 %d", in.Volume)
	}
	if v.MaxVolume > 0 && !in.Closing && in.Volume > v.MaxVolume {
		return fmt.Errorf("手数 %d 超过上限 PROBE_MAX_VOLUME=%d（这一笔不是已识别的平仓，按开仓对待）（%s）",
			in.Volume, v.MaxVolume, in.Desc)
	}
	if in.LimitPrice <= 0 {
		return fmt.Errorf("限价必须为正，得到 %v（%s）", in.LimitPrice, in.Desc)
	}
	return nil
}
