package ctp

import def "gitee.com/haifengat/goctp/ctpdefine"

// ⚠️ 本文件**不带 build tag**。这两个函数一行平台相关的东西都没有 ——
// 它们当初落在 `marketdata_windows.go` 里纯属**放错了地方**，而代价是
// `GOOS=linux go build ./...` 编不过，且 roadmap 里那句「已验证能过」
// 因此变成假话（20260910 评审抓到）。
//
// ⚠️ 教训：**「它和 Windows 代码放在一起」不等于「它需要 Windows」**。
// 一个函数该不该带 tag，看的是它用了什么，不是它的邻居是谁。

// splitSymbol 把 "SHFE.rb2701" 拆成交易所与合约。
func SplitSymbol(s string) (exchange, instrument string) {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return s[:i], s[i+1:]
		}
	}
	return "", s
}

// FarPrice 给出一个**挂得上但成不了**的价格。
//
//	买单 → 跌停价     卖单 → 涨停价
//
// ⚠️ 用涨跌停而不是「市价±很多」：后者会**越界被拒**，而被拒的单挂不上，
// 于是撤单那一步根本走不到 —— 而 P3 要验的正是完整往返。
//
// ⚠️ 它仍然可能成交：行情真打到涨跌停时这笔单会成。**那不是缺陷，是这条路的边界**，
// 调用方必须准备好处理「它成交了」，不能假定一定挂着。
func FarPrice(m *def.CThostFtdcDepthMarketDataField, dir def.TThostFtdcDirectionType) float64 {
	if dir == def.THOST_FTDC_D_Buy {
		return float64(m.LowerLimitPrice)
	}
	return float64(m.UpperLimitPrice)
}
