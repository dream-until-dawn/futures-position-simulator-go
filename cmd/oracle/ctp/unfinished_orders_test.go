package ctp

import (
	def "gitee.com/haifengat/goctp/ctpdefine"
	"testing"
)

// TestUnfinishedOrdersKeepUnknown 正向剔除已知终态，反向保留过渡态和未知的新值。
func TestUnfinishedOrdersKeepUnknown(t *testing.T) {
	rows := []def.CThostFtdcOrderField{{OrderStatus: def.THOST_FTDC_OST_AllTraded}, {OrderStatus: def.THOST_FTDC_OST_Canceled}, {OrderStatus: def.THOST_FTDC_OST_PartTradedNotQueueing}, {OrderStatus: def.THOST_FTDC_OST_NoTradeNotQueueing}, {OrderStatus: def.THOST_FTDC_OST_NoTradeQueueing}, {OrderStatus: def.THOST_FTDC_OST_PartTradedQueueing}, {OrderStatus: def.THOST_FTDC_OST_Unknown}, {OrderStatus: 'z'}}
	got := unfinishedOrderRows(rows)
	if len(got) != 4 {
		t.Fatalf("未终结委托被漏掉或终态被保留：%d", len(got))
	}
	for i := range got {
		if got[i].OrderStatus != rows[i+4].OrderStatus {
			t.Fatal("委托状态被修改")
		}
	}
}
