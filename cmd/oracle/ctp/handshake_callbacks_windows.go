//go:build windows

package ctp

import def "gitee.com/haifengat/goctp/ctpdefine"

// registerHandshakeCallbacks 只复制回报；推进阶段仍由原执行器负责。
func (c *Client) registerHandshakeCallbacks(hs *handshakeCollector) {
	c.on("SetOnFrontConnected", func() uintptr {
		hs.recordEntry(handshakeFront, 0, 0, true, true)
		hs.reply(handshakeFront, 0, &handshakeLoginResult{}, 0, true)
		return 0
	})
	c.on("SetOnFrontDisconnected", func(reason int) uintptr {
		hs.recordEntry("disconnected", 0, int32(reason), true, false)
		hs.abort("disconnected", int32(reason))
		return 0
	})
	c.on("SetOnRspAuthenticate", func(row *def.CThostFtdcRspAuthenticateField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		hs.recordEntry(handshakeAuth, id, handshakeErrorID(info), last, row != nil)
		var payload *handshakeLoginResult
		if row != nil {
			payload = &handshakeLoginResult{}
		}
		hs.reply(handshakeAuth, id, payload, handshakeErrorID(info), last)
		return 0
	})
	c.on("SetOnRspUserLogin", func(row *def.CThostFtdcRspUserLoginField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		hs.recordEntry(handshakeLogin, id, handshakeErrorID(info), last, row != nil)
		var payload *handshakeLoginResult
		if row != nil {
			payload = &handshakeLoginResult{day: text(row.TradingDay[:]), maxOrderRef: text(row.MaxOrderRef[:]), frontID: int(row.FrontID), sessionID: int(row.SessionID)}
		}
		hs.reply(handshakeLogin, id, payload, handshakeErrorID(info), last)
		return 0
	})
	c.on("SetOnRspSettlementInfoConfirm", func(row *def.CThostFtdcSettlementInfoConfirmField, info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		hs.recordEntry(handshakeConfirm, id, handshakeErrorID(info), last, row != nil)
		var payload *handshakeLoginResult
		if row != nil {
			payload = &handshakeLoginResult{}
		}
		hs.reply(handshakeConfirm, id, payload, handshakeErrorID(info), last)
		return 0
	})
	c.on("SetOnRspError", func(info *def.CThostFtdcRspInfoField, id int, last bool) uintptr {
		hs.recordEntry("general-error", id, handshakeErrorID(info), last, info != nil)
		if err := errOf(info); err != nil {
			c.logf("[ctp] OnRspError request=%d code=%d", id, info.ErrorID)
			hs.generalError(id, int32(info.ErrorID))
			// 通用错误回调也带请求编号；不能只打印后让调用方误读为空成功。
			c.positionRows.receive(id, nil, err, true)
			c.orderRows.receive(id, nil, err, true)
			c.tradeRows.receive(id, nil, err, true)
			c.settlementRows.receive(id, nil, err, true)
			c.accountRows.receive(id, nil, err, true)
			c.commissionRows.receive(id, nil, err, true)
			c.instrumentRows.receive(id, nil, err, true)
			c.marketRows.receive(id, nil, err, true)
			c.brokerRows.receive(id, nil, err, true)
		}
		return 0
	})

}
