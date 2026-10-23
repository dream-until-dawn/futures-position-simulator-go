package main

import (
	def "gitee.com/haifengat/goctp/ctpdefine"
	"testing"
)

// TestUniqueRoundingTrade 故意把旧两手成交放到列表末尾，防止误取整表最后一条。
func TestUniqueRoundingTrade(t *testing.T) {
	makeTrade := func(id string) *def.CThostFtdcTradeField {
		r := &def.CThostFtdcTradeField{Direction: def.THOST_FTDC_D_Sell, OffsetFlag: def.THOST_FTDC_OF_Close, Volume: 2, Price: 11780}
		copy(r.TradeID[:], id)
		copy(r.ExchangeID[:], "DCE")
		copy(r.InstrumentID[:], "lh2701")
		return r
	}
	old := makeTrade("old")
	fresh := makeTrade("fresh")
	fresh.Price = 11885
	got, err := uniqueRoundingTrade([]*def.CThostFtdcTradeField{old}, []*def.CThostFtdcTradeField{fresh, old}, "lh2701")
	if err != nil || got != fresh {
		t.Fatalf("误取旧成交：%v", err)
	}
	split := makeTrade("split")
	split.Volume = 1
	missing := makeTrade("")
	for _, rows := range [][]*def.CThostFtdcTradeField{{old}, {fresh, fresh, old}, {split, old}, {fresh, split, old}, {missing, old}, {nil, old}, {fresh}} {
		if _, err := uniqueRoundingTrade([]*def.CThostFtdcTradeField{old}, rows, "lh2701"); err == nil {
			t.Fatal("接受不唯一或不完整成交")
		}
	}
}
