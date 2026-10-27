// Command offline-replay 演示第三方模块只用导出API执行纯离线跨日重放。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"github.com/dream-until-dawn/futures-position-simulator-go/replay"
	"os"
)

func demonstrate(invalid bool) (replay.Report, error) {
	payload, err := replay.Demo()
	if err != nil {
		return replay.Report{}, err
	}
	if invalid {
		payload.Settings.Choices.Algorithm = 0
	}
	packet, err := replay.Encode(payload)
	if err != nil {
		return replay.Report{}, err
	}
	decoded, err := replay.Decode(bytes.NewReader(packet))
	if err != nil {
		return replay.Report{}, err
	}
	// 明确签字允许完全合成的placeholder；接入真实数据时不得悄悄沿用此选项。
	return replay.Run(decoded, replay.Options{AllowPlaceholder: true})
}

func main() {
	scenario := flag.String("scenario", "replay", "replay、lifecycle或frozen，全部使用合成数据")
	invalid := flag.Bool("invalid", false, "执行明确失败的接入反例")
	flag.Parse()
	if *scenario == "frozen" {
		book, known, err := demonstrateFrozen(*invalid)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("known=%v orders=%d sequence=%d\n", known, len(book.Live()), book.LastSequence())
		return
	}
	var report replay.Report
	var err error
	switch *scenario {
	case "replay":
		report, err = demonstrate(*invalid)
	case "lifecycle":
		report, err = demonstrateLifecycle(*invalid)
	default:
		err = fmt.Errorf("未知示例: %s", *scenario)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("status=%s events=%d balance=%s evidence=%s\n", report.Status, len(report.Frames), report.Final.Account.PreBalance, report.Evidence)
	if *scenario == "lifecycle" {
		fmt.Printf("rules_version=%d required_deposit=%s\n", report.Final.RulesVersion, report.Frames[len(report.Frames)-1].Risk.RequiredDeposit)
	}
}
