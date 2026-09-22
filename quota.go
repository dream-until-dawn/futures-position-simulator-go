package futsim

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// quotaKey 是裸平「当日开仓额度」的记账单位：合约 × 投保标志 × 持仓方向（design.md 门面形状 §15，F11）。
type quotaKey struct {
	inst  types.InstrumentID
	hedge types.HedgeFlag
	side  types.Direction // 持仓方向：多头 = Buy
}

// quotaCount 是一个记账单位在当日的三个计数。交易日翻过去（Settle）全部清零。
type quotaCount struct {
	Opened   int // 当日开仓手数
	Charged  int // 当日已按平今档收过的手数（裸平按额度收的 + 显式平今）
	Explicit int // Charged 里有几手来自显式平今
}

// todayTierLots 给一笔裸平按 §13 #23 的 (f) 算平今档手数：min(平仓量, 当日开仓量 − 当日已按平今档收过的手数)。纯函数。
//
// own 是被平那一侧（本方向）的计数。F11 当初的三条外推，F13 放开两条（design.md 门面形状 §17，交易日 20260923 两所各一次事前登记）：
//
//	多手裸平、0 < 额度 < 平仓量   按额度拆：min 手平今档、其余平昨档（A：一条 2 手记录，m2703 收 0.3、MA703 收 8）
//	额度分不分方向                 分方向：只看本方向的计数，反方向开过不算（C：m2705 收 0.2、MA705 收 2）
//	显式平今扣不扣额度             ⚠️ 照旧「分岔报错」—— 这两所上显式平今被柜台改写成了通用平仓（§13 #25，观测、未收敛；silent-risks 103）
//
// 两种读法（本方向 × 扣 / 不扣显式平今）逐一算；给出不同手数就报错，同值照收。
func todayTierLots(inst types.InstrumentID, vol int, own quotaCount) (int, error) {
	take := func(quota int) int { return min(vol, max(quota, 0)) }
	deduct := take(own.Opened - own.Charged)
	noDeduct := take(own.Opened - (own.Charged - own.Explicit))
	if deduct != noDeduct {
		return 0, fmt.Errorf("%s 裸平 %d 手：按「显式平今扣额度」平今档 %d 手，按「不扣」%d 手 —— "+
			"显式平今扣不扣额度没测到（大商所 / 郑商所上显式平今被柜台改写成通用平仓，§13 #25 未收敛），不猜", inst, vol, deduct, noDeduct)
	}
	return deduct, nil
}

// quotaOf 取一个记账单位的计数（没有就是零）。
func (s *Simulator) quotaOf(inst types.InstrumentID, hedge types.HedgeFlag, side types.Direction) quotaCount {
	return s.quotas[quotaKey{inst, hedge, side}]
}

// undatedCap 给一笔裸平（平 held 那一侧）返回「算平今档手数」的函数，供 commission 在两档费率不同时才调用。
func (s *Simulator) undatedCap(inst types.InstrumentID, hedge types.HedgeFlag, held types.Direction, vol int) func() (int, error) {
	return func() (int, error) {
		return todayTierLots(inst, vol, s.quotaOf(inst, hedge, held))
	}
}
