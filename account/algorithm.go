package account

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Algorithm 是盈亏算法：持仓盈亏的哪一侧计入可用资金。
//
// 取值照 CTP 的 `CThostFtdcBrokerTradingParamsField.Algorithm` 枚举列，
// 不照「哪些说得通」想 —— `margin.PriceBasis` 就是按后者想出候选集，
// 结果漏掉了柜台的取值（见那个类型的文档）。
//
// ⚠️ **两个口子实测的取值相反**，所以本包不给默认值：
//
//	快期模拟  '1' 全部计算   kq_facts 11：浮盈 6 份 + 浮亏 6 份，结存恒等式两侧都成立
//	SimNow    '2' 只计浮亏   §13 #17：浮盈 +150 时可用恰好少 150，浮亏 −40 时分毫不差
//
// ⚠️ 选错的方向是「**赢着的时候以为自己比真实账户有钱**」，而那正是会加仓的时候。
//
// '3' / '4' 定义了但 New 拒绝：它们的语义是从枚举名读的，没有一个账户配成过这两个值。
// 只定义两个会让「柜台还有两种配置」从类型上消失；定义了而放行，就是把枚举名当成了行为。
type Algorithm uint8

const (
	// AlgorithmUnset 是零值：New 报错。
	AlgorithmUnset Algorithm = iota
	// AlgorithmAll 对应 CTP '1'：浮盈浮亏都计入可用。✅ 快期模拟行为实测。
	AlgorithmAll
	// AlgorithmOnlyLost 对应 CTP '2'：只计浮亏，浮盈不计入可用。✅ SimNow 行为实测。
	AlgorithmOnlyLost
	// AlgorithmOnlyGain 对应 CTP '3'：只计浮盈。⚠️ 从枚举名推得，无观测，New 拒绝。
	AlgorithmOnlyGain
	// AlgorithmNone 对应 CTP '4'：都不计。⚠️ 从枚举名推得，无观测，New 拒绝。
	AlgorithmNone
)

func (g Algorithm) String() string {
	switch g {
	case AlgorithmUnset:
		return "未指定"
	case AlgorithmAll:
		return "全部计算('1')"
	case AlgorithmOnlyLost:
		return "只计浮亏('2')"
	case AlgorithmOnlyGain:
		return "只计浮盈('3')"
	case AlgorithmNone:
		return "都不计('4')"
	}
	return fmt.Sprintf("Algorithm(%d)", uint8(g))
}

// measured 报告这个取值有没有行为观测。
func (g Algorithm) measured() error {
	switch g {
	case AlgorithmAll, AlgorithmOnlyLost:
		return nil
	case AlgorithmUnset:
		return fmt.Errorf("盈亏算法未指定 —— ⚠️ 快期实测「全部计算」、SimNow 实测「只计浮亏」，" +
			"两个口子相反，没有能当默认的那一个")
	case AlgorithmOnlyGain, AlgorithmNone:
		return fmt.Errorf("盈亏算法 %s 没有行为观测（语义只从枚举名读出）—— "+
			"要用它先找一个这样配置的账户量一次", g)
	}
	return fmt.Errorf("认不得的盈亏算法 %s", g)
}

// excluded 返回持仓盈亏里**不计入可用**的那部分（从可用里减掉它）。
//
// ⚠️ 只写实测过的两支；另两支在 New 就被拒，走不到这里。
func (g Algorithm) excluded(positionProfit decimal.Decimal) decimal.Decimal {
	if g == AlgorithmOnlyLost {
		return decimal.Max(positionProfit, decimal.Zero)
	}
	return decimal.Zero
}
