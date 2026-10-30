package ctp

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

var errQueryClosed = errors.New("CTP客户端已关闭，查询没有完整成功")

// abort 保留首错，清除半截数据且只关闭一次信号；迟到回调不能复活结果。
func (b *queryBatch[T]) abort(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active {
		return
	}
	if b.err == nil {
		b.err = err
	}
	b.rows = nil
	if !b.complete {
		b.complete = true
		close(b.done)
	}
}

// queryBatch 关联一次分批查询；柜台回调可并发到来，调用者只在全部成功后取得数据。
// 上层 queryer 保证同一客户端串行发查询，请求编号还负责隔离超时后的迟到回报。
type queryBatch[T any] struct {
	mu               sync.Mutex
	id               int
	active, complete bool
	rows             []T
	err              error
	done             chan struct{}
}

func (b *queryBatch[T]) begin(id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.id = id
	b.active = true
	b.complete = false
	b.rows = nil
	b.err = nil
	b.done = make(chan struct{})
}

func (b *queryBatch[T]) receive(id int, row *T, err error, last bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.active || b.complete || id != b.id {
		return
	}
	if err != nil && b.err == nil {
		b.err = err
	}
	if row != nil && err == nil {
		b.rows = append(b.rows, *row)
	}
	if last {
		b.complete = true
		close(b.done)
	}
}

func (b *queryBatch[T]) wait(id int, timeout time.Duration) ([]T, error) {
	b.mu.Lock()
	if id != b.id || !b.active {
		b.mu.Unlock()
		return nil, fmt.Errorf("查询 %d 已失效", id)
	}
	done := b.done
	b.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if id != b.id || !b.active {
		return nil, fmt.Errorf("查询 %d 已失效", id)
	}
	b.active = false
	if b.err != nil {
		b.rows = nil
		return nil, b.err
	}
	if !b.complete {
		b.rows = nil
		return nil, fmt.Errorf("%v 内查询 %d 未完整返回，没有结论", timeout, id)
	}
	rows := append([]T(nil), b.rows...)
	b.rows = nil
	return rows, nil
}
