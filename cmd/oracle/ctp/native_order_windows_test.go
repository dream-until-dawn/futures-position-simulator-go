//go:build windows && amd64

package ctp

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
)

// copyNativeOrderFixture 安全复制假API收到的Go请求，不把uintptr直接解引用。
func copyNativeOrderFixture[T any](address uintptr) (T, error) {
	var out T
	var size uintptr
	ok, _, _ := readHandshakeMemory.Call(^uintptr(0), address, uintptr(unsafe.Pointer(&out)), unsafe.Sizeof(out), uintptr(unsafe.Pointer(&size)))
	if ok == 0 || size != unsafe.Sizeof(out) {
		return out, fmt.Errorf("假订单请求不可读")
	}
	return out, nil
}

func nativeOrderFixture(t *testing.T, code int32, filled bool) (*Client, *queryFakeAPI, *int, *error) {
	t.Helper()
	dll := nativeFixtureDLL(t)
	c := New(Credentials{}, nil)
	c.h, c.frontID, c.sessionID, c.orderSeq, c.tradingDay = dll, 3, 9, 41, "20261030"
	c.Valve = safety.Valve{AllowOrder: true, MaxVolume: 1}
	calls, wrong := new(int), new(error)
	// 订单夹具只分配两个回调，避免无关查询回调耗尽进程级NewCallback表。
	f := &queryFakeAPI{}
	f.api[0] = uintptr(unsafe.Pointer(&f.table[0]))
	insert := func(api, request, id uintptr) uintptr {
		*calls += 1
		row, err := copyNativeOrderFixture[def.CThostFtdcInputOrderField](request)
		if err != nil || api != f.address() || id != 1 || int(row.VolumeTotalOriginal) != 1 || row.CombHedgeFlag[0] != byte(def.THOST_FTDC_HF_Speculation) || text(row.OrderRef[:]) != "42" {
			*wrong = fmt.Errorf("原生报单参数串位")
		}
		if filled {
			c.book.put("42", func(s *OrderState) {
				s.Status = def.THOST_FTDC_OST_AllTraded
				s.VolumeTraded = 1
				s.OrderSysID = "SYNTHETIC"
				s.ErrorID = 77
			})
		}
		return uintptr(uint32(code))
	}
	cancel := func(api, request, id uintptr) uintptr {
		*calls += 1
		row, err := copyNativeOrderFixture[def.CThostFtdcInputOrderActionField](request)
		if err != nil || api != f.address() || id != 1 || row.ActionFlag != def.THOST_FTDC_AF_Delete || text(row.ExchangeID[:]) != "SHFE" || text(row.InstrumentID[:]) != "rb2701" {
			*wrong = fmt.Errorf("原生撤单参数串位")
		}
		if text(row.OrderRef[:]) != "" && (row.FrontID != 3 || row.SessionID != 9) {
			*wrong = fmt.Errorf("本会话撤单身份错配")
		}
		if text(row.OrderSysID[:]) != "" && (row.FrontID != 0 || row.SessionID != 0) {
			*wrong = fmt.Errorf("交易所撤单混用会话身份")
		}
		return uintptr(uint32(code))
	}
	f.keep = append(f.keep, insert, cancel)
	f.table[0xb8/8], f.table[0xd0/8] = syscall.NewCallback(insert), syscall.NewCallback(cancel)
	c.api, c.nativeSDKModule = f.address(), queryFixtureModule(t, f.table[0xb8/8])
	return c, f, calls, wrong
}

func nativeOrderRequest() OrderReq {
	return OrderReq{Exchange: "SHFE", Instrument: "rb2701", Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open, Volume: 1, LimitPrice: 3000}
}

