package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/shopspring/decimal"
)

// roundingReader 没有任何报单、撤单方法，前置检查无法借此接口改变持仓。
type roundingReader interface {
	TradingDay() string
	Instrument(string, time.Duration) (*def.CThostFtdcInstrumentField, error)
	CommissionRate(string, time.Duration) (*def.CThostFtdcInstrumentCommissionRateField, error)
	Positions(time.Duration) (map[string]*def.CThostFtdcInvestorPositionField, error)
	UnfinishedOrders(time.Duration) ([]*def.CThostFtdcOrderField, error)
	Account(time.Duration) (*def.CThostFtdcTradingAccountField, error)
}

// roundingPreflightReport 仅含白名单业务字段；不得加入原始 CTP 结构或身份信息。
type roundingPreflightReport struct {
	Format                               int `json:"format"`
	StartedUTC, FinishedUTC              string
	Symbol, TradingDay                   string
	Tick, Multiplier, Expiry             string
	OpenMoney, CloseMoney, TodayMoney    string
	OpenVolume, CloseVolume, TodayVolume string
	FeeScope                             string
	DeclaredTrading                      bool
	Long, Short                          longSides
	UnfinishedOrders                     int
	Available, Commission                string
	Stage                                string
	Blockers                             []string
	Requires                             []string
}

