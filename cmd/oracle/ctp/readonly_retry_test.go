package ctp

import (
	"context"
	"errors"
	def "gitee.com/haifengat/goctp/ctpdefine"
	"testing"
	"time"
)

type retryFixture struct {
	now                        time.Time
	clients                    []*retryClient
	connects                   []error
	queryErr                   error
	queries, closed, pauses    int
	day                        string
	row                        *def.CThostFtdcBrokerTradingParamsField
	closeAdvance, queryAdvance time.Duration
}
type retryClient struct {
	f      *retryFixture
	err    error
	closed bool
}

func (c *retryClient) Connect(time.Duration) error { return c.err }
func (c *retryClient) TradingDay() string          { return c.f.day }
func (c *retryClient) BrokerParams(time.Duration) (*def.CThostFtdcBrokerTradingParamsField, error) {
	c.f.queries++
	c.f.now = c.f.now.Add(c.f.queryAdvance)
	return c.f.row, c.f.queryErr
}
func (c *retryClient) Close() { c.closed = true; c.f.closed++; c.f.now = c.f.now.Add(c.f.closeAdvance) }
func newRetryFixture() *retryFixture {
	return &retryFixture{now: time.Now(), day: "20261103", row: &def.CThostFtdcBrokerTradingParamsField{MarginPriceType: '1', Algorithm: '2', AvailIncludeCloseProfit: '0'}}
}
func (f *retryFixture) runtime(t *testing.T) paramsRetryRuntime {
	return paramsRetryRuntime{
		now: func() time.Time { return f.now },
		create: func() readonlyParamsClient {
			for _, c := range f.clients {
				if !c.closed {
					t.Fatal("旧连接未关闭就重连")
				}
			}
			c := &retryClient{f: f}
			if len(f.clients) < len(f.connects) {
				c.err = f.connects[len(f.clients)]
			}
			f.clients = append(f.clients, c)
			return c
		},
		pause: func(ctx context.Context, d time.Duration) error { f.pauses++; f.now = f.now.Add(d); return ctx.Err() },
	}
}
func retryPolicy() ParamsRetryPolicy {
	return ParamsRetryPolicy{Attempts: 3, Timeout: 10 * time.Second, Backoff: 5 * time.Second, Total: 90 * time.Second, ExpectedTradingDay: "20261103"}
}
func retryCredentials() Credentials {
	return Credentials{BrokerID: "9999", Front: "tcp://182.254.243.31:30001", UserID: "fake-user", Password: "fake-password", AppID: "fake-app", AuthCode: "fake-auth"}
}
func confirmTimeout() error { return &handshakeFailure{stage: handshakeConfirm, kind: "timeout"} }

