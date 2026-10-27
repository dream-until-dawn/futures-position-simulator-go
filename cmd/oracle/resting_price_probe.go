package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/shopspring/decimal"
)

type restingReadClient interface {
	roundingReader
	MarketData(string, time.Duration) (*def.CThostFtdcDepthMarketDataField, error)
	Trades(time.Duration) ([]*def.CThostFtdcTradeField, error)
}

type restingProbeClient interface {
	restingReadClient
	ownedOrderClient
}

// restingProbeReport 是逐阶段追加的白名单证据，不包含账户身份或柜台自由文本。
type restingProbeReport struct {
	ObservedTradeRows                                                            int
	MainTrades                                                                   []restingTradeEvidence
	ControlPrice                                                                 string
	BuildCommit                                                                  string
	Format                                                                       int
	PlanCommit, Symbol, TradingDay, StartedUTC, UpdatedUTC, Stage                string
	Tick, Multiplier, OrderPrice, ActualPrice, ControlFeeBefore, ControlFeeAfter string
	Control, Main, Close                                                         ownedOrderOutcome
	Verdict                                                                      string
	FinalClean                                                                   bool
	Failed                                                                       bool
}

type restingProbeConfig struct {
	BuildCommit   string
	PlanCommit    string
	Wait, Timeout time.Duration
}

// checkRestingEmpty 查询失败不能变成“空”；此探针要求整个账户没有其他实验敞口。
func checkRestingEmpty(c restingReadClient, timeout time.Duration, requireNoFees bool) (decimal.Decimal, error) {
	positions, err := c.Positions(timeout)
	if err != nil {
		return decimal.Zero, err
	}
	for _, p := range positions {
		if p == nil || p.Position != 0 || p.TodayPosition != 0 || p.OpenVolume != 0 {
			return decimal.Zero, fmt.Errorf("账户非空或当日已有开仓累计，停止探针")
		}
	}
	orders, err := c.UnfinishedOrders(timeout)
	if err != nil {
		return decimal.Zero, err
	}
	if len(orders) != 0 {
		return decimal.Zero, fmt.Errorf("账户有未终结委托，停止探针")
	}
	trades, err := c.Trades(timeout)
	if err != nil {
		return decimal.Zero, err
	}
	if len(trades) != 0 {
		return decimal.Zero, fmt.Errorf("账户今天已有成交，停止探针")
	}
	account, err := c.Account(timeout)
	if err != nil {
		return decimal.Zero, err
	}
	if account == nil || !finitePositive(float64(account.Available)) || float64(account.Available) < 50000 || !validProbeFee(float64(account.Commission)) {
		return decimal.Zero, fmt.Errorf("账户可用不足五万元或资金声明无效")
	}
	fee := decimal.NewFromFloat(float64(account.Commission))
	if requireNoFees && !fee.IsZero() {
		return decimal.Zero, fmt.Errorf("账户今天已有手续费，不能隔离费用对照")
	}
	return fee, nil
}

func validProbeFee(v float64) bool { return v == 0 || finitePositive(v) }

