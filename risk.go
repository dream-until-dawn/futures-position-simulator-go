package futsim

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/risk"
)

// AssessRisk 按显式策略只读评估当前账户，不修改存档、委托或持仓，不执行强平。
func (s *Simulator) AssessRisk(policy risk.Policy) (risk.Report, error) {
	if s == nil || s.acc == nil {
		return risk.Report{}, fmt.Errorf("模拟器未初始化，不能评估风控")
	}
	if s.broken != nil {
		return risk.Report{}, fmt.Errorf("模拟器已失效，不能评估风控：%w", s.broken)
	}
	return risk.Assess(s.acc.State(), policy)
}
