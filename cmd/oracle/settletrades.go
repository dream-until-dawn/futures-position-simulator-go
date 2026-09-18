package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

// asciiLines 把结算单正文（GBK）切成行，每行只留 ASCII（非 ASCII 的一段换成一个空格），
// 并**整行丢掉**含 forbidden 任一子串的行（投资者代码等）。纯函数。
//
// ⚠️ 只留 ASCII 顺带去掉了抬头里的中文姓名；投资者代码是数字，靠 forbidden 丢整行。
// ⚠️ forbidden 里的空串会让每一行都「含」它 —— 调用方传进来之前要去掉空串（本函数对空串报错，不静默全丢）。
func asciiLines(raw []byte, forbidden []string) ([]string, error) {
	for _, f := range forbidden {
		if f == "" {
			return nil, fmt.Errorf("⚠️ 屏蔽词里有空串 —— 凭据没读到？不往下走")
		}
	}
	var out []string
	for _, ln := range bytes.Split(raw, []byte("\n")) {
		var b strings.Builder
		prevNon := false
		for _, c := range ln {
			if c == '\r' {
				continue
			}
			if c < 0x80 {
				b.WriteByte(c)
				prevNon = false
			} else if !prevNon {
				b.WriteByte(' ')
				prevNon = true
			}
		}
		s := b.String()
		drop := false
		for _, f := range forbidden {
			if strings.Contains(s, f) {
				drop = true
			}
		}
		if !drop {
			out = append(out, s)
		}
	}
	return out, nil
}

var englishWord = regexp.MustCompile(`[A-Za-z]{4,}`)

var legendLine = regexp.MustCompile(`^---[A-Za-z]`)

// tradeColumns 是成交记录里**允许输出**的列（白名单）。⚠️ tradingcode 与 AccountID 两列是投资者代码，不在里面。
// ⚠️ Exchange / B/S / O/C 三列在结算单里是中文，ASCII 过滤后是空串 —— 不取（取了会像「柜台给的是空」）。
var tradeColumns = []string{"Date", "Instrument", "Price", "Lots", "Turnover", "Fee", "Realized P/L"}

// summaryKeys 是结算单抬头里允许取的数（资金摘要，不含任何账户标识）。
var summaryKeys = []string{"Balance B/F", "Balance C/F", "Commission", "Realized P/L", "MTM P/L"}

// settlementSummary 从资金摘要取白名单里的数。纯函数。⚠️ 同一行左右两栏各有一个键，按键名后紧跟的数取。
func settlementSummary(lines []string) map[string]string {
	out := map[string]string{}
	for _, k := range summaryKeys {
		re := regexp.MustCompile(regexp.QuoteMeta(k) + `\s+(-?[0-9][0-9.]*)`)
		for _, l := range lines {
			if m := re.FindStringSubmatch(l); m != nil {
				if _, seen := out[k]; !seen {
					out[k] = m[1]
				}
			}
		}
	}
	return out
}

