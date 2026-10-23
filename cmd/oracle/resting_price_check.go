package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/shopspring/decimal"
)

type restingPricePreview struct {
	Symbol, TradingDay, NowUTC, QuoteTime, Tick, Multiplier, Last, Ask, PassivePrice, Lower, Upper string
	ReadOnly                                                                                       bool
}

// collectRestingPricePreview 的接口没有报单/撤单方法，可在启动前只读核行情时钟与账户条件。
func collectRestingPricePreview(c restingReadClient, timeout time.Duration, clock probeClock) (*restingPricePreview, error) {
	if timeout <= 0 || timeout > 15*time.Second {
		return nil, fmt.Errorf("只读查询超时必须在15秒以内且为正")
	}
	if _, err := checkRestingEmpty(c, timeout, true); err != nil {
		return nil, err
	}
	spec, err := c.Instrument("SHFE.rb2701", timeout)
	if err != nil {
		return nil, err
	}
	md, err := c.MarketData("SHFE.rb2701", timeout)
	if err != nil {
		return nil, err
	}
	now := clock.Now()
	price, err := passiveProbePrice(md, spec, c.TradingDay(), now)
	if err != nil {
		return nil, err
	}
	if spec.VolumeMultiple != 10 || price.Mul(decimal.NewFromInt(10)).GreaterThan(decimal.NewFromInt(50000)) {
		return nil, fmt.Errorf("乘数或名义金额不符合登记")
	}
	return &restingPricePreview{Symbol: "SHFE.rb2701", TradingDay: c.TradingDay(), NowUTC: now.UTC().Format(time.RFC3339Nano), QuoteTime: ctp.Text(md.UpdateTime[:]), Tick: decimal.NewFromFloat(float64(spec.PriceTick)).String(), Multiplier: "10", Last: decimal.NewFromFloat(float64(md.LastPrice)).String(), Ask: decimal.NewFromFloat(float64(md.AskPrice1)).String(), PassivePrice: price.String(), Lower: decimal.NewFromFloat(float64(md.LowerLimitPrice)).String(), Upper: decimal.NewFromFloat(float64(md.UpperLimitPrice)).String(), ReadOnly: true}, nil
}

func runRestingPriceCheck(envPath string, timeout time.Duration) error {
	if timeout <= 0 || timeout > 15*time.Second {
		return fmt.Errorf("查询超时必须在15秒以内且为正")
	}
	env, err := probe.LoadEnv(envPath)
	if err != nil {
		return err
	}
	if err = validateRestingTarget(env.CTPBrokerID, env.CTPTdFront); err != nil {
		return err
	}
	c := ctp.New(ctp.Credentials{Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID, Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode}, func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) })
	defer c.Close()
	if err = c.Connect(timeout); err != nil {
		return err
	}
	report, err := collectRestingPricePreview(c, timeout, wallProbeClock{})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
