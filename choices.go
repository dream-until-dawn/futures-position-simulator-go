package futsim

import (
	"errors"
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/pnl"
)

// Choices 是门面记账时的全部口径。每一项零值都是「未实测」，New 报错。
//
// ⚠️ 门面**不替调用方选**：六项里有三项两个口子量到的值相反（手续费基准、保证金基准、浮盈算法）。
// 出处见 docs/design.md「门面的形状」§1 那张表。
type Choices struct {
	FeeBasis    fee.PriceBasis
	FeeRounding fee.Rounding
	MarginBasis margin.PriceBasis
	SideScope   margin.SideScope
	Mark        pnl.Mark
	Algorithm   account.Algorithm
}

// CTPChoices 返回 CTP / SimNow 上**实测过**的口径。
//
// ⚠️ FeeRounding 留零值（§13 #5 未收敛）—— New 会因此报错，调用方必须自己填那一格。
// 那一行赋值就是「我知道这一项没实测」的签字。
func CTPChoices() Choices {
	return Choices{
		FeeBasis:    fee.TradePrice,                   // 平昨 @3137 收 3.142（以 +0.005/手为前提，§13 #19）
		MarginBasis: margin.OpenTodayPreSettleHistory, // §13 #1
		SideScope:   margin.ByProduct,                 // §13 #3
		Mark:        pnl.MarkLast,                     // §13 #2
		Algorithm:   account.AlgorithmOnlyLost,        // §13 #17
	}
}

// KQChoices 返回快期模拟上**实测过**的口径。
//
// ⚠️ FeeRounding 留零值（§13 #5）；SideScope 留零值 —— 快期没实现大边（simnow_pending#6），测不了不等于测过。
func KQChoices() Choices {
	return Choices{
		FeeBasis:    fee.PreSettlement,    // kq_facts 4
		MarginBasis: margin.PreSettleAll,  // Rebuild 现用
		Mark:        pnl.MarkLast,         // Rebuild 现用
		Algorithm:   account.AlgorithmAll, // kq_facts 11
	}
}

// validate 报出**全部**未指定的项，不是第一个 —— 一次补齐，而不是改一格跑一次。
func (c Choices) validate() error {
	var errs []error
	if c.FeeBasis == fee.PriceBasisUnmeasured {
		errs = append(errs, fmt.Errorf("FeeBasis 未指定"))
	}
	if c.FeeRounding == fee.RoundingUnmeasured {
		errs = append(errs, fmt.Errorf("FeeRounding 未指定（§13 #5 未收敛，两个预设都不填它）"))
	}
	if c.MarginBasis == margin.PriceBasisUnmeasured {
		errs = append(errs, fmt.Errorf("MarginBasis 未指定"))
	}
	if c.SideScope == margin.SideScopeUnmeasured {
		errs = append(errs, fmt.Errorf("SideScope 未指定"))
	}
	if c.Mark == pnl.MarkUnmeasured {
		errs = append(errs, fmt.Errorf("Mark 未指定"))
	}
	if c.Algorithm == account.AlgorithmUnset {
		errs = append(errs, fmt.Errorf("Algorithm 未指定"))
	}
	return errors.Join(errs...)
}
