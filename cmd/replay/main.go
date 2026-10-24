// Command replay 执行纯离线回放、生成合成演示包、定位两份报告首次差异。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"github.com/dream-until-dawn/futures-position-simulator-go/replay"
	"io"
	"os"
)

func main() {
	code, err := run(os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "失败:", err)
	}
	os.Exit(code)
}

func run(args []string, stdout io.Writer) (int, error) {
	if len(args) == 0 {
		return 1, fmt.Errorf("需要demo、run或diff子命令")
	}
	flags := flag.NewFlagSet("replay "+args[0], flag.ContinueOnError)
	out := flags.String("out", "", "新输出文件（不覆盖已有文件）")
	bundle := flags.String("bundle", "", "自包含JSON回放包")
	left := flags.String("left", "", "左侧报告")
	right := flags.String("right", "", "右侧报告")
	placeholder := flags.Bool("allow-placeholder", false, "显式允许合成占位规则（不提升证据等级）")
	if err := flags.Parse(args[1:]); err != nil {
		return 1, err
	}
	if *out == "" || flags.NArg() != 0 {
		return 1, fmt.Errorf("必须提供-out且不能有多余位置参数")
	}
	switch args[0] {
	case "demo":
		if *bundle != "" || *left != "" || *right != "" || *placeholder {
			return 1, fmt.Errorf("demo只接受-out")
		}
		p, err := replay.Demo()
		if err != nil {
			return 1, err
		}
		raw, err := replay.Encode(p)
		if err != nil {
			return 1, err
		}
		if err = replay.WriteFile(*out, raw); err != nil {
			return 1, err
		}
	case "run":
		if *bundle == "" || *left != "" || *right != "" {
			return 1, fmt.Errorf("run要求-bundle且不能带diff参数")
		}
		f, err := os.Open(*bundle)
		if err != nil {
			return 1, err
		}
		p, err := replay.Decode(f)
		_ = f.Close()
		if err != nil {
			return 1, err
		}
		report, runErr := replay.Run(p, replay.Options{AllowPlaceholder: *placeholder})
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return 1, err
		}
		if err = replay.WriteFile(*out, append(raw, '\n')); err != nil {
			return 1, err
		}
		if runErr != nil {
			return 1, fmt.Errorf("回放失败，有效前缀已标记failed: %w", runErr)
		}
	case "diff":
		if *left == "" || *right == "" || *bundle != "" || *placeholder {
			return 1, fmt.Errorf("diff要求-left和-right")
		}
		read := func(path string) (replay.Report, error) {
			f, err := os.Open(path)
			if err != nil {
				return replay.Report{}, err
			}
			defer f.Close()
			return replay.ReadReport(f)
		}
		a, err := read(*left)
		if err != nil {
			return 1, err
		}
		b, err := read(*right)
		if err != nil {
			return 1, err
		}
		result, err := replay.Compare(a, b)
		if err != nil {
			return 1, err
		}
		raw, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return 1, err
		}
		if err = replay.WriteFile(*out, append(raw, '\n')); err != nil {
			return 1, err
		}
		if !result.Equal {
			return 2, nil
		}
	default:
		return 1, fmt.Errorf("未知子命令: %s", args[0])
	}
	fmt.Fprintln(stdout, "离线处理完成，输出:", *out)
	return 0, nil
}
