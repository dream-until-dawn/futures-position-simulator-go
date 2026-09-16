package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

// runCTPInst 只读地打印柜台**声明**的合约参数。不下单、不落盘。
//
// ⚠️ 它不落盘是有理由的：判别性的读数必须落盘，而这条命令产出的不是读数，是**查法**——
// 真正会被引用的那个 tick 由 `ctp-reject` 记进拒因语料（带 `tick_source`），
// 在那里它与用它构造出来的那几个码在同一份文件里。这条命令只是让人先看一眼。
//
// ⚠️ 声明 ≠ 行为：同 `ctp-rates` 那一条纪律。`PriceTick` 说的是柜台**认为**的步长，
// 「报一个非整倍数的价会不会被拒」仍要由 `ctp-reject` 去实测。
func runCTPInst(args []string) error {
	fs := flag.NewFlagSet("ctp-inst", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbols := fs.String("symbols", "", "合约，逗号分隔，形如 SHFE.rb2701,GFEX.si2601（⚠️ 无默认值）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if strings.TrimSpace(*symbols) == "" {
		return fmt.Errorf("⚠️ -symbols 没有默认值：要查哪些合约得说出来")
	}
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
	c := ctp.New(ctp.Credentials{
		Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID,
		Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode,
	}, logf)
	c.Valve = ctpValve(env)
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}
	logf("")
	logf("%-16s %14s %10s %12s %12s", "合约", "最小变动价位", "合约乘数", "到期日", "交易所")
	var failed []string
	for _, sym := range strings.Split(*symbols, ",") {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		inst, err := c.Instrument(sym, *timeout)
		if err != nil {
			// ⚠️ 查不到也要逐个说清并计数：一条静静跳过的合约，
			// 与一条「柜台说它没有」的合约，在输出里看起来一样。
			logf("%-16s ⚠️ %v", sym, err)
			failed = append(failed, sym)
			continue
		}
		logf("%-16s %14g %10d %12s %12s", sym,
			float64(inst.PriceTick), int(inst.VolumeMultiple),
			ctp.Text(inst.ExpireDate[:]), ctp.Text(inst.ExchangeID[:]))
	}
	if len(failed) > 0 {
		return fmt.Errorf("⚠️ %d 个合约没查到：%s —— 多半是合约代码写错了（郑商所是 3 位年月，如 MA601）",
			len(failed), strings.Join(failed, " "))
	}
	return nil
}
