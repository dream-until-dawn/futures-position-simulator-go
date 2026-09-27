package futsim

import (
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// rewriteCloseToday 将已观测的委托侧平今改写为通用平仓（规则 #25，design.md 门面形状 §18）。
// 只用于委托入口，不用于 ApplyTrade：已经发生的成交必须按回报记账。
// 名单与手续费名单分开维护；两条规则的证据来源不同，不能一起外推。
func rewriteCloseToday(inst refdata.Instrument, req order.Request) (order.Request, bool) {
	if (req.Instrument.Exchange == types.DCE || req.Instrument.Exchange == types.CZCE) &&
		inst.PositionDateType == refdata.NoUseHistory && req.Hedge == types.Speculation && req.Offset == types.CloseToday {
		req.Offset = types.Close
		return req, true
	}
	return req, false
}
