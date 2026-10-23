package main

import (
	def "gitee.com/haifengat/goctp/ctpdefine"
	"testing"
)

// TestFlatPositionKeepsOpenedToday 当日开平完毕仍须保留累计开仓量，防错误满足实验前提。
func TestFlatPositionKeepsOpenedToday(t *testing.T) {
	for _, direction := range []def.TThostFtdcPosiDirectionType{def.THOST_FTDC_PD_Long, def.THOST_FTDC_PD_Short} {
		p := &def.CThostFtdcInvestorPositionField{PosiDirection: direction, OpenVolume: 2}
		copy(p.InstrumentID[:], "lh2701")
		rows := map[string]*def.CThostFtdcInvestorPositionField{"flat": p}
		got := sidesOf(rows, "lh2701", direction)
		if got.OpenedToday != 2 || got.Today != 0 || got.Yd != 0 {
			t.Fatalf("空仓丢失当日开仓累计：%+v", got)
		}
		if wrong := sidesOf(rows, "m2701", direction); wrong.OpenedToday != 0 {
			t.Fatal("其他合约串入累计")
		}
	}
}
