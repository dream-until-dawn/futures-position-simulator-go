package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestASCIILinesDropsForbiddenAndNonASCII：含投资者代码的行整行丢掉；中文（GBK 高位字节）换成一个空格；空屏蔽词报错。
func TestASCIILinesDropsForbiddenAndNonASCII(t *testing.T) {
	raw := []byte("Client ID 1234567\r\n\xd0\xd5\xc3\xfb Name\r\nTransaction Record\r\n20260918 DCE j2701 23.59\r\n")
	got, err := asciiLines(raw, []string{"1234567"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"  Name", "Transaction Record", "20260918 DCE j2701 23.59", ""}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("得到 %q，期望 %q", got, want)
	}
	for _, l := range got {
		if l == "Client ID 1234567" {
			t.Error("⚠️ 含投资者代码的行没被丢掉")
		}
	}
	if _, err := asciiLines(raw, []string{"1234567", ""}); err == nil {
		t.Error("⚠️ 屏蔽词里有空串时应当报错（否则每一行都「含」它、被全部丢掉而不说话）")
	}
}

// TestTradeRowsTakesOnlyWhitelistedColumns：成交记录的数据行带账号列（tradingcode / AccountID），白名单列里不许出现；
// 表头对错了列（账号落进白名单列）时整份报错、一行都不给。
func TestTradeRowsTakesOnlyWhitelistedColumns(t *testing.T) {
	raw := []byte("Client ID 1234567\r\nTransaction Record\r\n-----\r\n" +
		"|  Date  |tradingcode|   Instrument   |   Price  | Lots |  Turnover  |   Fee    |Realized P/L|   AccountID  |\r\n" +
		"|20260918|1234567|j2701|1965.000|2|393000.00|23.58|0.00|1234567|\r\n" +
		"---INE   ---SHFE\r\n")
	rows, err := tradeRows(raw, []string{"1234567"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["Fee"] != "23.58" || rows[0]["Instrument"] != "j2701" || rows[0]["Lots"] != "2" {
		t.Fatalf("得到 %v", rows)
	}
	for k, v := range rows[0] {
		if v == "1234567" || k == "AccountID" || k == "tradingcode" {
			t.Errorf("⚠️ 白名单之外的列进来了：%s=%s", k, v)
		}
	}
	bad := []byte("Transaction Record\r\n|  Date  |   Fee    |\r\n|20260918|1234567|\r\n")
	if _, err := tradeRows(bad, []string{"1234567"}); err == nil {
		t.Error("⚠️ 账号落进了白名单列（Fee），应当整份报错")
	}
}

// TestGBKCellsDoesNotSplitInsideADoubleByteChar：「亅」的 GBK 是 0x81 0x7C —— 第二个字节就是 '|'，
// 直接按 0x7C 切会把它劈成两半、多出一格。
func TestGBKCellsDoesNotSplitInsideADoubleByteChar(t *testing.T) {
	got := gbkCells([]byte("a|\x81\x7c|b"))
	if len(got) != 3 || string(got[1]) != "\x81\x7c" {
		t.Errorf("得到 %q，期望三格、中间一格是完整的「亅」", got)
	}
}

// TestTradeRowsMapsChineseColumnsByVocab：交易所 / 买卖 / 投保 / 开平四列按固定词表映射；词表外的取值报错。
func TestTradeRowsMapsChineseColumnsByVocab(t *testing.T) {
	head := "Transaction Record\r\n|  Date  |Exchange|   Instrument   | B/S |    S/H     |   Price  | Lots |  Turnover  |       O/C        |   Fee    |\r\n"
	row := "|20260918|\xb4\xf3\xc9\xcc\xcb\xf9|j2701|\xc2\xf4|\xcd\xb6\xbb\xfa|1964.000|2|392800.00|\xc6\xbd\xbd\xf1|23.57|\r\n"
	rows, err := tradeRows([]byte(head+row+"---INE\r\n"), []string{"1234567"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Exchange": "DCE", "B/S": "Sell", "S/H": "Speculation", "O/C": "CloseToday", "Fee": "23.57"}
	for k, v := range want {
		if len(rows) != 1 || rows[0][k] != v {
			t.Errorf("%s：得到 %v，期望 %s", k, rows, v)
		}
	}
	unknown := "|20260918|\xc4\xdc\xd4\xb4|j2701|\xc2\xf4|\xcd\xb6\xbb\xfa|1964.000|2|392800.00|\xc6\xbd|23.57|\r\n" // 「能源」不在词表里
	if _, err := tradeRows([]byte(head+unknown), []string{"1234567"}); err == nil || !strings.Contains(err.Error(), "词表外") {
		t.Errorf("⚠️ 词表外的交易所取值应当报错，得到 %v", err)
	}
}
