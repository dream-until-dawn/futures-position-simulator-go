//go:build windows

package ctp

import "unsafe"

// invokeNativeOrder 以有限枚举核对两个ABI，不提供任意偏移入口。
func invokeNativeOrder(api uintptr, kind nativeOrderKind, request unsafe.Pointer, id int, module uintptr) (int32, error) {
	offset, err := nativeOrderSlot(kind)
	if err != nil {
		return 0, err
	}
	return invokeNativeRequest(api, request, id, module, offset)
}

// sendNativeOrder 与Release互斥，唯一报单调用者仍先经过send的原安全阀。
func (c *Client) sendNativeOrder(kind nativeOrderKind, request unsafe.Pointer) error {
	c.setupMu.Lock()
	defer c.setupMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	operation := nativeOrderOperation(kind)
	if closed {
		return &NativeOrderError{Operation: operation, Code: nativeHandshakeBindingError, Local: true}
	}
	code, err := invokeNativeOrder(c.api, kind, request, c.nextRequestID(), c.nativeSDKModule)
	if err != nil {
		c.logf("[ctp.order.native] operation=%s event=binding-error", operation)
		return &NativeOrderError{Operation: operation, Code: nativeHandshakeBindingError, Local: true}
	}
	if code != 0 {
		return &NativeOrderError{Operation: operation, Code: code}
	}
	return nil
}
