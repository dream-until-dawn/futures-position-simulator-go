//go:build windows && amd64

package ctp

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// TestNativeHandshakeDiagnosticLog 核对生产Connect日志而不是仅观察收集器的测试格式。
func TestNativeHandshakeDiagnosticLog(t *testing.T) {
	var text strings.Builder
	c := fakeHandshakeClient(func(h *handshakeCollector, stage string, id int) int32 {
		secret := handshakeLoginResult{day: "LEAKME", maxOrderRef: "LEAKME"}
		h.reply(stage, id+100, &secret, 0, true)
		row := handshakeFixture()
		h.reply(stage, id, &row, 0, true)
		return 0
	})
	c.logf = func(f string, a ...any) { fmt.Fprintf(&text, f+"\n", a...) }
	if err := c.Connect(time.Second); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"event=ignored-request", "received_stage=authenticate", "received_request=101", "last=true", "has_payload=true"} {
		if !strings.Contains(text.String(), want) {
			t.Fatal("生产握手日志缺少过滤元数据", want)
		}
	}
	if strings.Contains(text.String(), "LEAKME") {
		t.Fatal("生产握手日志泄漏回报负载")
	}
}

// nativeFixtureDLL 只加载依赖的原生包装，不创建API、不初始化连接、不读取凭据。
func nativeFixtureDLL(t *testing.T) *syscall.DLL {
	t.Helper()
	dir, _, err := DLLDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"thosttraderapi_se.dll", "ctp_trade.dll"} {
		dll, err := syscall.LoadDLL(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { dll.Release() })
		if name == "ctp_trade.dll" {
			return dll
		}
	}
	t.Fatal("离线原生包装夹具缺失")
	return nil
}

// nativeFakeAPI 模拟已核验ABI的三个方法；方法只回调本进程Go代码，绝不连接柜台。
type nativeFakeAPI struct {
	table [29]uintptr
	api   [1]uintptr
	keep  []any
}

func newNativeFakeAPI(call func(string, uintptr, int) int32) *nativeFakeAPI {
	f := &nativeFakeAPI{}
	for stage, index := range map[string]int{handshakeAuth: 10, handshakeLogin: 13, handshakeConfirm: 28} {
		stage := stage
		callback := func(api uintptr, request uintptr, id uintptr) uintptr {
			if api != f.address() || request == 0 || id == 0 {
				return uintptr(nativeHandshakeFailureForTest())
			}
			code := call(stage, request, int(id))
			return uintptr(code)
		}
		f.keep = append(f.keep, callback)
		f.table[index] = syscall.NewCallback(callback)
	}
	f.api[0] = uintptr(unsafe.Pointer(&f.table[0]))
	return f
}

// 返回运行时值避免负常量直接转换uintptr；夹具错误必须有明确的非零结果。
func nativeHandshakeFailureForTest() int32 { return nativeHandshakeBindingError }

