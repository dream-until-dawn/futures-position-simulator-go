//go:build windows && amd64

package ctp

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// queryFixtureSlots 使用已核DLL的独立字面表，不从生产白名单派生测试预言。
var queryFixtureSlots = map[string]uintptr{
	"ReqQryOrder": 0x140, "ReqQryTrade": 0x148, "ReqQryInvestorPosition": 0x150,
	"ReqQryTradingAccount": 0x158, "ReqQryInstrumentCommissionRate": 0x178,
	"ReqQryInstrument": 0x190, "ReqQryDepthMarketData": 0x198,
	"ReqQrySettlementInfo": 0x1a0, "ReqQryBrokerTradingParams": 0x2c0,
}

// queryFakeAPI 只有本进程方法，不创建真实API/SPI或连接。
type queryFakeAPI struct {
	table [89]uintptr
	api   [1]uintptr
	keep  []any
}

func newQueryFakeAPI(call func(string, uintptr, uintptr, int) int32) *queryFakeAPI {
	f := &queryFakeAPI{}
	for name, offset := range queryFixtureSlots {
		fn := func(api, request, id uintptr) uintptr {
			return uintptr(uint32(call(name, api, request, int(id))))
		}
		f.keep = append(f.keep, fn)
		f.table[offset/8] = syscall.NewCallback(fn)
	}
	f.api[0] = uintptr(unsafe.Pointer(&f.table[0]))
	return f
}

func (f *queryFakeAPI) address() uintptr { return uintptr(unsafe.Pointer(&f.api[0])) }

// TestNativeQueryRowsKeepActualCodes 用真实清零包装复现旧行为，最终核实际queryRows接线。
func TestNativeQueryRowsKeepActualCodes(t *testing.T) {
	dll := nativeFixtureDLL(t)
	for name := range queryFixtureSlots {
		for _, code := range []int32{0, -1, -2, -3, 42} {
			var batch queryBatch[int]
			request, calls := 7, 0
			var f *queryFakeAPI
			f = newQueryFakeAPI(func(got string, api, pointer uintptr, id int) int32 {
				calls++
				if got != name || api != f.address() || pointer != uintptr(unsafe.Pointer(&request)) || id != 1 {
					t.Error("查询原生参数或方法串位", got, name, id)
				}
				answer := 42
				batch.receive(id, &answer, nil, true)
				return code
			})
			c := &Client{h: dll, api: f.address(), nativeQueryModule: queryFixtureModule(t, f.table[queryFixtureSlots[name]/8])}
			c.logf = func(string, ...any) {}
			rows, err := queryRows(c, &batch, name, unsafe.Pointer(&request), time.Second)
			runtime.KeepAlive(f)
			if calls != 1 {
				t.Fatal("查询实际发送次数错误", calls)
			}
			if code == 0 {
				if err != nil || len(rows) != 1 || rows[0] != 42 {
					t.Fatal("原生查询成功未返回完整结果", rows, err)
				}
			} else if err == nil || rows != nil || !strings.Contains(err.Error(), "返回码") {
				t.Fatalf("原生查询即时拒绝被清零或同步回调掩盖 name=%s code=%d rows=%v err=%v", name, code, rows, err)
			}
		}
	}
}

// queryFixtureModule 假方法属于本测试进程映像，显式核实际AllocationBase，不放宽生产检查。
func queryFixtureModule(t *testing.T, method uintptr) uintptr {
	t.Helper()
	var region nativeMemoryRegion
	n, _, _ := queryHandshakeMemory.Call(method, uintptr(unsafe.Pointer(&region)), unsafe.Sizeof(region))
	if n != unsafe.Sizeof(region) || region.allocationBase == 0 {
		t.Fatal("离线假方法模块不可确认")
	}
	return region.allocationBase
}

// TestNativeQueryLegacyClearing 单独保留真实旧导出清零证据，不经过新的生产适配。
func TestNativeQueryLegacyClearing(t *testing.T) {
	dll := nativeFixtureDLL(t)
	for name := range queryFixtureSlots {
		calls := 0
		f := newQueryFakeAPI(func(got string, _, _ uintptr, _ int) int32 {
			calls++
			if got != name {
				t.Error("真实旧导出调用错方法", got, name)
			}
			return -3
		})
		request := 1
		got, _, _ := dll.MustFindProc(name).Call(f.address(), uintptr(unsafe.Pointer(&request)), 7)
		runtime.KeepAlive(f)
		if calls != 1 || int32(got) != 0 {
			t.Fatal("旧查询导出清零实证与固定DLL不符", name, calls, got)
		}
	}
}

