package ctp

import (
	def "gitee.com/haifengat/goctp/ctpdefine"
	"testing"
)

// TestPositionRowsKeepAllDimensions 同合约多空、今昨、投保不同的回报不能互相覆盖。
func TestPositionRowsKeepAllDimensions(t *testing.T) {
	var rows []def.CThostFtdcInvestorPositionField
	for _, dir := range []def.TThostFtdcPosiDirectionType{def.THOST_FTDC_PD_Long, def.THOST_FTDC_PD_Short} {
		for _, date := range []def.TThostFtdcPositionDateType{def.THOST_FTDC_PSD_Today, def.THOST_FTDC_PSD_History} {
			for _, hedge := range []def.TThostFtdcHedgeFlagType{def.THOST_FTDC_HF_Speculation, def.THOST_FTDC_HF_Hedge} {
				r := def.CThostFtdcInvestorPositionField{PosiDirection: dir, PositionDate: date, HedgeFlag: hedge, Position: 1}
				copy(r.InstrumentID[:], "m2701")
				copy(r.ExchangeID[:], "DCE")
				rows = append(rows, r)
			}
		}
	}
	got := positionRowsByKey(rows)
	if len(got) != 8 {
		t.Fatalf("持仓维度被覆盖：%d", len(got))
	}
	for _, r := range got {
		if r.Position != 1 {
			t.Fatal("持仓字段被修改")
		}
	}
	rows[0].Position = 99
	for _, r := range got {
		if r.Position != 1 {
			t.Fatal("查询结果引用了调用方可变数组")
		}
	}
}
