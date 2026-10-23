package ctp

import def "gitee.com/haifengat/goctp/ctpdefine"

// unfinishedOrderRows 剔除全零无内容占位与已知终态；非空的未知状态必须保留。
func unfinishedOrderRows(rows []def.CThostFtdcOrderField) []*def.CThostFtdcOrderField {
	var out []*def.CThostFtdcOrderField
	for i := range rows {
		// 2026-10-23 只读实测：空委托列表的结束回报带全零结构指针，而非 nil。
		// 必须比较整个结构；只看状态为零会漏掉尚未确认状态的真实委托。
		if rows[i] == (def.CThostFtdcOrderField{}) {
			continue
		}
		switch rows[i].OrderStatus {
		case def.THOST_FTDC_OST_AllTraded, def.THOST_FTDC_OST_PartTradedNotQueueing, def.THOST_FTDC_OST_NoTradeNotQueueing, def.THOST_FTDC_OST_Canceled:
			continue
		}
		cp := rows[i]
		out = append(out, &cp)
	}
	return out
}
