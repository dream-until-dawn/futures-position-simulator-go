package ctp

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestQueryBatchSeparatesEmptyAndFailure 空结果成功与柜台错误绝不可混为一谈。
func TestQueryBatchSeparatesEmptyAndFailure(t *testing.T) {
	var b queryBatch[int]
	b.begin(1)
	b.receive(1, nil, nil, true)
	if rows, err := b.wait(1, time.Second); err != nil || len(rows) != 0 {
		t.Fatalf("空成功被拒：%v %v", rows, err)
	}
	sentinel := errors.New("柜台拒绝查询")
	one := 1
	b.begin(2)
	b.receive(2, &one, nil, false)
	b.receive(2, nil, sentinel, false)
	b.receive(2, nil, nil, true)
	if rows, err := b.wait(2, time.Second); !errors.Is(err, sentinel) || rows != nil {
		t.Fatalf("失败被洗成空成功或返回半截数据：%v %v", rows, err)
	}
}

// TestQueryBatchIgnoresLateReplies 超时后的数据、错误和结束信号不能串进新查询。
func TestQueryBatchIgnoresLateReplies(t *testing.T) {
	var b queryBatch[int]
	one, two := 1, 2
	b.begin(10)
	if _, err := b.wait(10, time.Millisecond); err == nil {
		t.Fatal("超时被当成空成功")
	}
	b.begin(11)
	b.receive(10, &one, errors.New("旧请求失败"), true)
	b.receive(11, &two, nil, true)
	b.receive(11, &one, nil, true)
	got, err := b.wait(11, time.Second)
	if err != nil || !reflect.DeepEqual(got, []int{2}) {
		t.Fatalf("迟到或重复结束污染新请求：%v %v", got, err)
	}
}

// TestQueryBatchKeepsFirstError 后续空结束或其他错误不能清掉最早失败的原因。
func TestQueryBatchKeepsFirstError(t *testing.T) {
	var b queryBatch[int]
	first := errors.New("首次失败")
	b.begin(20)
	b.receive(20, nil, first, false)
	b.receive(20, nil, errors.New("后续失败"), true)
	if rows, err := b.wait(20, time.Second); rows != nil || !errors.Is(err, first) {
		t.Fatalf("首个错误被覆盖：%v %v", rows, err)
	}
	// 即时发送失败和通用错误走相同终止路径，不需要等一条并不存在的 isLast。
	b.begin(21)
	b.receive(21, nil, first, true)
	if _, err := b.wait(21, time.Second); !errors.Is(err, first) {
		t.Fatalf("即时拒绝未传播：%v", err)
	}
}
