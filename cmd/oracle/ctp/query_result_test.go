package ctp

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestQueryTransportBoundary 用同步假回调证明登记必须先于发送，错误不能返回半截结果。
func TestQueryTransportBoundary(t *testing.T) {
	var batch queryBatch[int]
	value := 42
	rows, err := runQuery(&batch, 1, time.Second, func(id int) int32 {
		batch.receive(id, &value, nil, true)
		return 0
	})
	if err != nil || len(rows) != 1 || rows[0] != value {
		t.Fatalf("同步成功丢失：%v %v", rows, err)
	}
	rows, err = runQuery(&batch, 2, time.Second, func(id int) int32 { return -2 })
	if rows != nil || err == nil || !strings.Contains(err.Error(), "返回码 -2") {
		t.Fatalf("即时拒绝没有传播：%v %v", rows, err)
	}
	rows, err = runQuery(&batch, 5, time.Second, func(id int) int32 {
		batch.receive(id, &value, nil, true)
		return -3
	})
	if rows != nil || err == nil || !strings.Contains(err.Error(), "返回码 -3") {
		t.Fatalf("同步回调掩盖发送拒绝：%v %v", rows, err)
	}
	sentinel := errors.New("本次柜台查询拒绝")
	rows, err = runQuery(&batch, 3, time.Second, func(id int) int32 {
		batch.receive(id, &value, nil, false)
		batch.receive(id, nil, sentinel, true)
		return 0
	})
	if rows != nil || !errors.Is(err, sentinel) {
		t.Fatalf("回调错误被改成部分成功：%v %v", rows, err)
	}
	called := false
	if _, err = runQuery(&batch, 4, 0, func(int) int32 { called = true; return 0 }); err == nil || called {
		t.Fatal("非法超时仍发送查询")
	}
}

// TestQueryTransportLateReply 超时与新查询交错，旧数据和旧错误都不能污染新结果。
func TestQueryTransportLateReply(t *testing.T) {
	var batch queryBatch[int]
	if _, err := runQuery(&batch, 10, time.Millisecond, func(int) int32 { return 0 }); err == nil {
		t.Fatal("超时未失败")
	}
	value, old := 42, 99
	rows, err := runQuery(&batch, 11, time.Second, func(id int) int32 {
		batch.receive(10, &old, errors.New("旧请求失败"), true)
		batch.receive(id, &value, nil, true)
		return 0
	})
	if err != nil || len(rows) != 1 || rows[0] != value {
		t.Fatalf("旧编号污染新查询：%v %v", rows, err)
	}
}

// TestUniqueQueryRow 区分空结果、其他合约、重复匹配和唯一精确匹配。
func TestUniqueQueryRow(t *testing.T) {
	type row struct {
		symbol string
		tick   int
	}
	match := func(r row) bool { return r.symbol == "rb2701" }
	rows := []row{{"rb2701C3000", 5}, {"rb2701", 1}}
	got, err := uniqueQueryRow(rows, match)
	if err != nil || got.tick != 1 {
		t.Fatalf("精确匹配失败：%v %v", got, err)
	}
	rows[1].tick = 99
	if got.tick != 1 {
		t.Fatal("查询结果仍引用可变输入")
	}
	for _, input := range [][]row{nil, {{"rb2701C3000", 5}}, {{"rb2701", 1}, {"rb2701", 2}}} {
		if got, err := uniqueQueryRow(input, match); err == nil || got != nil {
			t.Fatalf("零条或不唯一结果仍通过：%v %v", got, err)
		}
	}
	if _, err = uniqueQueryRow([]int{1, 2}, nil); err == nil {
		t.Fatal("无过滤的多条账户仍通过")
	}
	if got, err := uniqueQueryRow([]int{7}, nil); err != nil || *got != 7 {
		t.Fatalf("唯一账户失败：%v %v", got, err)
	}
}
