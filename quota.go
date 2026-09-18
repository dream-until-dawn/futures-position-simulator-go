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
// own 是被平那一侧的计数，opp 是反方向的计数。
//
// ⚠️ (f) 有三处没有实测（design.md 门面形状 §15 决策点 2），一律「报错不猜」—— 只在两种读法**真会分岔**时报，同值照收：
//
//	多手裸平、0 < 额度 < 平仓量   按 min 拆 / 全今 / 全昨三种读法给不同的数（实验全是一手一笔）
//	显式平今扣不扣额度             「扣」与「不扣」算出的平今档手数不同
//	额度分不分方向                 「本方向」与「两个方向合计」算出的平今档手数不同（当日开过反方向时才可能）
//
// 四种读法（分 / 不分方向 × 扣 / 不扣显式平今）逐一算；任何一种落在 (0, 平仓量) 里、或两种给出不同的手数，都报错。
func todayTierLots(inst types.InstrumentID, vol int, own, opp quotaCount) (int, error) {
	readings := []struct {
		name  string
		quota int
	}{
		{"本方向、显式平今扣额度", own.Opened - own.Charged},
		{"本方向、显式平今不扣额度", own.Opened - (own.Charged - own.Explicit)},
		{"两个方向合计、显式平今扣额度", own.Opened + opp.Opened - own.Charged - opp.Charged},
		{"两个方向合计、显式平今不扣额度", own.Opened + opp.Opened - (own.Charged - own.Explicit) - (opp.Charged - opp.Explicit)},
	}
	lots, first := 0, ""
	for i, r := range readings {
		q := max(r.quota, 0)
		if q > 0 && q < vol {
			return 0, fmt.Errorf("%s 裸平 %d 手而当日开仓额度 %d（按「%s」）介于 0 与平仓量之间 —— 按 min 拆 / 全今 / 全昨三种读法给不同的数，"+
				"§13 #23 只测过一手一笔，不猜。拆成一手一手报，或显式给平今 / 平昨", inst, vol, q, r.name)
		}
		t := 0
		if q >= vol {
			t = vol
		}
		if i == 0 {
			lots, first = t, r.name
			continue
		}
		if t != lots {
			return 0, fmt.Errorf("%s 裸平 %d 手：按「%s」平今档 %d 手，按「%s」%d 手 —— §13 #23 没测过这种形状（显式平今扣不扣额度 / 额度分不分方向），不猜",
				inst, vol, first, lots, r.name, t)
		}
	}
	return lots, nil
}

// quotaOf 取一个记账单位的计数（没有就是零）。
func (s *Simulator) quotaOf(inst types.InstrumentID, hedge types.HedgeFlag, side types.Direction) quotaCount {
	return s.quotas[quotaKey{inst, hedge, side}]
}

// undatedCap 给一笔裸平（平 held 那一侧）返回「算平今档手数」的函数，供 commission 在两档费率不同时才调用。
func (s *Simulator) undatedCap(inst types.InstrumentID, hedge types.HedgeFlag, held types.Direction, vol int) func() (int, error) {
	return func() (int, error) {
		return todayTierLots(inst, vol, s.quotaOf(inst, hedge, held), s.quotaOf(inst, hedge, opposite(held)))
	}
}