// collectRoundingPreflight 只用于准备登记，顺序查询不是原子账户快照，也不授予下单权限。
func collectRoundingPreflight(c roundingReader, symbol string, timeout time.Duration) (*roundingPreflightReport, error) {
	ex, inst := ctp.SplitSymbol(symbol)
	if ex != "DCE" || !strings.HasPrefix(inst, "lh") || len(inst) != 6 {
		return nil, fmt.Errorf("本检查只支持显式的 DCE.lh四位年月合约")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("查询超时必须为正")
	}
	day := c.TradingDay()
	if _, err := time.Parse("20060102", day); err != nil {
		return nil, fmt.Errorf("柜台交易日无效")
	}
	report := &roundingPreflightReport{Format: 1, Symbol: symbol, TradingDay: day, StartedUTC: time.Now().UTC().Format(time.RFC3339Nano), Requires: []string{"核验实际交易日历和日盘窗口", "核验报价新鲜度、流动性和资金上限", "确认单独的种子及跨结算持仓范围", "新一笔两手及临时阀门调整需明确授权", "候选和判据正式登记推送后才执行", "本报告是顺序只读查询，不是原子快照或下单许可；未终结委托计数包含过渡态；仍需避免其他操作者并发使用账户"}}
	spec, err := c.Instrument(symbol, timeout)
	if err != nil {
		return nil, fmt.Errorf("合约查询：%w", err)
	}
	if spec == nil || ctp.Text(spec.ExchangeID[:]) != ex || ctp.Text(spec.InstrumentID[:]) != inst || spec.VolumeMultiple <= 0 || !finitePositive(float64(spec.PriceTick)) {
		return nil, fmt.Errorf("合约声明缺失、不匹配或数值无效")
	}
	report.Tick = decimal.NewFromFloat(float64(spec.PriceTick)).String()
	report.Multiplier = fmt.Sprint(spec.VolumeMultiple)
	report.Expiry = ctp.Text(spec.ExpireDate[:])
	report.DeclaredTrading = spec.IsTrading != 0
	if !report.DeclaredTrading {
		report.Blockers = append(report.Blockers, "柜台声明合约不可交易")
	}
	if expiry, err := time.Parse("20060102", report.Expiry); err != nil || expiry.Format("20060102") < day {
		return nil, fmt.Errorf("合约到期日无效或已到期")
	}
	rates, err := c.CommissionRate(symbol, timeout)
	if err != nil {
		return nil, fmt.Errorf("费率查询：%w", err)
	}
	if rates == nil || ctp.Text(rates.InstrumentID[:]) != inst {
		return nil, fmt.Errorf("费率声明缺失或合约不匹配")
	}
	values := []float64{float64(rates.OpenRatioByMoney), float64(rates.CloseRatioByMoney), float64(rates.CloseTodayRatioByMoney), float64(rates.OpenRatioByVolume), float64(rates.CloseRatioByVolume), float64(rates.CloseTodayRatioByVolume)}
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e300 {
			return nil, fmt.Errorf("费率数值无效")
		}
	}
	report.OpenMoney = decimal.NewFromFloat(values[0]).String()
	report.CloseMoney = decimal.NewFromFloat(values[1]).String()
	report.TodayMoney = decimal.NewFromFloat(values[2]).String()
	report.OpenVolume = decimal.NewFromFloat(values[3]).String()
	report.CloseVolume = decimal.NewFromFloat(values[4]).String()
	report.TodayVolume = decimal.NewFromFloat(values[5]).String()
	report.FeeScope = investorRange(rates)
	if rates.InvestorRange != def.THOST_FTDC_IR_All && rates.InvestorRange != def.THOST_FTDC_IR_Group && rates.InvestorRange != def.THOST_FTDC_IR_Single {
		report.Blockers = append(report.Blockers, "费率适用范围取值未知，不能直接用于登记")
	}
	if values[3] != 0 || values[4] != 0 || values[5] != 0 || values[1] <= 0 || values[2] <= 0 || values[1] == values[2] {
		report.Blockers = append(report.Blockers, "费率结构不能用于本次纯按额差别取整实验")
	}
	positions, err := c.Positions(timeout)
	if err != nil {
		return nil, fmt.Errorf("持仓查询：%w", err)
	}
	for _, p := range positions {
		if p == nil {
			return nil, fmt.Errorf("持仓含空记录")
		}
		if ctp.Text(p.InstrumentID[:]) != inst {
			continue
		}
		if ctp.Text(p.ExchangeID[:]) != ex || (p.PosiDirection != def.THOST_FTDC_PD_Long && p.PosiDirection != def.THOST_FTDC_PD_Short) {
			return nil, fmt.Errorf("持仓交易所或方向不匹配")
		}
		if p.HedgeFlag != def.THOST_FTDC_HF_Speculation {
			report.Blockers = append(report.Blockers, "合约含非投机记录，不能合并判断实验前提")
		}
		if p.Position < 0 || p.TodayPosition < 0 || p.TodayPosition > p.Position || p.OpenVolume < 0 {
			return nil, fmt.Errorf("持仓数量不合法")
		}
	}
	report.Long = longSidesOf(positions, inst)
	report.Short = shortSidesOf(positions, inst)
	orders, err := c.UnfinishedOrders(timeout)
	if err != nil {
		return nil, fmt.Errorf("委托查询：%w", err)
	}
	for _, o := range orders {
		if o == nil {
			return nil, fmt.Errorf("委托含空记录")
		}
	}
	report.UnfinishedOrders = len(orders)
	if len(orders) > 0 {
		report.Blockers = append(report.Blockers, "账户有未终结委托，先核对归属与干扰")
	}
	account, err := c.Account(timeout)
	if err != nil {
		return nil, fmt.Errorf("资金查询：%w", err)
	}
	if account == nil || !finitePositive(float64(account.Available)) || math.IsNaN(float64(account.Commission)) || math.IsInf(float64(account.Commission), 0) {
		return nil, fmt.Errorf("资金声明缺失或无效")
	}
	report.Available = decimal.NewFromFloat(float64(account.Available)).String()
	report.Commission = decimal.NewFromFloat(float64(account.Commission)).String()
	report.Stage = "blocked"
	l, s := report.Long, report.Short
	if s.Today != 0 || s.Yd != 0 || s.OpenedToday != 0 {
		report.Blockers = append(report.Blockers, "存在空头持仓或当日开空累计")
	}
	if len(report.Blockers) == 0 {
		switch {
		case l.Today == 0 && l.Yd == 0 && l.OpenedToday == 0:
			report.Stage = "empty-needs-seed-plan"
		case l.Today == 1 && l.Yd == 0 && l.OpenedToday == 1:
			report.Stage = "seed-awaiting-settlement"
		case l.Today == 0 && l.Yd == 1 && l.OpenedToday == 0:
			report.Stage = "history-seed-needs-experiment-authorization"
		default:
			report.Blockers = append(report.Blockers, "多头今昨或当日开仓累计不满足登记起点")
		}
	}
	report.FinishedUTC = time.Now().UTC().Format(time.RFC3339Nano)
	return report, nil
}

func finitePositive(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v > 0 && v < 1e300 }

// runCTPRoundingPreflight 日志走 stderr，stdout 只输出完整白名单报告，不写原始字段。
func runCTPRoundingPreflight(args []string) error {
	fs := flag.NewFlagSet("ctp-rounding-preflight", flag.ContinueOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "显式合约，例如 DCE.lh2701")
	timeout := fs.Duration("timeout", 10*time.Second, "每步只读查询超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" || *timeout <= 0 {
		return fmt.Errorf("必须给合约和正超时")
	}
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	c := ctp.New(ctp.Credentials{Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID, Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode}, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}
	report, err := collectRoundingPreflight(c, *symbol, *timeout)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}
