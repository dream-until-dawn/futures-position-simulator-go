//go:build windows

package ctp

import "unsafe"

// invokeNativeQuery 先通过九方法白名单，再进入共用指针和映像边界。
func invokeNativeQuery(api uintptr, name string, request unsafe.Pointer, id int, module uintptr) (int32, error) {
	offset, err := nativeQuerySlot(name)
	if err != nil {
		return 0, err
	}
	return invokeNativeRequest(api, request, id, module, offset)
}
