//go:build windows && amd64

package ctp

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// buildSPIBridge 构建只调用SPI的离线线程夹具，不创建CTP API。
func buildSPIBridge(t *testing.T) *syscall.Proc {
	t.Helper()
	dir := t.TempDir()
	source, err := os.ReadFile("testdata/spi_bridge.c")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "bridge.c"), source, 0600); err != nil {
		t.Fatal(err)
	}
	cc := os.Getenv("CC")
	if cc == "" {
		cc = "gcc"
	}
	cmd := exec.Command(cc, "-shared", "-save-temps=obj", "-O2", "-Wall", "-Werror", "-o", "bridge.dll", "bridge.c")
	cmd.Dir = dir
	// 汇编器中间文件使用相对路径，避免中文用户临时路径编码不兼容。
	cmd.Env = append(os.Environ(), "TMPDIR=.")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("构建离线SPI线程夹具: %v %s", err, output)
	}
	dll, err := syscall.LoadDLL(filepath.Join(dir, "bridge.dll"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dll.Release() })
	return dll.MustFindProc("RunFixture")
}

// nativeSPIObject 先准备固定包装的SPI，绝不CreateApi/Init/Release。
// DLL探测和哈希可能在慢宿主上耗时，必须在被测收集器开始计时前完成。
func nativeSPIObject(t *testing.T) *Client {
	t.Helper()
	dir, _, err := DLLDir()
	if err != nil {
		t.Fatal(err)
	}
	if err = validateNativeHandshakeDLLs(dir); err != nil {
		t.Fatal(err)
	}
	c := New(Credentials{}, nil)
	c.h = nativeFixtureDLL(t)
	c.spi, _, _ = c.h.MustFindProc("CreateSpi").Call()
	if c.spi == 0 {
		t.Fatal("离线SPI创建失败")
	}
	// 固定包装没有DestroySpi导出；有限测试对象由进程退出回收。
	return c
}

func spiMethod(t *testing.T, c *Client, offset uintptr) uintptr {
	t.Helper()
	table, err := readHandshakePointer(c.spi)
	if err != nil {
		t.Fatal(err)
	}
	method, err := readHandshakePointer(table + offset)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateHandshakeMethod(method); err != nil {
		t.Fatal(err)
	}
	if queryFixtureModule(t, method) != uintptr(c.h.Handle) {
		t.Fatal("SPI方法不属于固定包装")
	}
	return method
}

// TestNativeHandshakeSPIBridge 真实C++虚方法在Windows外部线程进入生产Go回调。
func TestNativeHandshakeSPIBridge(t *testing.T) {
	run := buildSPIBridge(t)
	for _, tc := range []struct {
		name                   string
		id, code, flags        int
		stop, complete, failed bool
	}{
		{"accepted", 7, 0, 4, false, true, false},
		{"wrong-id", 8, 0, 4, false, false, false},
		{"partial", 7, 0, 0, false, false, false},
		{"error", 7, 42, 4, false, true, true},
		{"nil-original-row", 7, 0, 7, false, true, false},
		{"late", 7, 0, 4, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := nativeSPIObject(t)
			method := spiMethod(t, c, 0x80)
			h := newHandshake(10*time.Second, nil)
			if err := h.prepare(handshakeConfirm, 7); err != nil {
				t.Fatal(err)
			}
			c.registerHandshakeCallbacks(h)
			if tc.stop {
				h.seal()
			}
			runtime.GC()
			thread, _, _ := run.Call(method, c.spi, uintptr(tc.id), uintptr(tc.code), uintptr(tc.flags))
			if thread == 0 {
				t.Fatal("外部线程夹具失败")
			}
			h.mu.Lock()
			complete, failed := h.complete, h.err != nil
			h.mu.Unlock()
			if complete != tc.complete || failed != tc.failed {
				t.Fatal("真实SPI桥改变过滤或错误语义", tc.name, complete, failed)
			}
			entry := h.entries.stages[3].Load()
			if entry == nil || entry.count != 1 || entry.id != tc.id || entry.code != int32(tc.code) || entry.last != (tc.flags&4 != 0) || !entry.wrappedPayload {
				t.Fatal("真实SPI入口记录丢失或串位", entry)
			}
			// nil-original-row仍wrappedPayload=true，明确复现固定包装的空指针替换。
			runtime.KeepAlive(c)
		})
	}
}

