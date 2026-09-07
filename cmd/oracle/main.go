// Command oracle 是本项目的「真值来源」工具。
//
// oracle = test oracle，**判定真值的那一方**，不是数据库。
//
// 它是一个**独立嵌套模块**（自带 go.mod）：连柜台要 WebSocket 客户端与 CTP 绑定，
// 后者还自带数十 MB 二进制。若把它们写进主模块，即便使用者从不引用本工具，
// 依赖仍会出现在他们的模块图里——而这件事**不会有任何报错**，
// 是使用者 `go get` 之后才在自己的模块图里看见。
//
// 两个子命令：
//
//	oracle probe        跑判别实验，产出证据
//	oracle conformance  逐字段对拍（v0.2.0 起）
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

func usage() {
	fmt.Fprint(os.Stderr, `oracle —— 判定真值的那一方

用法:
  oracle probe -exp <名称> [-symbols a,b] [-env 路径]
  oracle whitelist                 打印脱敏白名单，供评审逐键核对
  oracle status                    只读：登录并打印账户与持仓截面

实验名称:
  status          只读连通性自检（做到协议层登录，不是 TCP 层）
  margin-price    实验 1：盘中保证金用哪个价
  profit-price    实验 2：盘中持仓盈亏用哪个价
  max-margin-side 实验 3：单向大边按品种还是按合约合并
  close-order     实验 4：NoUseHistory 合约的平仓消耗顺序
  fee-rounding    实验 5：手续费的取整口径
  reject-code     实验 6：报单被拒的错误码
`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "whitelist":
		fmt.Print(kq.WhitelistReport())
	case "probe", "status":
		if err := runProbe(os.Args); err != nil {
			fmt.Fprintln(os.Stderr, "失败:", err)
			os.Exit(1)
		}
	case "conformance":
		fmt.Fprintln(os.Stderr, "conformance 从 v0.2.0 起提供，现在还没有实现可对拍")
		os.Exit(2)
	default:
		usage()
		os.Exit(2)
	}
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet(args[1], flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	exp := fs.String("exp", "status", "实验名称")
	symbols := fs.String("symbols", "", "实验用合约，逗号分隔，形如 SHFE.rb2601")
	dump := fs.String("dump", "testdata/probes", "夹具落盘目录")
	timeout := fs.Duration("timeout", 90*time.Second, "整体超时")
	if args[1] == "status" {
		_ = fs.Parse(args[2:])
		*exp = "status"
	} else {
		_ = fs.Parse(args[2:])
	}

	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var syms []string
	for _, s := range strings.Split(*symbols, ",") {
		if s = strings.TrimSpace(s); s != "" {
			syms = append(syms, s)
		}
	}

	r := &probe.Runner{
		Env:     env,
		Symbols: syms,
		DumpDir: *dump,
		Logf:    func(f string, a ...any) { fmt.Printf(f+"\n", a...) },
	}
	return r.Run(ctx, *exp)
}