func TestReadOnlyParamsRetryRecovery(t *testing.T) {
	f := newRetryFixture()
	f.connects = []error{confirmTimeout(), confirmTimeout(), nil}
	got, err := queryBrokerParamsReadOnly(context.Background(), retryCredentials(), retryPolicy(), f.runtime(t), nil)
	if err != nil || got == nil || len(f.clients) != 3 || f.queries != 1 || f.closed != 3 || f.pauses != 2 {
		t.Fatal("确认超时未有界恢复", err)
	}
}
func TestReadOnlyParamsRetryExhausted(t *testing.T) {
	f := newRetryFixture()
	f.connects = []error{confirmTimeout(), confirmTimeout(), confirmTimeout(), nil}
	got, err := queryBrokerParamsReadOnly(context.Background(), retryCredentials(), retryPolicy(), f.runtime(t), nil)
	if err == nil || got != nil || len(f.clients) != 3 || f.queries != 0 || f.closed != 3 {
		t.Fatal("重试耗尽仍继续或返回成功")
	}
}
func TestReadOnlyParamsRetryFailureKinds(t *testing.T) {
	for _, failure := range []error{errors.New("unknown"), &handshakeFailure{stage: handshakeAuth, kind: "timeout"}, &handshakeFailure{stage: handshakeConfirm, kind: "response-error"}, &handshakeFailure{stage: handshakeConfirm, kind: "send-rejected"}} {
		f := newRetryFixture()
		f.connects = []error{failure, nil}
		_, err := queryBrokerParamsReadOnly(context.Background(), retryCredentials(), retryPolicy(), f.runtime(t), nil)
		if err == nil || len(f.clients) != 1 || f.closed != 1 {
			t.Fatal("非确认超时被自动重试")
		}
	}
}
func TestReadOnlyParamsRetryNeverRetriesQuery(t *testing.T) {
	f := newRetryFixture()
	f.queryErr = confirmTimeout()
	_, err := queryBrokerParamsReadOnly(context.Background(), retryCredentials(), retryPolicy(), f.runtime(t), nil)
	if err == nil || len(f.clients) != 1 || f.queries != 1 || f.closed != 1 {
		t.Fatal("参数查询失败被自动重试")
	}
}
func TestReadOnlyParamsRetryDayAndValues(t *testing.T) {
	for _, change := range []func(*retryFixture){func(f *retryFixture) { f.day = "20261104" }, func(f *retryFixture) { f.row = nil }, func(f *retryFixture) { f.row.Algorithm = 0 }, func(f *retryFixture) { f.row.MarginPriceType = '9' }, func(f *retryFixture) { f.row.AvailIncludeCloseProfit = '1' }} {
		f := newRetryFixture()
		change(f)
		_, err := queryBrokerParamsReadOnly(context.Background(), retryCredentials(), retryPolicy(), f.runtime(t), nil)
		if err == nil || len(f.clients) != 1 || f.closed != 1 {
			t.Fatal("错日或未知参数被接受")
		}
	}
}
func TestReadOnlyParamsRetryPolicy(t *testing.T) {
	for _, change := range []func(*ParamsRetryPolicy, *Credentials){
		func(p *ParamsRetryPolicy, _ *Credentials) { p.Attempts = 0 }, func(p *ParamsRetryPolicy, _ *Credentials) { p.Attempts = 4 },
		func(p *ParamsRetryPolicy, _ *Credentials) { p.Timeout = 0 }, func(p *ParamsRetryPolicy, _ *Credentials) { p.Timeout = 61 * time.Second },
		func(p *ParamsRetryPolicy, _ *Credentials) { p.Backoff = 0 }, func(p *ParamsRetryPolicy, _ *Credentials) { p.Backoff = 31 * time.Second },
		func(p *ParamsRetryPolicy, _ *Credentials) { p.Total = 0 }, func(p *ParamsRetryPolicy, _ *Credentials) { p.Total = 181 * time.Second },
		func(p *ParamsRetryPolicy, _ *Credentials) { p.ExpectedTradingDay = "20261340" },
		func(_ *ParamsRetryPolicy, c *Credentials) { c.BrokerID = "other" }, func(_ *ParamsRetryPolicy, c *Credentials) { c.Front = "tcp://other" },
		func(_ *ParamsRetryPolicy, c *Credentials) { c.UserID = "" }, func(_ *ParamsRetryPolicy, c *Credentials) { c.Password = "" }, func(_ *ParamsRetryPolicy, c *Credentials) { c.AppID = "" }, func(_ *ParamsRetryPolicy, c *Credentials) { c.AuthCode = "" },
	} {
		f := newRetryFixture()
		p, c := retryPolicy(), retryCredentials()
		change(&p, &c)
		_, err := queryBrokerParamsReadOnly(context.Background(), c, p, f.runtime(t), nil)
		if err == nil || len(f.clients) != 0 {
			t.Fatal("无效策略或目标仍创建连接")
		}
	}
}
func TestReadOnlyParamsRetryCancellationAndDeadline(t *testing.T) {
	f := newRetryFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := queryBrokerParamsReadOnly(ctx, retryCredentials(), retryPolicy(), f.runtime(t), nil); err == nil || len(f.clients) != 0 {
		t.Fatal("取消后仍创建连接")
	}
	f = newRetryFixture()
	f.connects = []error{confirmTimeout(), nil}
	f.closeAdvance = 90 * time.Second
	if _, err := queryBrokerParamsReadOnly(context.Background(), retryCredentials(), retryPolicy(), f.runtime(t), nil); err == nil || len(f.clients) != 1 {
		t.Fatal("关闭跨截止仍重试")
	}
	f = newRetryFixture()
	f.connects = []error{confirmTimeout(), nil}
	p := retryPolicy()
	p.Total = 5 * time.Second
	if _, err := queryBrokerParamsReadOnly(context.Background(), retryCredentials(), p, f.runtime(t), nil); err == nil || f.pauses != 0 {
		t.Fatal("退避跨截止仍等待")
	}
	f = newRetryFixture()
	f.queryAdvance = 91 * time.Second
	if _, err := queryBrokerParamsReadOnly(context.Background(), retryCredentials(), retryPolicy(), f.runtime(t), nil); err == nil {
		t.Fatal("查询越截止仍成功")
	}
}
