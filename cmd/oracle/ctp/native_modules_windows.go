//go:build windows

package ctp

import (
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var nativeGetModuleFileName = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleFileNameW")
var nativeGetModuleHandle = syscall.NewLazyDLL("kernel32.dll").NewProc("GetModuleHandleW")

// nativeModulePath 使用真实句柄，不把DLL搜索目录当成实际加载结果。
func nativeModulePath(module uintptr) (string, error) {
	if module == 0 {
		return "", fmt.Errorf("原生模块句柄缺失")
	}
	buffer := make([]uint16, 32768)
	n, _, _ := nativeGetModuleFileName.Call(module, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
	if n == 0 || n >= uintptr(len(buffer)) {
		return "", fmt.Errorf("原生模块路径不可确认或被截断")
	}
	return syscall.UTF16ToString(buffer[:n]), nil
}

// loadedNativeSDK 核对两个实际加载路径及对应磁盘哈希；不冒充内存映像签名认证。
func loadedNativeSDK(dir string, wrapper uintptr) (uintptr, error) {
	wrapperPath, err := nativeModulePath(wrapper)
	if err != nil {
		return 0, err
	}
	name, _ := syscall.UTF16PtrFromString("thosttraderapi_se.dll")
	sdk, _, _ := nativeGetModuleHandle.Call(uintptr(unsafe.Pointer(name)))
	sdkPath, err := nativeModulePath(sdk)
	if err != nil {
		return 0, err
	}
	if err := validateLoadedModuleFiles(dir, wrapperPath, sdkPath); err != nil {
		return 0, err
	}
	return sdk, nil
}

// validateLoadedModuleFiles 只核给定路径及磁盘文件；真实模块路径必须由上层系统调用取得。
func validateLoadedModuleFiles(dir, wrapperPath, sdkPath string) error {
	for _, item := range []struct{ actual, file string }{{wrapperPath, "ctp_trade.dll"}, {sdkPath, "thosttraderapi_se.dll"}} {
		expected, err := filepath.Abs(filepath.Join(dir, item.file))
		if err != nil || !strings.EqualFold(filepath.Clean(item.actual), filepath.Clean(expected)) {
			return fmt.Errorf("实际加载原生模块路径与登记不符")
		}
	}
	if err := validateNativeHandshakeDLLs(dir); err != nil {
		return err
	}
	return nil
}