// TestNativeHandshakeConnectChain 将实际发送器、原生假API和收集器连起来，确认失败不发布、不重发。
func TestNativeHandshakeConnectChain(t *testing.T) {
	for _, code := range []int32{0, -1, -2, -3} {
		count := 0
		var current *handshakeCollector
		f := newNativeFakeAPI(func(stage string, _ uintptr, id int) int32 {
			count++
			if stage == handshakeConfirm && code != 0 {
				return code
			}
			row := handshakeFixture()
			current.reply(stage, id, &row, 0, true)
			return 0
		})
		c := fakeHandshakeClient(nil)
		c.api = f.address()
		c.handshakeSend = func(h *handshakeCollector, stage string, id int) int32 {
			current = h
			return c.sendHandshake(stage, id)
		}
		err := c.Connect(time.Second)
		runtime.KeepAlive(f)
		if count != 3 {
			t.Fatal("原生握手失败后重试或阶段遗漏", count)
		}
		if code == 0 {
			if err != nil || c.TradingDay() != handshakeFixture().day || atomic.LoadInt64(&c.orderSeq) != 41 {
				t.Fatal("原生成功链未发布完整会话", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "event=send-rejected") || c.TradingDay() != "" || atomic.LoadInt64(&c.orderSeq) != 0 {
			t.Fatal("原生拒绝变成超时或会话成功", err)
		}
		if c.Connect(time.Second) == nil || count != 3 {
			t.Fatal("原生失败客户端允许重复连接")
		}
	}
}

// TestNativeHandshakeInvalidPointers 不可读指针只返回错误，不能崩溃、执行任意方法或伪装原生成功。
func TestNativeHandshakeInvalidPointers(t *testing.T) {
	var request byte
	for _, api := range []uintptr{0, 1, ^uintptr(0)} {
		if _, err := invokeNativeHandshake(api, "ReqAuthenticate", unsafe.Pointer(&request), 1); err == nil {
			t.Fatal("非法原生指针被放行")
		}
	}
	f := newNativeFakeAPI(func(string, uintptr, int) int32 { t.Fatal("无效参数触发原生请求"); return 0 })
	for _, name := range []string{"ReqOrderInsert", ""} {
		if _, err := invokeNativeHandshake(f.address(), name, unsafe.Pointer(&request), 1); err == nil {
			t.Fatal("无效原生请求被放行")
		}
	}
	if _, err := invokeNativeHandshake(f.address(), "ReqAuthenticate", nil, 1); err == nil {
		t.Fatal("无效原生请求被放行")
	}
	if _, err := invokeNativeHandshake(f.address(), "ReqAuthenticate", unsafe.Pointer(&request), 0); err == nil {
		t.Fatal("无效原生请求被放行")
	}
	for _, method := range []uintptr{0, 1, uintptr(unsafe.Pointer(&request))} {
		f.table[28] = method
		if _, err := invokeNativeHandshake(f.address(), "ReqSettlementInfoConfirm", unsafe.Pointer(&request), 1); err == nil {
			t.Fatal("不可执行原生方法被放行")
		}
	}
	f.api[0] = ^uintptr(0) - 1
	if _, err := invokeNativeHandshake(f.address(), "ReqAuthenticate", unsafe.Pointer(&request), 1); err == nil || !strings.Contains(err.Error(), "偏移溢出") {
		t.Fatal("溢出原生方法被放行")
	}
	c := New(Credentials{}, nil)
	if c.sendHandshake(handshakeConfirm, 1) != nativeHandshakeBindingError {
		t.Fatal("本地绑定失败被当成原生成功")
	}
	runtime.KeepAlive(f)
}

// TestNativeHandshakeExecutablePage 直接验证页权限；破坏验证不能靠跳进数据页崩溃来冒充断言检出。
func TestNativeHandshakeExecutablePage(t *testing.T) {
	callback := func() uintptr { return 0 }
	if err := validateHandshakeMethod(syscall.NewCallback(callback)); err != nil {
		t.Fatal("有效原生代码页被拒绝", err)
	}
	var data byte
	for _, address := range []uintptr{0, 1, uintptr(unsafe.Pointer(&data))} {
		if validateHandshakeMethod(address) == nil {
			t.Fatal("不可执行原生方法被放行")
		}
	}
	runtime.KeepAlive(callback)
}

func (f *nativeFakeAPI) address() uintptr { return uintptr(unsafe.Pointer(&f.api[0])) }

// TestNativeHandshakeLegacyWrapper 用实际DLL证明原生-3被导出包装清零，既不需要服务器也不触发CreateApi。
func TestNativeHandshakeLegacyWrapper(t *testing.T) {
	dll := nativeFixtureDLL(t)
	observed := 0
	f := newNativeFakeAPI(func(stage string, request uintptr, id int) int32 {
		if stage != handshakeConfirm || request == 0 || id != 7 {
			t.Fatal("原生夹具收到错误请求")
		}
		observed++
		return -3
	})
	var request [128]byte
	result, _, _ := dll.MustFindProc("ReqSettlementInfoConfirm").Call(f.address(), uintptr(unsafe.Pointer(&request[0])), 7)
	runtime.KeepAlive(f)
	runtime.KeepAlive(request)
	if observed != 1 || int32(result) != 0 {
		t.Fatal("固定DLL未复现包装清零，必须重新核验ABI证据")
	}
}

// TestNativeHandshakeSendCodes 钉住实际sendHandshake路径，不能用只注入返回码的假传输代替。
func TestNativeHandshakeSendCodes(t *testing.T) {
	dll := nativeFixtureDLL(t)
	for _, stage := range []string{handshakeAuth, handshakeLogin, handshakeConfirm} {
		for _, want := range []int32{0, -1, -2, -3, 17} {
			count := 0
			f := newNativeFakeAPI(func(got string, request uintptr, id int) int32 {
				if got != stage || request == 0 || id != 7 {
					t.Fatal("原生握手参数或方法串位")
				}
				count++
				return want
			})
			c := New(Credentials{}, nil)
			c.h, c.api = dll, f.address()
			got := c.sendHandshake(stage, 7)
			runtime.KeepAlive(f)
			if got != want || count != 1 {
				t.Fatalf("原生握手发送码被包装清零 stage=%s want=%d got=%d calls=%d", stage, want, got, count)
			}
		}
	}
}