// finishRestingExposure 只允许平本次已确认的一手今仓，绝不调用全合约扫单或全仓平仓。
func finishRestingExposure(c restingProbeClient, report *restingProbeReport, timeout time.Duration, clock probeClock, emit func() error) error {
	owner := report.Main
	if owner.Ref == "" {
		owner = report.Control
	}
	if owner.Ref == "" {
		return nil
	}
	if !owner.Terminal {
		return fmt.Errorf("本次开仓委托终态未知，保留引用供处置，不继续发单")
	}
	positions, err := c.Positions(timeout)
	if err != nil {
		return err
	}
	held := 0
	for _, p := range positions {
		if p == nil {
			return fmt.Errorf("收尾持仓记录缺失")
		}
		if p.Position == 0 {
			continue
		}
		if ctp.Text(p.ExchangeID[:]) != "SHFE" || ctp.Text(p.InstrumentID[:]) != "rb2701" || p.PosiDirection != def.THOST_FTDC_PD_Long || p.HedgeFlag != def.THOST_FTDC_HF_Speculation || p.PositionDate != def.THOST_FTDC_PSD_Today || p.TodayPosition != p.Position {
			return fmt.Errorf("出现非本次可归属的一手今仓，不扩大收尾范围")
		}
		held += int(p.Position)
	}
	if held != 0 {
		if held != 1 || owner.Filled != 1 {
			return fmt.Errorf("收尾持仓与本次一手成交不一致")
		}
		spec, err := c.Instrument(report.Symbol, timeout)
		if err != nil {
			return err
		}
		md, err := c.MarketData(report.Symbol, timeout)
		if err != nil {
			return err
		}
		if err = validateRestingQuote(md, spec, report.TradingDay, clock.Now()); err != nil {
			return err
		}
		report.Stage = "closing-owned-long"
		persistErr := emit()
		closeReq := ctp.OrderReq{Exchange: "SHFE", Instrument: "rb2701", Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_CloseToday, Volume: 1, LimitPrice: float64(md.LowerLimitPrice)}
		report.Close, err = waitOwnedOrder(c, closeReq, 2*time.Second, 10*time.Second, timeout, clock)
		if err != nil || report.Close.Filled != 1 || !report.Close.Terminal {
			return errors.Join(persistErr, err, fmt.Errorf("本次一手收尾未确认全部成交"))
		}
		positions, err = c.Positions(timeout)
		if err != nil {
			return errors.Join(persistErr, err)
		}
		for _, p := range positions {
			if p == nil || p.Position != 0 || p.TodayPosition != 0 {
				return errors.Join(persistErr, fmt.Errorf("平仓后仍有持仓或状态未知"))
			}
		}
		if persistErr != nil {
			return persistErr
		}
	}
	orders, err := c.UnfinishedOrders(timeout)
	if err != nil {
		return err
	}
	if len(orders) != 0 {
		return fmt.Errorf("收尾后仍有未终结委托")
	}
	if owner.Filled == 1 && held == 0 && report.Close.Ref == "" {
		return fmt.Errorf("已报成交但持仓为空，不能确认本次收尾归属")
	}
	report.FinalClean = true
	return nil
}