func TestNativeQueryModuleAndPointerGuards(t *testing.T) {
	calls := 0
	f := newQueryFakeAPI(func(string, uintptr, uintptr, int) int32 { calls++; return 0 })
	request := 1
	module := queryFixtureModule(t, f.table[0x140/8])
	for _, test := range []struct {
		api, module uintptr
		name        string
		request     unsafe.Pointer
		id          int
		why         string
	}{
		{f.address(), module + 1, "ReqQryOrder", unsafe.Pointer(&request), 1, "不属于已核SDK映像"},
		{f.address(), 0, "ReqQryOrder", unsafe.Pointer(&request), 1, "请求参数无效"},
		{0, module, "ReqQryOrder", unsafe.Pointer(&request), 1, "请求参数无效"},
		{1, module, "ReqQryOrder", unsafe.Pointer(&request), 1, "指针不可读"},
		{f.address(), module, "ReqOrderInsert", unsafe.Pointer(&request), 1, "未登记"},
		{f.address(), module, "ReqQryOrder", nil, 1, "请求参数无效"},
		{f.address(), module, "ReqQryOrder", unsafe.Pointer(&request), 0, "请求参数无效"},
		{f.address(), module, "ReqQryOrder", unsafe.Pointer(&request), -1, "请求参数无效"},
		{f.address(), module, "ReqQryOrder", unsafe.Pointer(&request), 2147483648, "请求参数无效"},
	} {
		if _, err := invokeNativeQuery(test.api, test.name, test.request, test.id, test.module); err == nil || !strings.Contains(err.Error(), test.why) {
			t.Fatal("原生查询非法参数或模块未拒绝", test.name, test.id)
		}
	}
	if calls != 0 {
		t.Fatal("非法查询仍进入原生方法", calls)
	}
	// 空方法及不可执行的数据页都只返回错误，不能靠跳入数据导致进程崩溃检出。
	for _, pointer := range []uintptr{0, uintptr(unsafe.Pointer(&request))} {
		f.table[0x140/8] = pointer
		_, err := invokeNativeQuery(f.address(), "ReqQryOrder", unsafe.Pointer(&request), 1, module)
		if err == nil || (pointer != 0 && !strings.Contains(err.Error(), "不在可执行内存页")) {
			t.Fatal("原生查询空或非执行方法未拒绝")
		}
	}
	f.api[0] = ^uintptr(0) - 1
	if _, err := invokeNativeQuery(f.address(), "ReqQryOrder", unsafe.Pointer(&request), 1, module); err == nil || !strings.Contains(err.Error(), "偏移溢出") {
		t.Fatal("原生查询虚表偏移溢出未拒绝")
	}
	runtime.KeepAlive(f)
}

func TestLoadedNativeSDKPaths(t *testing.T) {
	dll := nativeFixtureDLL(t)
	dir, _, err := DLLDir()
	if err != nil {
		t.Fatal(err)
	}
	module, err := loadedNativeSDK(dir, uintptr(dll.Handle))
	if err != nil || module == 0 {
		t.Fatal("真实已核SDK模块无法确认", err)
	}
	if _, err := loadedNativeSDK(filepath.Join(dir, "unregistered"), uintptr(dll.Handle)); err == nil || !strings.Contains(err.Error(), "路径与登记不符") {
		t.Fatal("实际加载路径错配未拒绝")
	}
	if _, err := nativeModulePath(0); err == nil {
		t.Fatal("空模块句柄被当作当前EXE")
	}
	if _, err := nativeModulePath(1); err == nil {
		t.Fatal("非法模块句柄未拒绝")
	}
	// 这里只是假定路径值的纯文件校验，不把伪造路径当作真实加载证明。
	bad := t.TempDir()
	for _, name := range []string{"ctp_trade.dll", "thosttraderapi_se.dll"} {
		if err := os.WriteFile(filepath.Join(bad, name), []byte("unverified"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateLoadedModuleFiles(bad, filepath.Join(bad, "ctp_trade.dll"), filepath.Join(bad, "thosttraderapi_se.dll")); err == nil || !strings.Contains(err.Error(), "DLL未验收") {
		t.Fatal("加载后文件哈希未复核", err)
	}
}

func TestNativeQueriesCloseLifecycle(t *testing.T) {
	c := New(Credentials{}, nil)
	var b queryBatch[int]
	// 使用真实九个批次之一，使Close实际唤醒它，不拿孤立batch冒充生产接线。
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	c.querySend = func(string, unsafe.Pointer, int) int32 { close(entered); <-release; return 0 }
	result := make(chan error, 1)
	request := 1
	go func() {
		_, err := queryRows(c, &c.accountRows, "ReqQryTradingAccount", unsafe.Pointer(&request), time.Second)
		result <- err
	}()
	<-entered
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	deadline := time.After(time.Second)
	for {
		c.mu.Lock()
		stopped := c.closed
		c.mu.Unlock()
		if stopped {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Close未标记关闭")
		default:
			runtime.Gosched()
		}
	}
	select {
	case <-closed:
		t.Fatal("Close未等待在途原生发送")
	default:
	}
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, errQueryClosed) {
			t.Fatal("Close未唤醒实际查询失败", err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭查询仍等超时")
	}
	<-closed
	c.querySend = func(string, unsafe.Pointer, int) int32 { t.Error("关闭后仍调用查询发送器"); return 0 }
	if rows, err := queryRows(c, &b, "ReqQryOrder", unsafe.Pointer(&request), time.Second); err == nil || rows != nil {
		t.Fatal("已关闭查询仍返回成功")
	}
}
