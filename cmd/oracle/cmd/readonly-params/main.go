// readonly-params 是独立只读程序，不导出Client，不含任何报撤单入口。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"io"
	"os"
	"time"
)

type dependencies struct {
	load     func(string) (probe.Env, error)
	query    func(context.Context, ctp.Credentials, ctp.ParamsRetryPolicy, func(string, ...any)) (*def.CThostFtdcBrokerTradingParamsField, error)
	out, err io.Writer
}

func run(args []string) int {
	return runWith(args, dependencies{load: probe.LoadEnv, query: ctp.QueryBrokerParamsReadOnly, out: os.Stdout, err: os.Stderr})
}
func runWith(args []string, d dependencies) int {
	fs := flag.NewFlagSet("readonly-params", flag.ContinueOnError)
	fs.SetOutput(d.err)
	envPath := fs.String("env", "", "既有SimNow凭据文件，必填")
	day := fs.String("day", "", "明确的柜台交易日，必填")
	deadlineText := fs.String("deadline", "", "本轮绝对停止时间RFC3339，必填")
	attempts := fs.Int("attempts", 3, "最多三次，只对确认超时重连")
	timeout := fs.Duration("timeout", 10*time.Second, "每次握手或查询的期限")
	total := fs.Duration("total", 60*time.Second, "本批总期限")
	backoff := fs.Duration("backoff", 5*time.Second, "关闭后退避")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *envPath == "" {
		return 2
	}
	deadline, err := time.Parse(time.RFC3339, *deadlineText)
	p := ctp.ParamsRetryPolicy{Attempts: *attempts, Timeout: *timeout, Backoff: *backoff, Total: *total, ExpectedTradingDay: *day}
	if err != nil || !time.Now().Before(deadline) || p.Validate() != nil {
		return 2
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	env, err := d.load(*envPath)
	if err != nil {
		return 2
	}
	params, err := d.query(ctx, ctp.Credentials{Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID, Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode}, p, func(f string, a ...any) { fmt.Fprintf(d.err, f+"\n", a...) })
	if err != nil || params == nil {
		fmt.Fprintln(d.err, "只读握手或参数查询失败；查看本批私有阶段日志")
		return 1
	}
	if err = json.NewEncoder(d.out).Encode(map[string]string{"status": "complete", "trading_day": *day, "margin_price_type": string(params.MarginPriceType), "algorithm": string(params.Algorithm), "include_close_profit": string(params.AvailIncludeCloseProfit)}); err != nil {
		return 1
	}
	return 0
}
func main() { os.Exit(run(os.Args[1:])) }