// executeRestingPrice 有限状态流程：费用对照、唯一主挂单、归属判据、始终执行收尾。
func executeRestingPrice(c restingProbeClient, cfg restingProbeConfig, clock probeClock, save func(*restingProbeReport) error) (report *restingProbeReport, runErr error) {
	report = &restingProbeReport{
		Format: 1, PlanCommit: cfg.PlanCommit, BuildCommit: cfg.BuildCommit,
		Symbol: "SHFE.rb2701", TradingDay: c.TradingDay(),
		StartedUTC: clock.Now().UTC().Format(time.RFC3339Nano), Verdict: "no-verdict",
	}
	emit := func() error { report.UpdatedUTC = clock.Now().UTC().Format(time.RFC3339Nano); return save(report) }
	defer func() {
		runErr = errors.Join(runErr, finishRestingExposure(c, report, cfg.Timeout, clock, emit))
		report.Stage = "finished"
		report.Failed = runErr != nil
		runErr = errors.Join(runErr, emit())
		// 最后一条落盘本身也可能失败；返回给调用方的报告不能仍声称成功。
		report.Failed = runErr != nil
	}()
	local := clock.Now().In(probeShanghai)
	hm := local.Hour()*100 + local.Minute()
	if local.Format("20060102") != restingProbeDay || hm < 1055 || hm >= 1115 || cfg.Wait <= 0 || cfg.Wait > 90*time.Second || cfg.Timeout <= 0 || cfg.Timeout > 15*time.Second {
		return report, fmt.Errorf("本次探针日期、启动窗口或等待上限不成立")
	}
	report.Stage = "checking-empty-account"
	// 所有前提只读核验完毕并写下阶段证据后，才允许进入第一笔委托。
	if err := emit(); err != nil {
		return report, err
	}
	fee, err := checkRestingEmpty(c, cfg.Timeout, true)
	if err != nil {
		return report, err
	}
	report.ControlFeeBefore = fee.String()
	spec, err := c.Instrument(report.Symbol, cfg.Timeout)
	if err != nil {
		return report, err
	}
	md, err := c.MarketData(report.Symbol, cfg.Timeout)
	if err != nil {
		return report, err
	}
	price, err := passiveProbePrice(md, spec, report.TradingDay, clock.Now())
	if err != nil {
		return report, err
	}
	if spec.VolumeMultiple != 10 || price.Mul(decimal.NewFromInt(10)).GreaterThan(decimal.NewFromInt(50000)) {
		return report, fmt.Errorf("乘数不符或一手名义金额超过五万元")
	}
	report.Tick = decimal.NewFromFloat(float64(spec.PriceTick)).String()
	report.Multiplier = "10"
	report.Stage = "control-before-insert"
	report.ControlPrice = decimal.NewFromFloat(float64(md.LowerLimitPrice)).String()
	// 对照意外成交即终止主实验；defer 负责只平本次一手。
	if err = emit(); err != nil {
		return report, err
	}
	// 同步落盘可能阻塞；报单前再次核对同一份行情，只能停止，不能悄悄追价。
	if err = validateRestingQuote(md, spec, report.TradingDay, clock.Now()); err != nil {
		return report, err
	}
	controlReq := ctp.OrderReq{Exchange: "SHFE", Instrument: "rb2701", Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open, Volume: 1, LimitPrice: float64(md.LowerLimitPrice)}
	report.Control, err = waitOwnedOrder(c, controlReq, 500*time.Millisecond, 10*time.Second, cfg.Timeout, clock)
	report.Stage = "control-after-order"
	if persistErr := emit(); persistErr != nil {
		err = errors.Join(err, persistErr)
	}
	if err != nil {
		return report, err
	}
	if report.Control.Filled != 0 || !report.Control.QueueObserved || !report.Control.CancelAttempted || report.Control.ErrorID != 0 {
		return report, fmt.Errorf("费用对照未形成未成交排队后撤单，不进入主探针")
	}
	fee, err = checkRestingEmpty(c, cfg.Timeout, false)
	if err != nil {
		return report, err
	}
	report.ControlFeeAfter = fee.String()
	md, err = c.MarketData(report.Symbol, cfg.Timeout)
	if err != nil {
		return report, err
	}
	price, err = passiveProbePrice(md, spec, report.TradingDay, clock.Now())
	if err != nil {
		return report, err
	}
	if price.Mul(decimal.NewFromInt(10)).GreaterThan(decimal.NewFromInt(50000)) {
		return report, fmt.Errorf("主探针一手名义金额超过五万元")
	}
	report.OrderPrice = price.String()
	// 主挂单价格先落盘，再发单，禁止成交后倒填预言。
	report.Stage = "main-before-insert"
	if err = emit(); err != nil {
		return report, err
	}
	if err = validateRestingQuote(md, spec, report.TradingDay, clock.Now()); err != nil {
		return report, err
	}
	limit, _ := price.Float64()
	mainReq := controlReq
	mainReq.LimitPrice = limit
	report.Main, err = waitOwnedOrder(c, mainReq, cfg.Wait, 10*time.Second, cfg.Timeout, clock)
	report.Stage = "main-after-order"
	if persistErr := emit(); persistErr != nil {
		err = errors.Join(err, persistErr)
	}
	if err != nil {
		return report, err
	}
	if report.Main.Filled == 0 {
		return report, nil
	}
	trades, err := c.Trades(cfg.Timeout)
	if err != nil {
		return report, err
	}
	// 先把原始业务字段写下，再给出判定；不能只留下结论让后续无法重算。
	report.ObservedTradeRows = len(trades)
	report.MainTrades = copyOwnedTradeEvidence(trades, report.Main)
	report.Stage = "main-trades-observed"
	if err = emit(); err != nil {
		return report, err
	}
	verdict, actual, err := restingPriceVerdict(trades, report.Main, price, report.TradingDay)
	if err != nil {
		return report, err
	}
	report.Verdict = verdict
	if actual.IsPositive() {
		report.ActualPrice = actual.String()
	}
	return report, nil
}

