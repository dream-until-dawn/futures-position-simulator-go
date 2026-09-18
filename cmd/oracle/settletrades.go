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

// gbkCells 把结算单的一行（GBK 原始字节）按 '|' 切成格。纯函数。
//
// ⚠️ 不能直接按 0x7C 切：GBK 双字节字符的第二个字节范围是 0x40–0xFE，**包括 0x7C**。
// 所以先按 GBK 规则成对读（首字节 0x81–0xFE 就连下一个字节一起吃掉），只有落在单字节位置上的 0x7C 才是分隔符。
func gbkCells(line []byte) [][]byte {
	var cells [][]byte
	var cur []byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c >= 0x81 && c <= 0xFE && i+1 < len(line) {
			cur = append(cur, c, line[i+1])
			i++
			continue
		}
		if c == '|' {
			cells = append(cells, bytes.TrimSpace(cur))
			cur = nil
			continue
		}
		cur = append(cur, c)
	}
	return append(cells, bytes.TrimSpace(cur))
}

var legendLine = regexp.MustCompile(`^---[A-Za-z]`)

// tradeColumns 是成交记录里**允许输出**的列（白名单）。⚠️ tradingcode 与 AccountID 两列是投资者代码，不在里面。
// tradeColumns 是成交记录里**允许输出**的列（白名单）。⚠️ tradingcode 与 AccountID 两列是投资者代码，不在里面。
// Exchange / B/S / S/H / O/C 四列是中文：按 settleVocab 的固定词表映射成代码，不认识的值报错（评审 20260918：不整列丢，也不带进自由文本）。
var tradeColumns = []string{"Date", "Exchange", "Instrument", "B/S", "S/H", "Price", "Lots", "Turnover", "O/C", "Fee", "Realized P/L"}

// settleVocab 是结算单四列中文取值（GBK 字节）→ 代码的固定词表。只收**实际在结算单里出现过**的
// （20260911 / 14 / 15 / 17 / 18 五份）；能源中心、中金所、广期所没见过，不猜它们在结算单上怎么写。
var settleVocab = map[string]map[string]string{
	"Exchange": {"\xb4\xf3\xc9\xcc\xcb\xf9": "DCE", "\xc9\xcf\xc6\xda\xcb\xf9": "SHFE", "\xd6\xa3\xc9\xcc\xcb\xf9": "CZCE"},       // 大商所 上期所 郑商所
	"B/S":      {"\xc2\xf2": "Buy", "\xc2\xf4": "Sell"},                                                                           // 买 卖
	"S/H":      {"\xcd\xb6\xbb\xfa": "Speculation", "\xd2\xbb\xb0\xe3": "General"},                                                // 投机 一般
	"O/C":      {"\xbf\xaa": "Open", "\xc6\xbd": "Close", "\xc6\xbd\xbd\xf1": "CloseToday", "\xc6\xbd\xd7\xf2": "CloseYesterday"}, // 开 平 平今 平昨
}

// tradeRows 从结算单成交记录一节取白名单列。纯函数。直接在 GBK 原始字节上按格切（gbkCells），按表头名取列：
// 英文 / 数字列只留 ASCII；四列中文按 settleVocab 映射，不认识就报错；输出前逐格断言不含任何屏蔽词。
func tradeRows(raw []byte, forbidden []string) ([]map[string]string, error) {
	for _, f := range forbidden {
		if f == "" {
			return nil, fmt.Errorf("⚠️ 屏蔽词里有空串 —— 凭据没读到？不往下走")
		}
	}
	var header []string
	var rows []map[string]string
	in := false
	for _, ln := range bytes.Split(raw, []byte("\n")) {
		ln = bytes.TrimRight(ln, "\r")
		t := bytes.TrimSpace(ln)
		if !in {
			in = bytes.Contains(ln, []byte("Transaction Record"))
			continue
		}
		if legendLine.Match(t) || bytes.Contains(t, []byte("Position Closed")) { // 图例行（---INE ---SHFE …）是一节的尾巴；纯短横线是分隔线，不是
			break
		}
		if !bytes.HasPrefix(t, []byte("|")) {
			continue
		}
		cells := gbkCells(bytes.Trim(t, "|"))
		if header == nil {
			if len(cells) > 0 && string(cells[0]) == "Date" {
				for _, c := range cells {
					header = append(header, string(c))
				}
			}
			continue
		}
		if len(cells) != len(header) || !bytes.HasPrefix(cells[0], []byte("20")) {
			continue // 合计行、分隔行
		}
		row := map[string]string{}
		for i, h := range header {
			for _, w := range tradeColumns {
				if h != w {
					continue
				}
				if vocab, ok := settleVocab[h]; ok {
					code, known := vocab[string(cells[i])]
					if !known {
						return nil, fmt.Errorf("⚠️ 成交记录 %s 列有词表外的取值（%x）—— 不猜，先把它加进 settleVocab", h, cells[i])
					}
					row[h] = code
					continue
				}
				for _, c := range cells[i] {
					if c >= 0x80 {
						return nil, fmt.Errorf("⚠️ 成交记录 %s 列出现了非 ASCII 字节 —— 这一列应当是数字或代码", h)
					}
				}
				row[h] = string(cells[i])
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
	if !in {
		return nil, fmt.Errorf("结算单里没有 Transaction Record 一节")
	}
	if header == nil {
		return nil, fmt.Errorf("成交记录一节没找到表头")
	}
	return rows, nil
}

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

// runCTPSettleTrades 读某个交易日的结算单，只输出白名单部分（F10 补测那 0.010 的线索：结算单里的逐笔手续费）。
//
//	-mode headers   只打印带英文单词的行（章节标题），看结构
//	-mode trades    成交记录的白名单列（四列中文按词表映射）；-dump 落盘
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
