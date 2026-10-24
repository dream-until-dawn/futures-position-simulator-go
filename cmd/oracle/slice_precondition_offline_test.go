package main

import (
	def "gitee.com/haifengat/goctp/ctpdefine"
	"testing"
)

// TestOfflineSlicePrecondition 直接验证脏账户会被阻断，不把AST位置当作行为证据。
func TestOfflineSlicePrecondition(t *testing.T) {
	for _, tc := range []struct {
		name, inst string
		dir, date  byte
		volume     int
		rejected   bool
	}{
		{"空仓", "rb2701", def.THOST_FTDC_PD_Long, def.THOST_FTDC_PSD_Today, 0, false},
		{"已有多头今仓", "rb2701", def.THOST_FTDC_PD_Long, def.THOST_FTDC_PSD_Today, 1, true},
		{"别的合约", "m2701", def.THOST_FTDC_PD_Long, def.THOST_FTDC_PSD_Today, 1, false},
		{"仅昨仓", "rb2701", def.THOST_FTDC_PD_Long, def.THOST_FTDC_PSD_History, 1, false},
		{"空头", "rb2701", def.THOST_FTDC_PD_Short, def.THOST_FTDC_PSD_Today, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &def.CThostFtdcInvestorPositionField{PosiDirection: def.TThostFtdcPosiDirectionType(tc.dir), PositionDate: def.TThostFtdcPositionDateType(tc.date)}
			p.Position = def.TThostFtdcVolumeType(tc.volume)
			copy(p.InstrumentID[:], tc.inst)
			err := checkSlicePrecondition(map[string]*def.CThostFtdcInvestorPositionField{"p": p}, "rb2701", "SHFE.rb2701")
			if (err != nil) != tc.rejected {
				t.Fatalf("脏账户前提未阻断或误拦截：%v", err)
			}
		})
	}
}
