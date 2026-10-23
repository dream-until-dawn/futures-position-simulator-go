package ctp

import (
	"fmt"
	"time"
)

// runQuery 在发送之前登记编号；发送返回码也必须终止本次查询，不能只等回调。
// send 仅为只读查询的传输边界，生产调用者仍由 queryer 串行节流。
func runQuery[T any](batch *queryBatch[T], id int, timeout time.Duration, send func(int) int32) ([]T, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("查询超时必须为正")
	}
	batch.begin(id)
	if code := send(id); code != 0 {
		err := fmt.Errorf("查询请求被立即拒绝：返回码 %d", code)
		batch.receive(id, nil, err, true)
		// 即便异常传输先同步回调了成功，发送拒绝也不能被它覆盖。
		// wait 负责停用本次批次并清理缓存；返回值始终以发送失败为准。
		_, _ = batch.wait(id, timeout)
		return nil, err
	}
	return batch.wait(id, timeout)
}

// uniqueQueryRow 只接受唯一的精确匹配。其他合约、零条和重复匹配均不能冒充答案。
func uniqueQueryRow[T any](rows []T, match func(T) bool) (*T, error) {
	var found *T
	for i := range rows {
		if match != nil && !match(rows[i]) {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("查询结果有多条匹配，不能擅自选择")
		}
		copyRow := rows[i]
		found = &copyRow
	}
	if found == nil {
		return nil, fmt.Errorf("查询完整返回，但没有匹配记录")
	}
	return found, nil
}
