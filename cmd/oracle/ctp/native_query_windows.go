//go:build windows

package ctp

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// invokeNativeQuery 保留SDK真正的int32；模块来自加载后校验，不由调用者输入路径猜测。
func invokeNativeQuery(api uintptr, name string, request unsafe.Pointer, id int, module uintptr) (int32, error) {
	if runtime.GOARCH != "amd64" || api == 0 || request == nil || id <= 0 || int64(id) > 2147483647 || module == 0 {
		return 0, fmt.Errorf("原生查询架构、句柄或请求参数无效")
	}
	offset, err := nativeQuerySlot(name)
	if err != nil {
		return 0, err
	}
	table, err := readHandshakePointer(api)
	if err != nil {
		return 0, err
	}
	if table > ^uintptr(0)-offset {
		return 0, fmt.Errorf("原生查询方法偏移溢出")
	}
	method, err := readHandshakePointer(table + offset)
	if err != nil {
		return 0, err
	}
	if err = validateHandshakeMethod(method); err != nil {
		return 0, err
	}
	var region nativeMemoryRegion
	n, _, _ := queryHandshakeMemory.Call(method, uintptr(unsafe.Pointer(&region)), unsafe.Sizeof(region))
	if n != unsafe.Sizeof(region) || region.allocationBase != module {
		return 0, fmt.Errorf("原生查询方法不属于已核SDK映像")
	}
	result, _, _ := syscall.SyscallN(method, api, uintptr(request), uintptr(id))
	runtime.KeepAlive(request)
	return int32(result), nil
}
