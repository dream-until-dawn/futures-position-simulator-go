package ctp

import def "gitee.com/haifengat/goctp/ctpdefine"

// positionRowsByKey 保留同合约的方向、今昨及投保三个维度，防回报互相覆盖。
func positionRowsByKey(rows []def.CThostFtdcInvestorPositionField) map[string]*def.CThostFtdcInvestorPositionField {
	out := make(map[string]*def.CThostFtdcInvestorPositionField, len(rows))
	for _, cp := range rows {
		if text(cp.InstrumentID[:]) == "" {
			continue
		}
		key := text(cp.ExchangeID[:]) + "." + text(cp.InstrumentID[:])
		out[key+"/"+string(rune(cp.PosiDirection))+"/"+string(rune(cp.PositionDate))+"/"+string(rune(cp.HedgeFlag))] = &cp
	}
	return out
}
