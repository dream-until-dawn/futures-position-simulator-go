package position

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// Restore 从存档的明细还原一个持仓。long / short 是两侧的明细，按原顺序。
//
// ⚠️ 不用 Side.Append 拼：它对手数 ≤ 0 的片**静默丢弃**、不查价格 —— 存档是可以手改的文件。逐片检查：
//
//	手数 > 0、开仓价 > 0、基线 > 0
//	昨仓片（Settled）的开仓交易日早于持仓交易日；今仓片等于它
//	⚠️ **昨仓片必须全部排在今仓片之前**
//
// 最后一条是「先平昨 ≡ 先开先平」的前提（CloseOrder 的文档：明细只经 Open 追加、Settle 一次标全部）。
// 那个前提在类型上不保证，而恢复是它唯一可能被打破的入口 —— 放过一个乱序的存档，
// 两种消耗顺序就会在恢复之后悄悄分岔。
func Restore(inst types.InstrumentID, hedge types.HedgeFlag, day types.TradingDay,
	dateType refdata.PositionDateType, long, short []Lot) (*Position, error) {

	p, err := New(inst, hedge, day, dateType)
	if err != nil {
		return nil, fmt.Errorf("恢复持仓 %s：%w", inst, err)
	}
	for _, side := range []struct {
		name string
		lots []Lot
		into *Side
	}{{"多头", long, &p.long}, {"空头", short, &p.short}} {
		seenToday := false
		for i, l := range side.lots {
			where := fmt.Sprintf("恢复持仓 %s %s 第 %d 片", inst, side.name, i+1)
			switch {
			case l.Volume <= 0:
				return nil, fmt.Errorf("%s：手数 %d 不为正", where, l.Volume)
			case !l.OpenPrice.IsPositive() || !l.Basis.IsPositive():
				return nil, fmt.Errorf("%s：开仓价 %s / 基线 %s 不为正", where, l.OpenPrice, l.Basis)
			case l.Settled && !day.After(l.OpenDay):
				return nil, fmt.Errorf("%s：标成昨仓，而开仓交易日 %d 不早于持仓交易日 %d", where, l.OpenDay, day)
			case !l.Settled && l.OpenDay != day:
				return nil, fmt.Errorf("%s：标成今仓，而开仓交易日 %d 不是持仓交易日 %d", where, l.OpenDay, day)
			case l.Settled && seenToday:
				return nil, fmt.Errorf("%s：昨仓片排在今仓片之后 —— 「先平昨 ≡ 先开先平」的前提被打破", where)
			}
			if !l.Settled {
				seenToday = true
			}
			side.into.lots = append(side.into.lots, l)
		}
	}
	return p, nil
}
