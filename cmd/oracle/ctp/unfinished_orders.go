package ctp

import def "gitee.com/haifengat/goctp/ctpdefine"

// unfinishedOrderRows 只剔除已知终态；不认识的状态必须保留，失败方向朝阻止新实验。
func unfinishedOrderRows(rows []def.CThostFtdcOrderField) []*def.CThostFtdcOrderField {
	var out []*def.CThostFtdcOrderField
	for i := range rows {
		switch rows[i].OrderStatus {
		case def.THOST_FTDC_OST_AllTraded, def.THOST_FTDC_OST_PartTradedNotQueueing, def.THOST_FTDC_OST_NoTradeNotQueueing, def.THOST_FTDC_OST_Canceled:
			continue
		}
		cp := rows[i]
		out = append(out, &cp)
	}
	return out
}
