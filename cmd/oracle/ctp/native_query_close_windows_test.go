//go:build windows

package ctp

import (
	"errors"
	"testing"
	"time"
)

// TestCloseAbortsAllNineQueries 覆盖生产Client的九条实际接线，不能只证明一个孤立批次。
func TestCloseAbortsAllNineQueries(t *testing.T) {
	c := New(Credentials{}, nil)
	c.positionRows.begin(1)
	c.orderRows.begin(1)
	c.tradeRows.begin(1)
	c.settlementRows.begin(1)
	c.accountRows.begin(1)
	c.commissionRows.begin(1)
	c.instrumentRows.begin(1)
	c.marketRows.begin(1)
	c.brokerRows.begin(1)
	c.Close()
	waits := []func() error{
		func() error { _, e := c.positionRows.wait(1, time.Millisecond); return e },
		func() error { _, e := c.orderRows.wait(1, time.Millisecond); return e },
		func() error { _, e := c.tradeRows.wait(1, time.Millisecond); return e },
		func() error { _, e := c.settlementRows.wait(1, time.Millisecond); return e },
		func() error { _, e := c.accountRows.wait(1, time.Millisecond); return e },
		func() error { _, e := c.commissionRows.wait(1, time.Millisecond); return e },
		func() error { _, e := c.instrumentRows.wait(1, time.Millisecond); return e },
		func() error { _, e := c.marketRows.wait(1, time.Millisecond); return e },
		func() error { _, e := c.brokerRows.wait(1, time.Millisecond); return e },
	}
	for i, wait := range waits {
		if err := wait(); !errors.Is(err, errQueryClosed) {
			t.Fatal("关闭遗漏查询批次", i, err)
		}
	}
}
