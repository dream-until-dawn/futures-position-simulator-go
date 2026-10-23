//go:build windows

package ctp

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unsafe"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// replyReadQuery 模拟 DLL 回调的数据边界，不替代实际柜台兼容性验证。
func replyReadQuery(c *Client, name string, id int, mode string, err error, last bool) {
	inst, exchange := "rb2701", "SHFE"
	if mode == "empty-exchange" {
		exchange = ""
	}
	if mode == "wrong-exchange" {
		exchange = "DCE"
	}
	if mode == "wrong-symbol" {
		inst = "rb2701C3000"
	}
	switch name {
	case "ReqQryTradingAccount":
		row := &def.CThostFtdcTradingAccountField{Available: 12345}
		if mode == "empty" {
			row = nil
		}
		c.accountRows.receive(id, row, err, last)
	case "ReqQryBrokerTradingParams":
		row := &def.CThostFtdcBrokerTradingParamsField{}
		copy(row.CurrencyID[:], "CNY")
		if mode == "empty" {
			row = nil
		}
		c.brokerRows.receive(id, row, err, last)
	case "ReqQryInstrumentCommissionRate":
		row := &def.CThostFtdcInstrumentCommissionRateField{OpenRatioByMoney: 0.0001}
		copy(row.InstrumentID[:], inst)
		copy(row.ExchangeID[:], exchange)
		if mode == "empty" {
			row = nil
		}
		c.commissionRows.receive(id, row, err, last)
	case "ReqQryInstrument":
		row := &def.CThostFtdcInstrumentField{PriceTick: 1}
		copy(row.InstrumentID[:], inst)
		copy(row.ExchangeID[:], exchange)
		if mode == "empty" {
			row = nil
		}
		c.instrumentRows.receive(id, row, err, last)
	case "ReqQryDepthMarketData":
		row := &def.CThostFtdcDepthMarketDataField{LastPrice: 3000}
		copy(row.InstrumentID[:], inst)
		copy(row.ExchangeID[:], exchange)
		if mode == "empty" {
			row = nil
		}
		c.marketRows.receive(id, row, err, last)
	}
}

// TestSingleQueriesUseIsolatedBatches 经实际公开查询函数验证五条路径，避免只测收集器而漏接线。
func TestSingleQueriesUseIsolatedBatches(t *testing.T) {
	type queryCase struct {
		name   string
		symbol bool
		call   func(*Client, time.Duration) (bool, error)
	}
	cases := []queryCase{
		{"ReqQryTradingAccount", false, func(c *Client, d time.Duration) (bool, error) {
			r, err := c.Account(d)
			return r != nil && r.Available == 12345, err
		}},
		{"ReqQryBrokerTradingParams", false, func(c *Client, d time.Duration) (bool, error) {
			r, err := c.BrokerParams(d)
			return r != nil && text(r.CurrencyID[:]) == "CNY", err
		}},
		{"ReqQryInstrumentCommissionRate", true, func(c *Client, d time.Duration) (bool, error) {
			r, err := c.CommissionRate("SHFE.rb2701", d)
			return r != nil && text(r.InstrumentID[:]) == "rb2701", err
		}},
		{"ReqQryInstrument", true, func(c *Client, d time.Duration) (bool, error) {
			r, err := c.Instrument("SHFE.rb2701", d)
			return r != nil && text(r.InstrumentID[:]) == "rb2701", err
		}},
		{"ReqQryDepthMarketData", true, func(c *Client, d time.Duration) (bool, error) {
			r, err := c.MarketData("SHFE.rb2701", d)
			return r != nil && text(r.InstrumentID[:]) == "rb2701", err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []string{"success", "empty", "duplicate", "error", "immediate", "timeout", "wrong-symbol", "empty-exchange", "wrong-exchange"} {
				if (mode == "wrong-symbol" || mode == "empty-exchange" || mode == "wrong-exchange") && !tc.symbol {
					continue
				}
				t.Run(mode, func(t *testing.T) {
					c := New(Credentials{}, nil)
					c.querySend = func(name string, _ unsafe.Pointer, id int) int32 {
						if name != tc.name {
							t.Fatalf("查询调用错接：%s != %s", name, tc.name)
						}
						switch mode {
						case "immediate":
							return -2
						case "timeout":
							return 0
						case "error":
							replyReadQuery(c, name, id, "success", nil, false)
							replyReadQuery(c, name, id, "empty", errors.New("fixture failure"), true)
						case "duplicate":
							replyReadQuery(c, name, id, "success", nil, false)
							replyReadQuery(c, name, id, "success", nil, true)
						default:
							// 旧编号错误和数据先到，也不能影响本次结果。
							replyReadQuery(c, name, id-1, "success", errors.New("old error"), true)
							replyReadQuery(c, name, id, mode, nil, true)
						}
						return 0
					}
					valid, err := tc.call(c, time.Millisecond)
					if mode == "success" || (mode == "empty-exchange" && tc.name == "ReqQryInstrumentCommissionRate") {
						if err != nil || !valid {
							t.Fatalf("正常查询失败：%v", err)
						}
					} else if err == nil {
						t.Fatalf("非法查询结果仍成功：%s", mode)
					} else if mode == "error" && !strings.Contains(err.Error(), "fixture failure") {
						t.Fatalf("原始查询错误丢失：%v", err)
					}
				})
			}
		})
	}
}
