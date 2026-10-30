package ctp

import "fmt"

// nativeQuerySlot 只允许已核固定DLL的九种只读方法；握手与报撤单不在此入口。
func nativeQuerySlot(name string) (uintptr, error) {
	switch name {
	case "ReqQryOrder":
		return 0x140, nil
	case "ReqQryTrade":
		return 0x148, nil
	case "ReqQryInvestorPosition":
		return 0x150, nil
	case "ReqQryTradingAccount":
		return 0x158, nil
	case "ReqQryInstrumentCommissionRate":
		return 0x178, nil
	case "ReqQryInstrument":
		return 0x190, nil
	case "ReqQryDepthMarketData":
		return 0x198, nil
	case "ReqQrySettlementInfo":
		return 0x1a0, nil
	case "ReqQryBrokerTradingParams":
		return 0x2c0, nil
	default:
		return 0, fmt.Errorf("未登记的原生只读查询方法")
	}
}
