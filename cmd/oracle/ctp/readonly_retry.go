package ctp

import (
	"context"
	"errors"
	"fmt"
	def "gitee.com/haifengat/goctp/ctpdefine"
	"strings"
	"time"
)

// ParamsRetryPolicy 只用于SimNow经纪商参数查询，不适用于任何报撤单或资金操作。
type ParamsRetryPolicy struct {
	Attempts                int
	Timeout, Backoff, Total time.Duration
	ExpectedTradingDay      string
}

// readonlyParamsClient 故意不包含报单/撤单/资金接口。
type readonlyParamsClient interface {
	Connect(time.Duration) error
	TradingDay() string
	BrokerParams(time.Duration) (*def.CThostFtdcBrokerTradingParamsField, error)
	Close()
}
type paramsRetryRuntime struct {
	create func() readonlyParamsClient
	now    func() time.Time
	pause  func(context.Context, time.Duration) error
}

// QueryBrokerParamsReadOnly 显式有界只读重连：仅确认超时可重试，旧连接Close返回后才建新连接。
// 不复用失败Client，不重试认证拒绝/查询失败/未知状态，不返回可用于交易的Client。
// SDK原生调用或Close若阻塞，调用者须将进程状态视为未知，不能另启替代连接。
func QueryBrokerParamsReadOnly(ctx context.Context, cred Credentials, p ParamsRetryPolicy, logf func(string, ...any)) (*def.CThostFtdcBrokerTradingParamsField, error) {
	return queryBrokerParamsReadOnly(ctx, cred, p, paramsRetryRuntime{
		create: func() readonlyParamsClient { return New(cred, logf) }, now: time.Now,
		pause: func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
	}, logf)
}

// Validate 允许命令入口在读取凭据前拒绝无效策略。
func (p ParamsRetryPolicy) Validate() error {
	if p.Attempts < 1 || p.Attempts > 3 || p.Timeout <= 0 || p.Timeout > 60*time.Second || p.Backoff < 5*time.Second || p.Backoff > 30*time.Second || p.Total <= 0 || p.Total > 180*time.Second {
		return fmt.Errorf("只读重试策略无效")
	}
	if _, err := time.Parse("20060102", p.ExpectedTradingDay); err != nil {
		return fmt.Errorf("只读重试需要明确交易日")
	}
	return nil
}
func queryBrokerParamsReadOnly(ctx context.Context, cred Credentials, p ParamsRetryPolicy, r paramsRetryRuntime, logf func(string, ...any)) (*def.CThostFtdcBrokerTradingParamsField, error) {
	if ctx == nil {
		return nil, fmt.Errorf("只读重试缺少上下文")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if cred.BrokerID != "9999" || cred.Front != "tcp://182.254.243.31:30001" {
		return nil, fmt.Errorf("只读重试只允许登记SimNow前置")
	}
	if cred.UserID == "" || cred.Password == "" || cred.AppID == "" || cred.AuthCode == "" {
		return nil, fmt.Errorf("只读重试凭据缺失")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	deadline := r.now().Add(p.Total)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	remaining := func() (time.Duration, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		left := deadline.Sub(r.now())
		if left <= 0 {
			return 0, context.DeadlineExceeded
		}
		return min(left, p.Timeout), nil
	}
	var history []error
	for attempt := 1; attempt <= p.Attempts; attempt++ {
		budget, err := remaining()
		if err != nil {
			return nil, errors.Join(append(history, err)...)
		}
		client := r.create()
		logf("[ctp.readonly.retry] attempt=%d event=begin", attempt)
		err = client.Connect(budget)
		if err != nil {
			client.Close() // 必须先完成关闭；失败或阻塞不能再建连接。
			history = append(history, err)
			failure, known := err.(*handshakeFailure)
			retry := known && failure.stage == handshakeConfirm && failure.kind == "timeout"
			logf("[ctp.readonly.retry] attempt=%d event=connect-failed-closed retryable=%t", attempt, retry)
			if !retry || attempt == p.Attempts {
				return nil, errors.Join(history...)
			}
			if _, err = remaining(); err != nil {
				return nil, errors.Join(append(history, err)...)
			}
			if deadline.Sub(r.now()) <= p.Backoff {
				return nil, errors.Join(append(history, context.DeadlineExceeded)...)
			}
			logf("[ctp.readonly.retry] attempt=%d event=closed-before-backoff", attempt)
			if err = r.pause(ctx, p.Backoff); err != nil {
				return nil, errors.Join(append(history, err)...)
			}
			continue
		}
		if client.TradingDay() != p.ExpectedTradingDay {
			client.Close()
			return nil, fmt.Errorf("只读重试真实交易日不符")
		}
		budget, err = remaining()
		if err != nil {
			client.Close()
			return nil, errors.Join(append(history, err)...)
		}
		params, err := client.BrokerParams(budget)
		client.Close()
		if err != nil {
			return nil, errors.Join(append(history, err)...)
		} // 查询失败不重连，不能伪装成握手恢复。
		if params == nil || !strings.ContainsRune("1234", rune(params.MarginPriceType)) || !strings.ContainsRune("1234", rune(params.Algorithm)) || !strings.ContainsRune("02", rune(params.AvailIncludeCloseProfit)) {
			return nil, fmt.Errorf("只读重试参数声明未知")
		}
		if _, err = remaining(); err != nil {
			return nil, errors.Join(append(history, err)...)
		}
		logf("[ctp.readonly.retry] attempt=%d event=complete", attempt)
		return params, nil
	}
	return nil, fmt.Errorf("只读重试未产生完整结果")
}