// runCTPRestingPrice 必须显式执行、指定已推送的计划提交及全新报告路径。
func runCTPRestingPrice(args []string) error {
	fs := flag.NewFlagSet("ctp-resting-price", flag.ContinueOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	plan := fs.String("plan", "", "已推送计划的完整提交号")
	out := fs.String("out", "", "本机全新JSONL报告路径")
	execute := fs.Bool("execute", false, "按登记方案运行一手挂单探针")
	check := fs.Bool("check", false, "仅只读查询前提与现场行情，不报单")
	wait := fs.Duration("wait", 90*time.Second, "主挂单等待，最多90秒")
	timeout := fs.Duration("timeout", 10*time.Second, "每步查询超时，最多15秒")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *check {
		if *execute || *out != "" || *plan != "" || fs.NArg() != 0 {
			return fmt.Errorf("只读-check不与执行或报告参数混用")
		}
		return runRestingPriceCheck(*envPath, *timeout)
	}
	if !*execute || *out == "" || len(*plan) != 40 || fs.NArg() != 0 {
		return fmt.Errorf("必须显式-execute、-out及40位-plan提交号")
	}
	if _, err := hex.DecodeString(*plan); err != nil {
		return fmt.Errorf("计划提交号无效")
	}
	now := time.Now().In(probeShanghai)
	hm := now.Hour()*100 + now.Minute()
	if now.Format("20060102") != restingProbeDay || hm < 1055 || hm >= 1115 || *wait <= 0 || *wait > 90*time.Second || *timeout <= 0 || *timeout > 15*time.Second {
		return fmt.Errorf("不在登记启动窗口或等待参数无效")
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return fmt.Errorf("无法核对程序构建信息，停止执行")
	}
	buildCommit, err := validateRestingBuild(*plan, build.Settings)
	if err != nil {
		return err
	}
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	if err = validateRestingTarget(env.CTPBrokerID, env.CTPTdFront); err != nil {
		return err
	}
	file, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	c := ctp.New(ctp.Credentials{Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID, Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode}, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
	c.Valve = ctpValve(env)
	defer c.Close()
	if err = c.Connect(*timeout); err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	_, err = executeRestingPrice(c, restingProbeConfig{PlanCommit: *plan, BuildCommit: buildCommit, Wait: *wait, Timeout: *timeout}, wallProbeClock{}, func(r *restingProbeReport) error {
		if err := encoder.Encode(r); err != nil {
			return err
		}
		return file.Sync()
	})
	return err
}

// validateRestingBuild 把程序实际构建提交与事前登记绑定；未知或带未提交改动的构建拒绝运行。
func validateRestingBuild(plan string, settings []debug.BuildSetting) (string, error) {
	revision, modified := "", "unknown"
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value
		}
	}
	if revision != plan || modified != "false" {
		return "", fmt.Errorf("构建提交与登记不符，或构建含未提交改动；停止执行")
	}
	return revision, nil
}

// validateRestingTarget 本次登记只允许已核对的 SimNow 日盘前置，拒绝误配到其他柜台。
func validateRestingTarget(broker, front string) error {
	if broker != "9999" || front != "tcp://182.254.243.31:30001" {
		return fmt.Errorf("本次探针仅允许登记的 SimNow 9999 日盘第一组前置")
	}
	return nil
}
