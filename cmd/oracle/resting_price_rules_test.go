package main

import (
	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/shopspring/decimal"
	"testing"
	"time"
)

func restingQuoteFixture() (*def.CThostFtdcDepthMarketDataField, *def.CThostFtdcInstrumentField, time.Time) {
	now := time.Date(2026, 10, 28, 10, 55, 1, 0, probeShanghai)
	md := &def.CThostFtdcDepthMarketDataField{LastPrice: 3000, AskPrice1: 3001, BidPrice1: 3000, AskVolume1: 5, BidVolume1: 5, LowerLimitPrice: 2700, UpperLimitPrice: 3300}
	copy(md.InstrumentID[:], "rb2701")
	copy(md.TradingDay[:], "20261028")
	copy(md.UpdateTime[:], "10:55:00")
	spec := &def.CThostFtdcInstrumentField{PriceTick: 1, IsTrading: 1, VolumeMultiple: 10}
	copy(spec.InstrumentID[:], "rb2701")
	copy(spec.ExchangeID[:], "SHFE")
	return md, spec, now
}

// TestPassivePriceInputs 主动价、陈旧行情和错误日期均不能进入挂单路径。
func TestPassivePriceInputs(t *testing.T) {
	md, spec, now := restingQuoteFixture()
	got, err := passiveProbePrice(md, spec, "20261028", now)
	if err != nil || !got.Equal(decimal.NewFromInt(2997)) {
		t.Fatalf("被动价错误：%s %v", got, err)
	}
	for _, change := range []func(*def.CThostFtdcDepthMarketDataField){func(m *def.CThostFtdcDepthMarketDataField) { m.AskPrice1 = 2997 }, func(m *def.CThostFtdcDepthMarketDataField) { m.LowerLimitPrice = 2998 }, func(m *def.CThostFtdcDepthMarketDataField) { copy(m.UpdateTime[:], "10:54:00") }, func(m *def.CThostFtdcDepthMarketDataField) { copy(m.TradingDay[:], "20261022") }} {
		md, spec, now = restingQuoteFixture()
		change(md)
		if _, err = passiveProbePrice(md, spec, "20261028", now); err == nil {
			t.Fatal("无效行情放行")
		}
	}
}

// TestRestingEvidence 三种结论必须分开，错误引用和旧成交不能成为被动价证据。
func TestRestingEvidence(t *testing.T) {
	r := &def.CThostFtdcTradeField{Direction: def.THOST_FTDC_D_Buy, OffsetFlag: def.THOST_FTDC_OF_Open, HedgeFlag: def.THOST_FTDC_HF_Speculation, Volume: 1, Price: 2997}
	copy(r.TradeID[:], "t")
	copy(r.OrderRef[:], "own")
	copy(r.OrderSysID[:], "sys")
	copy(r.ExchangeID[:], "SHFE")
	copy(r.InstrumentID[:], "rb2701")
	copy(r.TradingDay[:], "20261028")
	copy(r.TradeTime[:], "10:56:00")
	out := ownedOrderOutcome{Ref: "own", SysID: "sys", QueueObserved: true, LocalQueueMillis: 2000}
	price := decimal.NewFromInt(2997)
	verdict, _, err := restingPriceVerdict([]*def.CThostFtdcTradeField{r}, out, price, "20261028")
	if err != nil || verdict != "supports-order-price" {
		t.Fatal("相等样本判错")
	}
	r.Price = 2996
	verdict, _, err = restingPriceVerdict([]*def.CThostFtdcTradeField{r}, out, price, "20261028")
	if err != nil || verdict != "counterexample" {
		t.Fatal("价格反例被忽略")
	}
	out.QueueObserved = false
	verdict, _, _ = restingPriceVerdict([]*def.CThostFtdcTradeField{r}, out, price, "20261028")
	if verdict != "no-verdict" {
		t.Fatal("即时成交被当成被动挂单证据")
	}
	out.QueueObserved = true
	out.LocalQueueMillis = 1999
	verdict, _, _ = restingPriceVerdict([]*def.CThostFtdcTradeField{r}, out, price, "20261028")
	if verdict != "no-verdict" {
		t.Fatal("短暂排队被当成充分观察")
	}
	out.LocalQueueMillis = 2000
	copy(r.OrderRef[:], "old")
	if _, _, err = restingPriceVerdict([]*def.CThostFtdcTradeField{r}, out, price, "20261028"); err == nil {
		t.Fatal("误认旧引用成交")
	}
}