func TestNativeInsertPreservesErrorsAndObservedState(t *testing.T) {
	for _, code := range []int32{0, -1, -2, -3, 17, -10001, -2147483648, 2147483647} {
		c, f, calls, wrong := nativeOrderFixture(t, code, true)
		state, err := c.Insert(nativeOrderRequest(), time.Millisecond)
		runtime.KeepAlive(f)
		if *wrong != nil || *calls != 1 {
			t.Fatal("原生报单参数或次数错误", *wrong, *calls)
		}
		if state.OrderRef != "42" || state.VolumeTraded != 1 || state.OrderSysID != "SYNTHETIC" || state.ErrorID != 77 || state.Status != def.THOST_FTDC_OST_AllTraded {
			t.Fatal("原生报单失败丢失已见成交或引用", state, err)
		}
		if code == 0 && err != nil {
			t.Fatal("原生报单成功被拒", err)
		}
		if code != 0 && (err == nil || !strings.Contains(err.Error(), fmt.Sprintf("code=%d", code))) {
			t.Fatal("原生报单即时失败被当成功或丢码", code, err)
		}
		if code != 0 {
			var detail *NativeOrderError
			if !errors.As(err, &detail) || detail.Code != code || detail.Local || detail.Operation != "insert" {
				t.Fatal("原生报单错误来源或类型不符", err)
			}
		}
	}
}

func TestNativeCancelPreservesErrorsAndState(t *testing.T) {
	for _, code := range []int32{0, -1, -2, -3, 17, -10001} {
		for _, bySystem := range []bool{false, true} {
			c, f, calls, wrong := nativeOrderFixture(t, code, false)
			c.book.put("42", func(s *OrderState) {
				s.Status = def.THOST_FTDC_OST_AllTraded
				s.VolumeTraded = 1
				s.OrderSysID = "SYNTHETIC"
			})
			before, _ := c.Order("42")
			var err error
			if bySystem {
				row := &def.CThostFtdcOrderField{}
				copy(row.ExchangeID[:], "SHFE")
				copy(row.InstrumentID[:], "rb2701")
				copy(row.OrderSysID[:], "SYNTHETIC")
				row.FrontID = 777
				row.SessionID = 888
				err = c.CancelByOrder(row)
			} else {
				err = c.Cancel("42", nativeOrderRequest())
			}
			runtime.KeepAlive(f)
			if *wrong != nil || *calls != 1 {
				t.Fatal("原生撤单参数或次数错误", *wrong, *calls)
			}
			after, _ := c.Order("42")
			if after != before {
				t.Fatal("撤单调用改写已见成交或终态", before, after)
			}
			if code == 0 && err != nil {
				t.Fatal("原生撤单成功被拒", err)
			}
			if code != 0 && (err == nil || !strings.Contains(err.Error(), fmt.Sprintf("code=%d", code))) {
				t.Fatal("原生撤单即时失败被当成功或丢码", code, err)
			}
			if code != 0 {
				var detail *NativeOrderError
				if !errors.As(err, &detail) || detail.Code != code || detail.Local || detail.Operation != "cancel" {
					t.Fatal("原生撤单错误来源或类型不符", err)
				}
			}
		}
	}
}

func TestNativeInsertUnknownAndLocalFailure(t *testing.T) {
	for _, mode := range []string{"native", "local", "zero-without-report", "closed"} {
		code := int32(-3)
		if mode == "zero-without-report" {
			code = 0
		}
		c, f, calls, _ := nativeOrderFixture(t, code, false)
		if mode == "local" {
			c.nativeSDKModule = 0
		}
		if mode == "closed" {
			c.h = nil
			c.Close()
		} // 假API不送给真实SDK的Release。
		state, err := c.Insert(nativeOrderRequest(), time.Millisecond)
		runtime.KeepAlive(f)
		if err == nil || state.OrderRef != "42" || state.Status != 0 || state.VolumeTraded != 0 || state.ErrorID != 0 || state.OrderSysID != "" {
			t.Fatal("原生失败或无回报被洗成终态或丢引用", mode, state, err)
		}
		wantCalls := 1
		if mode == "local" || mode == "closed" {
			wantCalls = 0
		}
		if *calls != wantCalls {
			t.Fatal("原生订单未知后重试或本地失败仍调用SDK", mode, *calls)
		}
		var detail *NativeOrderError
		if mode == "zero-without-report" {
			if errors.As(err, &detail) {
				t.Fatal("发送0的等待超时误当即时拒绝", err)
			}
		} else if !errors.As(err, &detail) || detail.Local != (mode != "native") {
			t.Fatal("本地失败与SDK失败混淆", mode, err)
		}
	}
}

func TestNativeInsertSafetyValveBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"disabled", "oversize", "protected"} {
		c, f, calls, _ := nativeOrderFixture(t, 0, true)
		r := nativeOrderRequest()
		if mode == "disabled" {
			c.Valve.AllowOrder = false
		}
		if mode == "oversize" {
			r.Volume = 2
		}
		if mode == "protected" {
			r.Direction = def.THOST_FTDC_D_Sell
			r.Offset = def.THOST_FTDC_OF_CloseToday
			c.Valve.Protected = []safety.ProtectedLeg{{Symbol: r.Symbol(), Side: safety.Long, TradingDay: "20261030", Volume: 1, Why: "synthetic"}}
		}
		state, err := c.Insert(r, time.Millisecond)
		runtime.KeepAlive(f)
		if err == nil || *calls != 0 || state.OrderRef != "" || c.orderSeq != 41 {
			t.Fatal("报单安全阀前发生派发或分配引用", mode, *calls, state, err)
		}
	}
}

func TestNativeOrderLegacyClearing(t *testing.T) {
	dll := nativeFixtureDLL(t)
	for name, slot := range map[string]int{"ReqOrderInsert": 0xb8 / 8, "ReqOrderAction": 0xd0 / 8} {
		f := &queryFakeAPI{}
		f.api[0] = uintptr(unsafe.Pointer(&f.table[0]))
		calls := 0
		fn := func(uintptr, uintptr, uintptr) uintptr { calls++; code := int32(-3); return uintptr(uint32(code)) }
		f.keep = append(f.keep, fn)
		f.table[slot] = syscall.NewCallback(fn)
		var request [512]byte
		result, _, _ := dll.MustFindProc(name).Call(f.address(), uintptr(unsafe.Pointer(&request[0])), 7)
		runtime.KeepAlive(f)
		runtime.KeepAlive(request)
		if calls != 1 || int32(result) != 0 {
			t.Fatal("固定订单导出清零实证不符", name, calls, result)
		}
	}
}

func TestNativeOrderCloseAndLocalDispatch(t *testing.T) {
	c, f, calls, _ := nativeOrderFixture(t, 0, false)
	c.h = nil // 真实SDK的Release不得处理假API对象。
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var sendErr error
	fn := func(uintptr, uintptr, uintptr) uintptr { *calls += 1; close(entered); <-release; return 0 }
	f.keep = append(f.keep, fn)
	f.table[0xb8/8] = syscall.NewCallback(fn)
	request := def.CThostFtdcInputOrderField{}
	go func() { sendErr = c.sendNativeOrder(nativeOrderInsert, unsafe.Pointer(&request)); close(finished) }()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("原生发送夹具未退出")
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("未进入原生订单夹具")
	}
	if c.setupMu.TryLock() {
		c.setupMu.Unlock()
		t.Fatal("订单发送未持有生命周期锁")
	}
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	deadline := time.After(2 * time.Second)
	for {
		c.mu.Lock()
		stopped := c.closed
		c.mu.Unlock()
		if stopped {
			break
		}
		select {
		case <-deadline:
			t.Fatal("关闭未标记订单客户端")
		default:
			runtime.Gosched()
		}
	}
	select {
	case <-closed:
		t.Fatal("Close未等待原生订单发送")
	default:
	}
	close(release)
	<-finished
	if sendErr != nil {
		t.Fatal("已进入SDK的0返回被改写", sendErr)
	}
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close未在原生订单发送后退出")
	}
	for _, bySystem := range []bool{false, true} {
		var err error
		if bySystem {
			row := &def.CThostFtdcOrderField{}
			copy(row.OrderSysID[:], "SYNTHETIC")
			err = c.CancelByOrder(row)
		} else {
			err = c.Cancel("42", nativeOrderRequest())
		}
		var detail *NativeOrderError
		if !errors.As(err, &detail) || !detail.Local || detail.Code != nativeHandshakeBindingError {
			t.Fatal("关闭后撤单未本地失败", err)
		}
	}
	if *calls != 1 {
		t.Fatal("关闭后原生订单仍被调用或发生重试", *calls)
	}
	runtime.KeepAlive(f)
}
