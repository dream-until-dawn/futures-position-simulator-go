package main

import (
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/shopspring/decimal"
)

// restingProbeDate 与 restingProbeDay 分别绑定今晚自然日和柜台交易日，禁止把夜盘日期混为一谈。
const restingProbeDate = "20261029"
const restingProbeDay = "20261030"

var probeShanghai = time.FixedZone("Asia/Shanghai", 8*60*60)

// 本地短暂排队回报不能排除即时撮合与回调延迟；至少观察两秒才纳入本次样本判据。
const minRestingQueueMillis int64 = 2000

// validateRestingQuote 仅支持本轮夜盘；行情时刻用自然日拼接，交易日仍由柜台独立核验。
func validateRestingQuote(md *def.CThostFtdcDepthMarketDataField, spec *def.CThostFtdcInstrumentField, day string, now time.Time) error {
	local := now.In(probeShanghai)
	hm := local.Hour()*100 + local.Minute()
	if day != restingProbeDay || local.Format("20060102") != restingProbeDate || hm < 2100 || hm >= 2145 {
		return fmt.Errorf("不在本次登记的夜盘自然日、交易日或收尾窗口")
	}
	if md == nil || spec == nil || ctp.Text(md.InstrumentID[:]) != "rb2701" || ctp.Text(spec.InstrumentID[:]) != "rb2701" || ctp.Text(spec.ExchangeID[:]) != "SHFE" || spec.IsTrading == 0 || spec.PriceTick != 1 || spec.VolumeMultiple != 10 {
		return fmt.Errorf("合约或可交易声明不匹配")
	}
	if ctp.Text(md.TradingDay[:]) != day {
		return fmt.Errorf("行情交易日不匹配")
	}
	quoted, err := time.ParseInLocation("20060102 15:04:05", restingProbeDate+" "+ctp.Text(md.UpdateTime[:]), probeShanghai)
	if err != nil || md.UpdateMillisec < 0 || md.UpdateMillisec > 999 {
		return fmt.Errorf("行情时刻无效")
	}
	quoted = quoted.Add(time.Duration(md.UpdateMillisec) * time.Millisecond)
	if age := now.Sub(quoted); age < 0 || age > 5*time.Second {
		return fmt.Errorf("行情陈旧或时钟不一致")
	}
	for _, v := range []float64{float64(spec.PriceTick), float64(md.LastPrice), float64(md.AskPrice1), float64(md.BidPrice1), float64(md.LowerLimitPrice), float64(md.UpperLimitPrice)} {
		if !finitePositive(v) {
			return fmt.Errorf("行情或价位参数缺失")
		}
	}
	if md.BidVolume1 <= 0 || md.AskVolume1 <= 0 || md.LowerLimitPrice >= md.UpperLimitPrice || md.LastPrice < md.LowerLimitPrice || md.LastPrice > md.UpperLimitPrice || md.BidPrice1 > md.AskPrice1 {
		return fmt.Errorf("行情买卖盘或上下限不成立")
	}
	return nil
}

// passiveProbePrice 先固定定价规则，现场只代入当时行情，不追价选择结果。
func passiveProbePrice(md *def.CThostFtdcDepthMarketDataField, spec *def.CThostFtdcInstrumentField, day string, now time.Time) (decimal.Decimal, error) {
	if err := validateRestingQuote(md, spec, day, now); err != nil {
		return decimal.Zero, err
	}
	tick := decimal.NewFromFloat(float64(spec.PriceTick))
	price := decimal.NewFromFloat(float64(md.LastPrice)).Sub(tick.Mul(decimal.NewFromInt(3)))
	lower := decimal.NewFromFloat(float64(md.LowerLimitPrice))
	upper := decimal.NewFromFloat(float64(md.UpperLimitPrice))
	if !price.IsPositive() || !price.Mod(tick).IsZero() || !lower.Mod(tick).IsZero() || !upper.Mod(tick).IsZero() || price.LessThan(lower) || price.GreaterThan(upper) || !price.LessThan(decimal.NewFromFloat(float64(md.AskPrice1))) {
		return decimal.Zero, fmt.Errorf("离最新价三跳的买价不是合法被动价格")
	}
	return price, nil
}

// restingPriceVerdict 在空账户实验约束下只认唯一的一手买开，并要求曾观察到排队。
func restingPriceVerdict(rows []*def.CThostFtdcTradeField, out ownedOrderOutcome, price decimal.Decimal, day string) (string, decimal.Decimal, error) {
	if len(rows) != 1 || rows[0] == nil {
		return "no-verdict", decimal.Zero, fmt.Errorf("本次成交不能唯一归属")
	}
	r := rows[0]
	if ctp.Text(r.TradeID[:]) == "" || ctp.Text(r.ExchangeID[:]) != "SHFE" || ctp.Text(r.InstrumentID[:]) != "rb2701" || ctp.Text(r.OrderRef[:]) != out.Ref || out.SysID == "" || ctp.Text(r.OrderSysID[:]) != out.SysID || r.Direction != def.THOST_FTDC_D_Buy || r.OffsetFlag != def.THOST_FTDC_OF_Open || r.HedgeFlag != def.THOST_FTDC_HF_Speculation || int(r.Volume) != 1 || !finitePositive(float64(r.Price)) || ctp.Text(r.TradingDay[:]) != day {
		return "no-verdict", decimal.Zero, fmt.Errorf("成交身份、方向、手数或交易日不匹配")
	}
	at, err := time.Parse("15:04:05", ctp.Text(r.TradeTime[:]))
	if err != nil || at.Hour()*100+at.Minute() < 2115 || at.Hour()*100+at.Minute() >= 2140 {
		return "no-verdict", decimal.Zero, fmt.Errorf("成交不在登记的连续竞价观察窗口")
	}
	actual := decimal.NewFromFloat(float64(r.Price))
	if !out.QueueObserved || out.SubmissionUncertain || out.LocalQueueMillis < minRestingQueueMillis {
		return "no-verdict", actual, nil
	}
	if actual.Equal(price) {
		return "supports-order-price", actual, nil
	}
	return "counterexample", actual, nil
}
