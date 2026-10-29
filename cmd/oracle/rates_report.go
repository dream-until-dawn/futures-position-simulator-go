package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// commissionRateReader 仅暴露费率查询，离线测试无需构造连接或接触凭据。
type commissionRateReader interface {
	CommissionRate(string, time.Duration) (*def.CThostFtdcInstrumentCommissionRateField, error)
}

// reportCTPRates 汇总单次只读查询；连接由原CLI负责，便于独立验证结果与失败传播。
func reportCTPRates(c commissionRateReader, symbols string, timeout time.Duration, logf func(string, ...any)) error {
	if strings.TrimSpace(symbols) == "" || timeout <= 0 {
		return fmt.Errorf("费率查询范围或超时无效")
	}
	logf("")
	logf("%-16s %12s %10s %12s %10s %12s %10s  %s", "合约",
		"开仓/按额", "开仓/每手", "平昨/按额", "平昨/每手", "平今/按额", "平今/每手",
		"适用范围")
	var interesting []string
	for _, sym := range strings.Split(symbols, ",") {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		r, err := c.CommissionRate(sym, timeout)
		if err != nil {
			return fmt.Errorf("手续费查询失败 symbol=%s: %w", sym, err)
		}
		if err := validateRateRow(sym, r); err != nil {
			return err
		}
		logf("%-16s %12.8g %10.8g %12.8g %10.8g %12.8g %10.8g  %s", sym,
			float64(r.OpenRatioByMoney), float64(r.OpenRatioByVolume),
			float64(r.CloseRatioByMoney), float64(r.CloseRatioByVolume),
			float64(r.CloseTodayRatioByMoney), float64(r.CloseTodayRatioByVolume),
			investorRange(r))

		// ⚠️ 三档是不是同一组数 —— #8 的后半格（平今档仍未测）就挂在这里。
		if !sameTier(r) {
			logf("      ⚠️ **三档不同**：#8 的「开仓档 = 平昨档 = 平今档」在这个合约上不成立")
		}
		if d := rateDigits(float64(r.OpenRatioByMoney)); d >= 4 {
			interesting = append(interesting, fmt.Sprintf("%s（按额费率 %g，%d 位有效小数）",
				sym, float64(r.OpenRatioByMoney), d))
		}
	}
	logf("")
	if len(interesting) == 0 {
		logf("⇒ ⚠️ 这一批里**没有**一个合约的按额费率比 1e-4 更细 ——")
		logf("   #5 的两个残余候选（不取整 / 取到三位或更细）在它们身上仍然同值。")
		logf("   ⚠️ 这是一条**有信息的空结果**：它说的是「再多测这些合约也分不开」，")
		logf("   不是「没测出来」。要接着走得换一批合约，或者换一个柜台。")
	} else {
		logf("⇒ 这些合约的按额费率够细，**可能**把 #5 的两个候选分开：")
		for _, s := range interesting {
			logf("     · %s", s)
		}
		logf("   ⚠️ 「够细」只是必要条件：还要 名义金额 × 费率 真的落在三位以外，")
		logf("   而那取决于当时的价格与乘数 —— 拿 `ctp-fee` 去那个合约上实测才算数。")
	}
	return nil
}

// validateRateRow 区分合法零费率与查询空值/异合约/未知适用范围，禁止把未知变成0。
func validateRateRow(symbol string, row *def.CThostFtdcInstrumentCommissionRateField) error {
	exchange, instrument, ok := strings.Cut(symbol, ".")
	if !ok || exchange == "" || instrument == "" || row == nil || ctp.Text(row.InstrumentID[:]) != instrument || (ctp.Text(row.ExchangeID[:]) != "" && ctp.Text(row.ExchangeID[:]) != exchange) {
		return fmt.Errorf("费率查询无有效匹配记录")
	}
	if row.InvestorRange != def.THOST_FTDC_IR_All && row.InvestorRange != def.THOST_FTDC_IR_Group && row.InvestorRange != def.THOST_FTDC_IR_Single {
		return fmt.Errorf("费率适用范围未知")
	}
	if row.InvestorRange == def.THOST_FTDC_IR_Single && ctp.Text(row.InvestorID[:]) == "" {
		return fmt.Errorf("单一投资者费率缺少适用身份声明")
	}
	for _, value := range []float64{float64(row.OpenRatioByMoney), float64(row.OpenRatioByVolume), float64(row.CloseRatioByMoney), float64(row.CloseRatioByVolume), float64(row.CloseTodayRatioByMoney), float64(row.CloseTodayRatioByVolume)} {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("六项费率包含非有限数或负值")
		}
	}
	return nil
}
