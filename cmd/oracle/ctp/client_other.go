//go:build !windows

package ctp

import (
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
)

// ⚠️ 非 Windows 上本包**没有实现**，而不是「静默不可用」。
//
// CTP 在 Linux 侧是 .so + cgo，形状与 Windows 的 syscall 绑定完全不同 ——
// 不在本期范围内（docs/ctp-oracle.md 第 1 节 ②）。
//
// 这里留一份同 API 的桩，目的只有一个：让 `go build ./...` 在别的平台上
// **仍然能过**，而任何实际调用都得到一句说清楚的错误。
// ⚠️ 桩不返回零值假装成功 —— 那会让「这个平台没实现」在运行时
// 表现成「柜台没反应」。

type Credentials struct {
	Front    string
	BrokerID string
	UserID   string
	Password string
	AppID    string
	AuthCode string
}

type Client struct {
	cred  Credentials
	Valve safety.Valve
}

func New(cred Credentials, _ func(string, ...any)) *Client { return &Client{cred: cred} }

var errPlatform = fmt.Errorf("CTP 客户端**只支持 Windows** —— " +
	"Linux 侧是 .so + cgo，形状完全不同，不在本期范围内（docs/ctp-oracle.md 第 1 节）")

func (c *Client) TradingDay() string                    { return "" }
func (c *Client) Connect(time.Duration) error           { return errPlatform }
func (c *Client) BrokerParams(time.Duration) (*def.CThostFtdcBrokerTradingParamsField, error) {
	return nil, errPlatform
}
func (c *Client) Close()                                {}

// ⚠️ 这几个同样返回错误而不是空值：一个返回 (nil, nil) 的桩
// 会让「这个平台没实现」在调用方表现成「账户是空的」。
func (c *Client) Account(time.Duration) (*def.CThostFtdcTradingAccountField, error) {
	return nil, errPlatform
}

func (c *Client) Positions(time.Duration) (map[string]*def.CThostFtdcInvestorPositionField, error) {
	return nil, errPlatform
}

func (c *Client) Capture(time.Duration, string) (*Fixture, error) { return nil, errPlatform }

// ⚠️ 报单相关的桩同样返回错误。**尤其是这几个**：一个返回 (零值, nil) 的
// Insert 会让调用方以为「单发出去了、只是还没回报」，而实际上什么都没发生 ——
// 那比「没实现」危险得多。
func (c *Client) Insert(OrderReq, time.Duration) (OrderState, error) {
	return OrderState{}, errPlatform
}

func (c *Client) Cancel(string, OrderReq) error { return errPlatform }

// MarketData 在非 Windows 上没有实现 —— ⚠️ 返回**明确错误**而不是零值：
// 后者会让「这个平台没实现」在运行时表现成「柜台没反应」，
// 而那两件事要人做的事完全不同。
func (c *Client) MarketData(string, time.Duration) (*def.CThostFtdcDepthMarketDataField, error) {
	return nil, errPlatform
}

// Order 在非 Windows 上永远查不到：这里一笔单都发不出去。
func (c *Client) Order(string) (OrderState, bool) { return OrderState{}, false }
