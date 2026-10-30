package main

import (
	"errors"
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// ownedOrderClient 只允许本次会话按引用操作，不提供扫描或撤销其他委托的方法。
type ownedOrderClient interface {
	Insert(ctp.OrderReq, time.Duration) (ctp.OrderState, error)
	Order(string) (ctp.OrderState, bool)
	Cancel(string, ctp.OrderReq) error
}

type probeClock interface {
	Now() time.Time
	Sleep(time.Duration)
}
type wallProbeClock struct{}

func (wallProbeClock) Now() time.Time        { return time.Now() }
func (wallProbeClock) Sleep(d time.Duration) { time.Sleep(d) }

// ownedOrderOutcome 不含柜台自由文本，未知终态不能被编码成已撤。
type ownedOrderOutcome struct {
	LocalQueueMillis                                              int64
	Ref, SysID                                                    string
	Status                                                        byte
	ErrorID, Filled                                               int
	QueueObserved, CancelAttempted, Terminal, SubmissionUncertain bool
}

// waitOwnedOrder 最多一手，超时只撤本次引用；撤单途中成交仍须交给调用方按实际持仓收尾。
func waitOwnedOrder(c ownedOrderClient, req ctp.OrderReq, wait, cancelWait, requestTimeout time.Duration, clock probeClock) (ownedOrderOutcome, error) {
	var out ownedOrderOutcome
	var firstQueued time.Time
	if req.Volume != 1 || wait <= 0 || cancelWait <= 0 || requestTimeout <= 0 {
		return out, fmt.Errorf("挂单探针必须一手且全部等待上限为正")
	}
	deadline := clock.Now().Add(wait)
	initial, submitErr := c.Insert(req, requestTimeout)
	out.Ref = initial.OrderRef
	out.SubmissionUncertain = submitErr != nil
	if out.Ref == "" {
		return out, errors.Join(fmt.Errorf("未取得本次委托引用，不能假称已经挂单或已撤"), submitErr)
	}
	observe := func(st ctp.OrderState) error {
		if st.OrderRef != out.Ref {
			return fmt.Errorf("本地委托引用不匹配")
		}
		if st.VolumeTraded < 0 || st.VolumeTraded > 1 {
			return fmt.Errorf("本次一手委托的成交量无效")
		}
		// 一手委托不可能部分成交；身份或成交终态矛盾不能洗成成功。
		if out.SysID != "" && st.OrderSysID != "" && out.SysID != st.OrderSysID {
			return fmt.Errorf("本次委托系统编号发生变化")
		}
		if st.Status == def.THOST_FTDC_OST_AllTraded && st.VolumeTraded != 1 {
			return fmt.Errorf("本次一手全部成交回报缺少成交量")
		}
		if st.Status == def.THOST_FTDC_OST_PartTradedQueueing || st.Status == def.THOST_FTDC_OST_PartTradedNotQueueing {
			return fmt.Errorf("本次一手委托不允许部分成交回报")
		}
		out.Status = st.Status
		out.ErrorID = st.ErrorID
		out.Filled = st.VolumeTraded
		if st.OrderSysID != "" {
			out.SysID = st.OrderSysID
		}
		if st.Alive() && st.VolumeTraded == 0 {
			if !out.QueueObserved {
				firstQueued = clock.Now()
			}
			out.QueueObserved = true
		}
		if st.VolumeTraded == 1 && out.QueueObserved {
			out.LocalQueueMillis = clock.Now().Sub(firstQueued).Milliseconds()
		}
		out.Terminal = st.VolumeTraded == 1 || ownedTerminal(st.Status)
		return nil
	}
	if err := observe(initial); err != nil {
		submitErr = errors.Join(submitErr, err)
		out.SubmissionUncertain = true
	}
	for !out.Terminal && submitErr == nil && clock.Now().Before(deadline) {
		clock.Sleep(250 * time.Millisecond)
		if st, ok := c.Order(out.Ref); ok {
			if err := observe(st); err != nil {
				submitErr = errors.Join(submitErr, err)
				out.SubmissionUncertain = true
				break
			}
		}
	}
	if out.Terminal {
		return out, submitErr
	}
	out.CancelAttempted = true
	cancelErr := c.Cancel(out.Ref, req)
	cancelDeadline := clock.Now().Add(cancelWait)
	for clock.Now().Before(cancelDeadline) {
		if st, ok := c.Order(out.Ref); ok {
			if err := observe(st); err != nil {
				out.SubmissionUncertain = true
				out.Terminal = false
				return out, errors.Join(submitErr, cancelErr, err)
			}
			if out.Terminal {
				return out, errors.Join(submitErr, cancelErr)
			}
		}
		clock.Sleep(250 * time.Millisecond)
	}
	return out, errors.Join(submitErr, cancelErr, fmt.Errorf("本次委托 %s 撤单后终态未知，不能继续发单（撤单调用错误：%v）", out.Ref, cancelErr))
}

func ownedTerminal(status byte) bool {
	switch status {
	case def.THOST_FTDC_OST_AllTraded, def.THOST_FTDC_OST_PartTradedNotQueueing, def.THOST_FTDC_OST_NoTradeNotQueueing, def.THOST_FTDC_OST_Canceled:
		return true
	}
	return false
}