// tradeRows 从结算单成交记录一节取白名单列。纯函数。raw 行**不**经过 asciiLines 的整行丢弃（数据行本来就带账号列）：
// 按 '|' 切列、按表头名取白名单列，其余列丢掉；输出前逐格断言不含任何屏蔽词。
func tradeRows(raw []byte, forbidden []string) ([]map[string]string, error) {
	for _, f := range forbidden {
		if f == "" {
			return nil, fmt.Errorf("⚠️ 屏蔽词里有空串 —— 凭据没读到？不往下走")
		}
	}
	all, err := asciiLines(raw, []string{"\x00\x01never"})
	if err != nil {
		return nil, err
	}
	start := -1
	for i, l := range all {
		if strings.Contains(l, "Transaction Record") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, fmt.Errorf("结算单里没有 Transaction Record 一节")
	}
	var header []string
	var rows []map[string]string
	for _, l := range all[start+1:] {
		t := strings.TrimSpace(l)
		if legendLine.MatchString(t) || strings.Contains(t, "Position Closed") { // 图例行（---INE ---SHFE …）是一节的尾巴；纯短横线是分隔线，不是
			break
		}
		if !strings.HasPrefix(t, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(t, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		if header == nil {
			if len(cells) > 0 && cells[0] == "Date" {
				header = cells
			}
			continue
		}
		if len(cells) != len(header) || cells[0] == "" || !strings.HasPrefix(cells[0], "20") {
			continue // 合计行、分隔行
		}
		row := map[string]string{}
		for i, h := range header {
			for _, w := range tradeColumns {
				if h == w {
					row[h] = cells[i]
				}
			}
		}
		for _, v := range row {
			for _, f := range forbidden {
				if strings.Contains(v, f) {
					return nil, fmt.Errorf("⚠️ 白名单列里出现了屏蔽词 —— 表头对错列了，整份不输出")
				}
			}
		}
		rows = append(rows, row)
	}
	if header == nil {
		return nil, fmt.Errorf("成交记录一节没找到表头")
	}
	return rows, nil
}

// runCTPSettleTrades 读某个交易日的结算单，只输出白名单部分（F10 补测那 0.010 的线索：结算单里的逐笔手续费）。
//
//	-mode headers   只打印带英文单词的行（章节标题），看结构
//	-mode section   打印 -section 指定的那一节（到下一个空行为止）
//
// ⚠️ 只读；不落盘。结算单抬头的投资者代码所在行整行丢掉，中文姓名随 ASCII 过滤去掉。
func runCTPSettleTrades(args []string) error {
	fs := flag.NewFlagSet("ctp-settle-trades", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	day := fs.String("day", "", "交易日，形如 20260918（⚠️ 无默认值）")
	mode := fs.String("mode", "headers", "headers | trades | section")
	section := fs.String("section", "Transaction Record", "-mode section 时取哪一节（英文标题的子串）")
	timeout := fs.Duration("timeout", 40*time.Second, "超时")
	dump := fs.String("dump", "", "-mode trades 时把白名单列落盘（给 ../../testdata/ctp，实际写进它旁边的 refdata/ctp-settlement-<交易日>.json）")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *day == "" {
		return fmt.Errorf("⚠️ -day 没有默认值")
	}
	if *dump != "" {
		abs, err := ctpDumpDir(*dump)
		if err != nil {
			return err
		}
		*dump = filepath.Join(filepath.Dir(abs), "refdata")
	}
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	c := ctp.New(ctp.Credentials{
		Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID,
		Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode,
	}, func(string, ...any) {}) // ⚠️ 静默：登录日志里不需要任何东西
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}
	raw, err := c.SettlementText(*day, *timeout)
	if err != nil {
		return err
	}
	lines, err := asciiLines(raw, []string{env.CTPUserID, env.CTPPassword, env.CTPAuthCode})
	if err != nil {
		return err
	}
	fmt.Printf("结算单 %s：%d 字节，%d 行（已丢掉含投资者代码的行）\n", *day, len(raw), len(lines))
	switch *mode {
	case "headers":
		for i, l := range lines {
			if englishWord.MatchString(l) {
				fmt.Printf("%4d  %s\n", i, strings.TrimSpace(l))
			}
		}
	case "trades":
		rows, err := tradeRows(raw, []string{env.CTPUserID, env.CTPPassword, env.CTPAuthCode})
		if err != nil {
			return err
		}
		for _, r := range rows {
			var parts []string
			for _, w := range tradeColumns {
				parts = append(parts, w+"="+r[w])
			}
			fmt.Println(strings.Join(parts, "  "))
		}
		fmt.Printf("共 %d 笔\n", len(rows))
		sum := settlementSummary(lines)
		fmt.Printf("摘要 %v\n", sum)
		if *dump != "" {
			forbidden := []string{env.CTPUserID, env.CTPPassword, env.CTPAuthCode}
			doc := map[string]any{"source": "ctp-settle-trades（结算单白名单列）", "trading_day": *day, "summary": sum, "trades": rows}
			b, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return err
			}
			for _, f := range forbidden {
				if strings.Contains(string(b), f) {
					return fmt.Errorf("⚠️ 落盘内容里出现了屏蔽词 —— 整份不写")
				}
			}
			path := filepath.Join(*dump, fmt.Sprintf("ctp-settlement-%s.json", *day))
			if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Printf("落盘 %s\n", path)
		}
	case "section":
		in := false
		for _, l := range lines {
			if !in && strings.Contains(l, *section) {
				in = true
			}
			if in {
				if strings.TrimSpace(l) == "" {
					break
				}
				fmt.Println(l)
			}
		}
		if !in {
			return fmt.Errorf("结算单里没有 %q 这一节", *section)
		}
	default:
		return fmt.Errorf("-mode 只认 headers / section")
	}
	return nil
}
