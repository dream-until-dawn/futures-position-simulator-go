package main

import (
	"encoding/hex"
	"fmt"
	"strconv"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/shopspring/decimal"
)

// restingTradeEvidence 逐字段摘录本次成交，不允许直接序列化包含身份信息的 CTP 结构。
type restingTradeEvidence struct {
	TradeID, OrderRef, OrderSysID, Exchange, Instrument, TradingDay, TradeDate, TradeTime, Price string
	DirectionByte, OffsetByte, HedgeByte                                                         byte
	Volume                                                                                       int
}

func copyOwnedTradeEvidence(rows []*def.CThostFtdcTradeField, owner ownedOrderOutcome) []restingTradeEvidence {
	var out []restingTradeEvidence
	for _, r := range rows {
		if r == nil || ctp.Text(r.OrderRef[:]) != owner.Ref || ctp.Text(r.ExchangeID[:]) != "SHFE" || ctp.Text(r.InstrumentID[:]) != "rb2701" {
			continue
		}
		out = append(out, restingTradeEvidence{TradeID: ctp.Text(r.TradeID[:]), OrderRef: ctp.Text(r.OrderRef[:]), OrderSysID: ctp.Text(r.OrderSysID[:]), Exchange: ctp.Text(r.ExchangeID[:]), Instrument: ctp.Text(r.InstrumentID[:]), TradingDay: ctp.Text(r.TradingDay[:]), TradeDate: ctp.Text(r.TradeDate[:]), TradeTime: ctp.Text(r.TradeTime[:]), Price: strconv.FormatFloat(float64(r.Price), 'g', -1, 64), DirectionByte: byte(r.Direction), OffsetByte: byte(r.OffsetFlag), HedgeByte: byte(r.HedgeFlag), Volume: int(r.Volume)})
	}
	return out
}

// verifyRestingReport 只核验落盘数据与预先登记判据的一致性，不声称提供独立柜台或签名证明。
func verifyRestingReport(report restingProbeReport) (string, error) {
	if report.Format != 1 || report.Stage != "finished" || report.Failed || !report.FinalClean {
		return "", fmt.Errorf("报告未完整成功收尾，不能作为完成的价格证据")
	}
	if len(report.PlanCommit) != 40 || report.BuildCommit != report.PlanCommit {
		return "", fmt.Errorf("报告构建与计划不一致")
	}
	if _, err := hex.DecodeString(report.PlanCommit); err != nil {
		return "", fmt.Errorf("报告计划号无效")
	}
	if report.Symbol != "SHFE.rb2701" || report.TradingDay != restingProbeDay || report.Tick != "1" || report.Multiplier != "10" {
		return "", fmt.Errorf("报告范围不符合登记")
	}
	if report.Control.Ref == "" || !report.Control.Terminal || !report.Control.QueueObserved || !report.Control.CancelAttempted || report.Control.Filled != 0 || report.Control.ErrorID != 0 {
		return "", fmt.Errorf("报告没有合格撤单对照")
	}
	feeBefore, err := strconv.ParseFloat(report.ControlFeeBefore, 64)
	if err != nil || feeBefore != 0 {
		return "", fmt.Errorf("费用对照起点无效")
	}
	feeAfter, err := strconv.ParseFloat(report.ControlFeeAfter, 64)
	if err != nil || !validProbeFee(feeAfter) {
		return "", fmt.Errorf("费用对照终点无效")
	}
	if report.Main.Ref == "" || report.Main.Ref == report.Control.Ref || !report.Main.Terminal {
		return "", fmt.Errorf("主挂单归属或终态无效")
	}
	if report.Main.Filled == 0 {
		if report.Verdict != "no-verdict" || report.ObservedTradeRows != 0 || len(report.MainTrades) != 0 || report.Close.Ref != "" {
			return "", fmt.Errorf("未成交报告却包含成交判定或清理单")
		}
		return "no-verdict", nil
	}
	if report.Main.Filled != 1 || !report.Close.Terminal || report.Close.Filled != 1 || report.Close.Ref == "" || report.Close.Ref == report.Main.Ref || report.Close.Ref == report.Control.Ref {
		return "", fmt.Errorf("本次一手成交或收尾不完整")
	}
	if report.ObservedTradeRows != 1 || len(report.MainTrades) != 1 {
		return "", fmt.Errorf("原始成交证据不唯一或缺失")
	}
	evidence := report.MainTrades[0]
	price, err := strconv.ParseFloat(evidence.Price, 64)
	if err != nil || !finitePositive(price) {
		return "", fmt.Errorf("原始成交价无效")
	}
	orderPrice, err := strconv.ParseFloat(report.OrderPrice, 64)
	if err != nil || !finitePositive(orderPrice) || orderPrice > 5000 {
		return "", fmt.Errorf("登记挂单价无效")
	}
	if evidence.Volume != 1 {
		return "", fmt.Errorf("原始成交手数无效")
	}
	raw := &def.CThostFtdcTradeField{Direction: def.TThostFtdcDirectionType(evidence.DirectionByte), OffsetFlag: def.TThostFtdcOffsetFlagType(evidence.OffsetByte), HedgeFlag: def.TThostFtdcHedgeFlagType(evidence.HedgeByte), Volume: 1, Price: def.TThostFtdcPriceType(price)}
	copy(raw.TradeID[:], evidence.TradeID)
	copy(raw.OrderRef[:], evidence.OrderRef)
	copy(raw.OrderSysID[:], evidence.OrderSysID)
	copy(raw.ExchangeID[:], evidence.Exchange)
	copy(raw.InstrumentID[:], evidence.Instrument)
	copy(raw.TradingDay[:], evidence.TradingDay)
	copy(raw.TradeDate[:], evidence.TradeDate)
	copy(raw.TradeTime[:], evidence.TradeTime)
	verdict, actual, err := restingPriceVerdict([]*def.CThostFtdcTradeField{raw}, report.Main, decimal.NewFromFloat(orderPrice), report.TradingDay)
	if err != nil || verdict != report.Verdict || actual.String() != report.ActualPrice {
		return "", fmt.Errorf("报告判定与原始成交字段不一致：%v", err)
	}
	return verdict, nil
}
