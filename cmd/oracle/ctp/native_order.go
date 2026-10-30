package ctp

import "fmt"

type nativeOrderKind uint8

const (
	nativeOrderInsert nativeOrderKind = iota + 1
	nativeOrderCancel
)

// nativeOrderSlot 只允许固定报单/撤单，不接受任意名称或虚表偏移。
func nativeOrderSlot(kind nativeOrderKind) (uintptr, error) {
	switch kind {
	case nativeOrderInsert:
		return 0xb8, nil
	case nativeOrderCancel:
		return 0xd0, nil
	default:
		return 0, fmt.Errorf("未登记的原生订单方法")
	}
}

func nativeOrderOperation(kind nativeOrderKind) string {
	if kind == nativeOrderInsert {
		return "insert"
	}
	if kind == nativeOrderCancel {
		return "cancel"
	}
	return "unknown"
}

// NativeOrderError 区分本地失败与SDK即时非零；不能据此改写已经观察到的委托终态。
type NativeOrderError struct {
	// Operation 固定为insert/cancel，不包含账户、委托号或柜台自由文本。
	Operation string
	// Code 保留SDK的int32或本地保留码，不等于OrderState.ErrorID。
	Code int32
	// Local 为true表示本地闭锁/绑定失败，false表示实际SDK调用的非零结果。
	Local bool
}

// Error 仅输出固定操作、数值码与来源，不含柜台自由文本或账户身份。
func (e *NativeOrderError) Error() string {
	return fmt.Sprintf("原生请求未完整成功 operation=%s code=%d local=%t；不能据此改写委托终态", e.Operation, e.Code, e.Local)
}
