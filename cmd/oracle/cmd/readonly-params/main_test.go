package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"testing"
	"time"
)

func argsForTest() []string {
	return []string{"-env", "synthetic.env", "-day", "20261103", "-deadline", time.Now().Add(time.Minute).Format(time.RFC3339)}
}
func TestReadonlyCommandRejectsBeforeCredentials(t *testing.T) {
	for _, extra := range [][]string{{"-attempts", "4"}, {"-timeout", "0s"}, {"-deadline", "2000-01-01T00:00:00Z"}, {"-day", "invalid"}, {"-execute"}, {"extra"}} {
		loads, queries := 0, 0
		var out, errout bytes.Buffer
		d := dependencies{out: &out, err: &errout, load: func(string) (probe.Env, error) { loads++; return probe.Env{}, nil }, query: func(context.Context, ctp.Credentials, ctp.ParamsRetryPolicy, func(string, ...any)) (*def.CThostFtdcBrokerTradingParamsField, error) {
			queries++
			return nil, nil
		}}
		args := append(argsForTest(), extra...)
		if code := runWith(args, d); code != 2 || loads != 0 || queries != 0 {
			t.Fatal("无效只读命令仍读取凭据或调用查询")
		}
	}
}
func TestReadonlyCommandWiring(t *testing.T) {
	loads, queries := 0, 0
	var out, errout bytes.Buffer
	d := dependencies{out: &out, err: &errout,
		load: func(path string) (probe.Env, error) {
			loads++
			if path != "synthetic.env" {
				t.Fatal("凭据路径串位")
			}
			return probe.Env{CTPBrokerID: "9999", CTPTdFront: "tcp://182.254.243.31:30001", CTPUserID: "LEAKME"}, nil
		},
		query: func(ctx context.Context, c ctp.Credentials, p ctp.ParamsRetryPolicy, _ func(string, ...any)) (*def.CThostFtdcBrokerTradingParamsField, error) {
			queries++
			deadline, ok := ctx.Deadline()
			if !ok || !deadline.After(time.Now()) || c.BrokerID != "9999" || c.UserID != "LEAKME" || p.Attempts != 3 || p.ExpectedTradingDay != "20261103" {
				t.Fatal("只读接线未绑定策略与凭据")
			}
			return &def.CThostFtdcBrokerTradingParamsField{MarginPriceType: '4', Algorithm: '2', AvailIncludeCloseProfit: '0'}, nil
		},
	}
	if runWith(argsForTest(), d) != 0 || loads != 1 || queries != 1 {
		t.Fatal("只读成功接线失败")
	}
	var row map[string]string
	if json.Unmarshal(out.Bytes(), &row) != nil || row["status"] != "complete" || row["trading_day"] != "20261103" || len(row) != 5 {
		t.Fatal("只读白名单输出错误")
	}
	if bytes.Contains(out.Bytes(), []byte("LEAKME")) || bytes.Contains(errout.Bytes(), []byte("LEAKME")) {
		t.Fatal("只读输出泄漏凭据")
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, errors.New("write failure") }
func TestReadonlyCommandFailureAndPersistence(t *testing.T) {
	for _, tc := range []struct {
		queryErr bool
		nilRow   bool
		writeErr bool
	}{{queryErr: true}, {nilRow: true}, {writeErr: true}} {
		var out, errout bytes.Buffer
		d := dependencies{out: &out, err: &errout, load: func(string) (probe.Env, error) { return probe.Env{}, nil }, query: func(context.Context, ctp.Credentials, ctp.ParamsRetryPolicy, func(string, ...any)) (*def.CThostFtdcBrokerTradingParamsField, error) {
			if tc.queryErr {
				return nil, errors.New("LEAKME")
			}
			if tc.nilRow {
				return nil, nil
			}
			return &def.CThostFtdcBrokerTradingParamsField{}, nil
		}}
		if tc.writeErr {
			d.out = failedWriter{}
		}
		if runWith(argsForTest(), d) != 1 {
			t.Fatal("只读失败或落盘错误被当成功")
		}
		if bytes.Contains(errout.Bytes(), []byte("LEAKME")) {
			t.Fatal("只读底层自由文本公开")
		}
	}
}
