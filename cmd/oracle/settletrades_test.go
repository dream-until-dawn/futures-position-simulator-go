package main

import (
	"reflect"
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
