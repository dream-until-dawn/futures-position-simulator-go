package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
)

// readRestingReport 检查逐阶段顺序和不可变身份，拒绝只拼一条最终结论冒充事前记录。
func readRestingReport(reader io.Reader) (restingProbeReport, error) {
	var first, last restingProbeReport
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	ranks := map[string]int{"checking-empty-account": 1, "control-before-insert": 2, "control-after-order": 3, "main-before-insert": 4, "main-after-order": 5, "main-trades-observed": 6, "closing-owned-long": 7, "finished": 8}
	count, previous := 0, 0
	seen := make(map[string]bool)
	beforePrice := ""
	for {
		var row restingProbeReport
		err := decoder.Decode(&row)
		if err == io.EOF {
			break
		}
		if err != nil {
			return last, fmt.Errorf("报告不完整或字段无效：%w", err)
		}
		count++
		if count > 100 {
			return last, fmt.Errorf("报告阶段超过上限")
		}
		if count == 1 {
			first = row
		}
		if row.Format != 1 || row.PlanCommit != first.PlanCommit || row.BuildCommit != first.BuildCommit || row.Symbol != first.Symbol || row.TradingDay != first.TradingDay || row.StartedUTC != first.StartedUTC {
			return last, fmt.Errorf("报告阶段身份或计划发生变化")
		}
		rank := ranks[row.Stage]
		if rank == 0 || rank <= previous {
			return last, fmt.Errorf("报告阶段顺序无效")
		}
		previous = rank
		seen[row.Stage] = true
		if row.Stage == "main-before-insert" {
			if row.Main.Ref != "" || row.Main.Filled != 0 || len(row.MainTrades) != 0 || row.OrderPrice == "" {
				return last, fmt.Errorf("挂单前记录已含成交或缺失预言价格")
			}
			beforePrice = row.OrderPrice
		}
		last = row
	}
	if count == 0 || beforePrice == "" || last.OrderPrice != beforePrice {
		return last, fmt.Errorf("缺少固定的挂单前价格记录")
	}
	for _, stage := range []string{"checking-empty-account", "control-before-insert", "control-after-order", "main-before-insert", "main-after-order", "finished"} {
		if !seen[stage] {
			return last, fmt.Errorf("缺少必要阶段 %s", stage)
		}
	}
	if last.Main.Filled == 1 && (!seen["main-trades-observed"] || !seen["closing-owned-long"]) {
		return last, fmt.Errorf("缺少成交观察或收尾阶段")
	}
	if _, err := verifyRestingReport(last); err != nil {
		return last, err
	}
	return last, nil
}

// runRestingVerify 仅离线核验白名单JSONL，不连接柜台，不读取凭据。
func runRestingVerify(args []string) error {
	fs := flag.NewFlagSet("resting-verify", flag.ContinueOnError)
	filePath := fs.String("file", "", "本机JSONL报告路径")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *filePath == "" || fs.NArg() != 0 {
		return fmt.Errorf("必须给-file报告路径")
	}
	file, err := os.Open(*filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	const limit = 1 << 20
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return err
	}
	if len(raw) > limit {
		return fmt.Errorf("报告超过一MiB上限")
	}
	report, err := readRestingReport(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	fmt.Printf("报告离线核验通过：%s；终态、收尾与成交字段一致（未连接柜台）\n", report.Verdict)
	return nil
}
