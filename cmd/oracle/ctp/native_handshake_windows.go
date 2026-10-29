//go:build windows

package ctp

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

var readHandshakeMemory = syscall.NewLazyDLL("kernel32.dll").NewProc("ReadProcessMemory")
var queryHandshakeMemory = syscall.NewLazyDLL("kernel32.dll").NewProc("VirtualQuery")

// nativeMemoryRegion 对应Windows amd64的MEMORY_BASIC_INFORMATION，只用于核验方法页可执行。
type nativeMemoryRegion struct {
	base, allocationBase             uintptr
	allocationProtect, padding       uint32
	size                             uintptr
	state, protect, kind, endPadding uint32
}

// validateHandshakeMethod 只接受已提交且可执行、无PAGE_GUARD/PAGE_NOACCESS的本进程页。
func validateHandshakeMethod(address uintptr) error {
	var region nativeMemoryRegion
	n, _, _ := queryHandshakeMemory.Call(address, uintptr(unsafe.Pointer(&region)), unsafe.Sizeof(region))
	if n != unsafe.Sizeof(region) || region.state != 0x1000 || region.protect&0xf0 == 0 || region.protect&0x101 != 0 {
		return fmt.Errorf("原生握手方法不在可执行内存页")
	}
	return nil
}

// readHandshakePointer 安全读取本进程指针，错误句柄不能成为Go的非法地址解引用。
func readHandshakePointer(address uintptr) (uintptr, error) {
	if address == 0 {
		return 0, fmt.Errorf("原生握手指针缺失")
	}
	var value, read uintptr
	ok, _, _ := readHandshakeMemory.Call(^uintptr(0), address, uintptr(unsafe.Pointer(&value)), unsafe.Sizeof(value), uintptr(unsafe.Pointer(&read)))
	if ok == 0 || read != unsafe.Sizeof(value) || value == 0 {
		return 0, fmt.Errorf("原生握手指针不可读或为空")
	}
	return value, nil
}

// invokeNativeHandshake 保留API真正的int32返回值，绕过已证实会清零的C导出包装。
// 生产路径必须先验证固定DLL哈希；此处只读取API自身的三个已登记方法，不计算任意函数地址。
func invokeNativeHandshake(api uintptr, name string, request unsafe.Pointer, id int) (int32, error) {
	if runtime.GOARCH != "amd64" || api == 0 || request == nil || id <= 0 {
		return 0, fmt.Errorf("原生握手架构或请求参数无效")
	}
	offset, err := nativeHandshakeSlot(name)
	if err != nil {
		return 0, err
	}
	table, err := readHandshakePointer(api)
	if err != nil {
		return 0, err
	}
	if table > ^uintptr(0)-offset {
		return 0, fmt.Errorf("原生握手方法偏移溢出")
	}
	method, err := readHandshakePointer(table + offset)
	if err != nil {
		return 0, err
	}
	if err = validateHandshakeMethod(method); err != nil {
		return 0, err
	}
	result, _, _ := syscall.SyscallN(method, api, uintptr(request), uintptr(id))
	runtime.KeepAlive(request)
	return int32(result), nil
}
