package fee

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/shopspring/decimal"
)

// Rounding 是手续费的取整口径 —— internal/decimalx.Rounding 的导出别名。
//
// ⚠️ 存在的理由：Compute 的签名里有这个类型，而 decimalx 是 internal 包，
// 模块外的调用方**点不出它的常量**。别名让门面（futsim.Choices）能被外部填上。
type Rounding = decimalx.Rounding

// 取整口径的取值。⚠️ 判别实验 5 未收敛（§13 #5），零值报错。
const (
	RoundingUnmeasured = decimalx.RoundingUnmeasured
	HalfUpToCent       = decimalx.HalfUpToCent
	TruncateToCent     = decimalx.TruncateToCent
	NoRounding         = decimalx.NoRounding
)

// PriceBasis 是按额手续费按哪个价算。
//
// ⚠️ **两个口子实测相反**，零值报错：
//
//	CTP / SimNow  成交价     平昨 @3137 收 3.142，昨结算 3147 ⇒ 3.152，否（以行为费率 +0.005/手为前提，§13 #19）
//	快期模拟      昨结算价   kq_facts 4：cu2701 八个成交价、一个费额
type PriceBasis uint8

const (
	// PriceBasisUnmeasured 是零值：使用即报错。
	PriceBasisUnmeasured PriceBasis = iota
	// TradePrice 按成交价。✅ SimNow 实测。
	TradePrice
	// PreSettlement 按昨结算价。✅ 快期模拟实测。
	PreSettlement
)

func (b PriceBasis) String() string {
	switch b {
	case PriceBasisUnmeasured:
		return "未实测"
	case TradePrice:
		return "成交价"
	case PreSettlement:
		return "昨结算价"
	}
	return fmt.Sprintf("PriceBasis(%d)", uint8(b))
}

// BasisPrice 按 basis 从成交价与昨结算价里挑一个。
//
// ⚠️ 昨结算价缺失（has 为假）时，**即使 basis 是成交价也不报错** —— 用不上它；
// 反过来 basis 要昨结算价而没有，报错，不拿成交价顶。
func BasisPrice(b PriceBasis, trade decimal.Decimal, pre decimal.Decimal, hasPre bool) (decimal.Decimal, error) {
	switch b {
	case TradePrice:
		return trade, nil
	case PreSettlement:
		if !hasPre {
			return decimal.Zero, fmt.Errorf("手续费按昨结算价算，而没有昨结算价 —— 不拿成交价顶")
		}
		return pre, nil
	case PriceBasisUnmeasured:
		return decimal.Zero, fmt.Errorf("手续费的计价基准未指定 —— ⚠️ SimNow 实测成交价、快期实测昨结算价，两个口子相反")
	}
	return decimal.Zero, fmt.Errorf("认不得的手续费计价基准 %s", b)
}
