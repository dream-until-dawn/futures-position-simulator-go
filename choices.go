package futsim

import (
	"errors"
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/pnl"
)

// Choices 是门面记账时的全部口径。前七项零值都是「未实测」，New 报错；第八项零值合法（见字段注释）。
//
// ⚠️ 门面**不替调用方选**：八项里有四项两个口子量到的值相反（手续费基准、保证金基准、浮盈算法、冻结保证金基准）。
// 出处见 docs/design.md「门面的形状」§1 那张表；FreezeMargin 见 §7。
type Choices struct {
	FeeBasis    fee.PriceBasis
	FeeRounding fee.Rounding
	MarginBasis margin.PriceBasis
	SideScope   margin.SideScope
	Mark        pnl.Mark
	Algorithm   account.Algorithm
	// FreezeMargin 是开仓单冻结保证金按哪个价算（报单路径的资金校验用它算要占用多少）。
	FreezeMargin order.FreezeMarginBasis
	// UndatedCloseOnUseHistory 是 UseHistory 合约（上期所）上裸 CLOSE 的语义。
	//
	// ⚠️ 与上面七项不同：**零值合法** —— 零值时 UseHistory 上的裸 CLOSE 报错（显式声明平今平昨本来就是要求），
	// 不是开户就报错；否则 CTP 预设开不了户。design.md「门面的形状」§10 决策点 1。
	UndatedCloseOnUseHistory UndatedClose
}

// UndatedClose 是 UseHistory 合约上裸 CLOSE 的语义。
type UndatedClose uint8

const (
	// UndatedCloseUnmeasured 是零值：UseHistory 上的裸 CLOSE 报错（simnow_pending#1 未裁决）。
	UndatedCloseUnmeasured UndatedClose = iota
	// UndatedCloseAsYesterday 按平昨记账：消耗昨仓、冻结昨仓、手续费走平昨档。
	//
	// ⚠️ 快期模拟的实测语义（kq_facts 32：今 1 昨 3 时 CLOSE 冻昨；只有今仓时 CLOSE 被拒「平昨手数超过昨仓持仓量」）。
	// 记账路径（ApplyTrade / Fill / FreezeOf / PlaceAccepted）与八项校验（Submit / Place）都跟它：按平昨校验、按平昨记账。
	// 改写得来的拒单不给 CTP 拒因码（语料是 CTP 上显式平昨的拒单，不外推）。评审 20260915 打回「八项不跟」那一版后改。
	UndatedCloseAsYesterday
)

// CTPChoices 返回 CTP / SimNow 上**实测过**的口径。
//
// UndatedCloseOnUseHistory 留零值（simnow_pending#1 未裁决）—— 它的零值合法，上期所裸 CLOSE 照旧报错。
// ⚠️ FeeRounding 留零值（§13 #5 未收敛）—— New 会因此报错，调用方必须自己填那一格。
// 那一行赋值就是「我知道这一项没实测」的签字。
func CTPChoices() Choices {
	return Choices{
		FeeBasis:     fee.TradePrice,                   // 平昨 @3137 收 3.142（以 +0.005/手为前提，§13 #19）
		MarginBasis:  margin.OpenTodayPreSettleHistory, // §13 #1
		SideScope:    margin.ByProduct,                 // §13 #3
		Mark:         pnl.MarkLast,                     // §13 #2
		Algorithm:    account.AlgorithmOnlyLost,        // §13 #17
		FreezeMargin: order.FreezeAtOrderPrice,         // ctp-frozen-20260910：委托额 30050、冻结 4808 = 3005 × 10 × 0.16
	}
}

// KQChoices 返回快期模拟上**实测过**的口径。
//
// ⚠️ FeeRounding 留零值（§13 #5）；SideScope 留零值 —— 快期没实现大边（simnow_pending#6），测不了不等于测过。
func KQChoices() Choices {
	return Choices{
		FeeBasis:     fee.PreSettlement,           // kq_facts 4
		MarginBasis:  margin.PreSettleAll,         // 快期对拍有判别力：换成今仓按开仓价，status-20260908-7 的 margin 差 22.4（破坏 518）
		Mark:         pnl.MarkLast,                // 快期对拍有判别力：换成昨结算价，position_profit 700 → −320（破坏 519）
		Algorithm:    account.AlgorithmAll,        // kq_facts 11
		FreezeMargin: order.FreezeAtPreSettlement, // kq_facts 46

		UndatedCloseOnUseHistory: UndatedCloseAsYesterday, // kq_facts 32
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
	if c.FreezeMargin == order.FreezeMarginUnmeasured {
		errs = append(errs, fmt.Errorf("FreezeMargin 未指定"))
	}
	if c.UndatedCloseOnUseHistory > UndatedCloseAsYesterday {
		errs = append(errs, fmt.Errorf("UndatedCloseOnUseHistory %d 认不得", c.UndatedCloseOnUseHistory))
	}
	return errors.Join(errs...)
}
