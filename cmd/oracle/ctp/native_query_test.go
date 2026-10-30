package ctp

import (
	"errors"
	"testing"
	"time"
)

func TestNativeQuerySlots(t *testing.T) {
	for name, want := range map[string]uintptr{
		"ReqQryOrder": 0x140, "ReqQryTrade": 0x148, "ReqQryInvestorPosition": 0x150,
		"ReqQryTradingAccount": 0x158, "ReqQryInstrumentCommissionRate": 0x178,
		"ReqQryInstrument": 0x190, "ReqQryDepthMarketData": 0x198,
		"ReqQrySettlementInfo": 0x1a0, "ReqQryBrokerTradingParams": 0x2c0,
	} {
		if got, err := nativeQuerySlot(name); err != nil || got != want {
			t.Fatal("原生查询白名单偏移不符", name, got, want, err)
		}
	}
	for _, name := range []string{"", "ReqOrderInsert", "ReqOrderAction", "ReqUserLogin", "ReqSettlementInfoConfirm", "ReqQryUnknown"} {
		if _, err := nativeQuerySlot(name); err == nil {
			t.Fatal("原生只读查询白名单放行未知或交易方法", name)
		}
	}
}

func TestQueryAbortKeepsFailureAndBlocksLateRows(t *testing.T) {
	for _, complete := range []bool{false, true} {
		var b queryBatch[int]
		b.begin(1)
		value := 42
		b.receive(1, &value, nil, complete)
		b.abort(errQueryClosed)
		b.mu.Lock()
		left := len(b.rows)
		b.mu.Unlock()
		if left != 0 {
			t.Fatal("关闭时未清除查询半截数据")
		}
		select {
		case <-b.done:
		default:
			t.Fatal("关闭查询未关闭完成信号")
		}
		b.abort(errors.New("重复关闭"))
		b.receive(1, &value, nil, true)
		rows, err := b.wait(1, time.Second)
		if rows != nil {
			t.Fatal("关闭查询发布半截或迟到结果", rows, err)
		}
		if !errors.Is(err, errQueryClosed) {
			t.Fatal("关闭查询覆盖首错", err)
		}
	}
	var b queryBatch[int]
	b.begin(2)
	first := errors.New("首个查询失败")
	b.receive(2, nil, first, false)
	b.abort(errQueryClosed)
	if _, err := b.wait(2, time.Second); !errors.Is(err, first) {
		t.Fatal("关闭查询覆盖首错", err)
	}
}
