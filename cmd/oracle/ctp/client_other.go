//go:build !windows

package ctp

import (
	"fmt"
	"time"
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

type Client struct{ cred Credentials }

func New(cred Credentials, _ func(string, ...any)) *Client { return &Client{cred: cred} }

var errPlatform = fmt.Errorf("CTP 客户端**只支持 Windows** —— " +
	"Linux 侧是 .so + cgo，形状完全不同，不在本期范围内（docs/ctp-oracle.md 第 1 节）")

func (c *Client) TradingDay() string                    { return "" }
func (c *Client) Connect(time.Duration) error           { return errPlatform }
func (c *Client) BrokerParams(time.Duration) (any, error) { return nil, errPlatform }
func (c *Client) Close()                                {}