func TestNativeHandshakeSPIGeneralErrorAndConcurrency(t *testing.T) {
	run := buildSPIBridge(t)
	c := nativeSPIObject(t)
	method := spiMethod(t, c, 0x240) // 已核SDK头文件OnRspError第72项。
	h := newHandshake(10*time.Second, nil)
	if err := h.prepare(handshakeConfirm, 7); err != nil {
		t.Fatal(err)
	}
	c.registerHandshakeCallbacks(h)
	var workers sync.WaitGroup
	for i := 0; i < 24; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			thread, _, _ := run.Call(method, c.spi, 7, 0, 12)
			if thread == 0 {
				t.Error("并发外部线程失败")
			}
		}()
	}
	workers.Wait()
	runtime.GC()
	entry := h.entries.stages[4].Load()
	if entry == nil || entry.count != 24 || entry.code != 0 || entry.id != 7 {
		t.Fatal("零错误通用回调在入口处不可见", entry)
	}
	h.mu.Lock()
	complete := h.complete
	h.mu.Unlock()
	if complete {
		t.Fatal("零错误通用回调被伪造为握手完成")
	}
	// 并发与GC验证入口，不测试宿主跑完它们需要多少秒。
	// 最后一笔非零错误使用独立新收集器，不能借用已耗尽的旧期限。
	response := newHandshake(10*time.Second, nil)
	if err := response.prepare(handshakeConfirm, 7); err != nil {
		t.Fatal(err)
	}
	c.registerHandshakeCallbacks(response)
	thread, _, _ := run.Call(method, c.spi, 7, 42, 12)
	if thread == 0 {
		t.Fatal("错误线程失败")
	}
	response.mu.Lock()
	complete, failed := response.complete, response.err != nil
	response.mu.Unlock()
	if !complete || !failed {
		t.Fatalf("真实通用错误未传入握手 elapsed=%s expired=%t entry=%+v", time.Since(response.started), !time.Now().Before(response.deadline), response.entries.stages[4].Load())
	}
	runtime.KeepAlive(c)
}

// TestNativeHandshakeSPIAllStages 补齐前置、断开、认证与登录的真实回调桥。
func TestNativeHandshakeSPIAllStages(t *testing.T) {
	run := buildSPIBridge(t)
	for _, tc := range []struct {
		stage           string
		index           int
		offset          uintptr
		id, code, flags int
	}{
		{handshakeFront, 0, 0, 0, 0, 16},
		{"disconnected", 5, 8, 7, 42, 32},
		{handshakeAuth, 1, 0x18, 7, 42, 4},
		{handshakeLogin, 2, 0x20, 7, 42, 4},
	} {
		c := nativeSPIObject(t)
		method := spiMethod(t, c, tc.offset)
		h := newHandshake(10*time.Second, nil)
		stage := tc.stage
		if stage == "disconnected" {
			stage = handshakeConfirm
		}
		if err := h.prepare(stage, tc.id); err != nil {
			t.Fatal(err)
		}
		c.registerHandshakeCallbacks(h)
		thread, _, _ := run.Call(method, c.spi, uintptr(tc.id), uintptr(tc.code), uintptr(tc.flags))
		if thread == 0 {
			t.Fatal("阶段线程夹具失败", tc.stage)
		}
		entry := h.entries.stages[tc.index].Load()
		if entry == nil || entry.count != 1 || entry.code != int32(tc.code) {
			t.Fatal("真实阶段入口丢失", tc.stage, entry)
		}
		h.mu.Lock()
		complete, failed, aborted := h.complete, h.err != nil, h.abortErr != nil
		h.mu.Unlock()
		if !complete || (tc.stage == "disconnected" && !aborted) || (tc.stage != handshakeFront && tc.stage != "disconnected" && !failed) {
			t.Fatal("真实阶段错误未传播", tc.stage, complete, failed, aborted)
		}
		runtime.KeepAlive(c)
	}
}

// TestNativeHandshakeSPIErrorDeadline 分开验证有效错误传播与真实迟到回调的正确拒绝。
func TestNativeHandshakeSPIErrorDeadline(t *testing.T) {
	run := buildSPIBridge(t)
	for _, expired := range []bool{false, true} {
		c := nativeSPIObject(t)
		method := spiMethod(t, c, 0x240)
		var traceMu sync.Mutex
		var events []string
		h := newHandshake(10*time.Second, func(d handshakeDiagnostic) {
			traceMu.Lock()
			events = append(events, d.event)
			traceMu.Unlock()
		})
		if err := h.prepare(handshakeConfirm, 7); err != nil {
			t.Fatal(err)
		}
		c.registerHandshakeCallbacks(h)
		if expired {
			// 直接构造已耗尽的测试期限，不sleep，不改生产时钟或阀门。
			h.mu.Lock()
			h.deadline = time.Now().Add(-time.Nanosecond)
			h.mu.Unlock()
		}
		thread, _, _ := run.Call(method, c.spi, 7, 42, 12)
		if thread == 0 {
			t.Fatal("真实截止线程失败")
		}
		h.mu.Lock()
		complete, failed := h.complete, h.err != nil
		h.mu.Unlock()
		entry := h.entries.stages[4].Load()
		if entry == nil || entry.count != 1 || entry.id != 7 || entry.code != 42 {
			t.Fatal("真实迟到入口记录丢失", entry)
		}
		if complete != !expired || failed != !expired {
			t.Fatal("真实通用错误的截止边界错误", expired, complete, failed)
		}
		traceMu.Lock()
		last := events[len(events)-1]
		traceMu.Unlock()
		if (expired && last != "ignored-late") || (!expired && last != "response") {
			t.Fatal("真实截止过滤原因未留证", expired, last)
		}
		runtime.KeepAlive(c)
	}
}
