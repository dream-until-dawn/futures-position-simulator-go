//go:build windows

package ctp

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
)

// Credentials 是连上 SimNow 要的那几样。⚠️ 一律来自 .env，不进代码、不进夹具。
type Credentials struct {
	Front    string // tcp://host:port
	BrokerID string
	UserID   string
	Password string
	AppID    string
	AuthCode string
}

// Client 是一条 CTP 交易连接。
//
// 支持连接、认证、登录、确认结算单及查询；报单只经 Insert 的安全阀路径。
// 只读查询具有独立传输边界，不能借查询测试接口绕过报单安全阀。
type Client struct {
	cred Credentials
	// Valve 是下单安全阀。⚠️ 它是 Client 的字段而不是 Insert 的参数：
	// 参数可以在某一次调用里忘了传，字段不会。
	Valve safety.Valve
	logf  func(string, ...any)

	h        *syscall.DLL
	api, spi uintptr
	// nativeSDKModule 只在真实加载路径及双DLL哈希复核后设置。
	nativeSDKModule uintptr

	mu     sync.Mutex
	nReq   int
	closed bool

	// ⚠️ 回调用 syscall.NewCallback 注册，而 Go 的 GC **不认识**
	// C 侧持有的那个函数指针。keep 把它们钉住，否则回调可能在
	// 第一次被调用之前就被回收 —— 那种崩溃看起来像 DLL 的问题。
	keep []any

	tradingDay     string
	hs             *handshakeCollector
	connectStarted bool
	setupMu        sync.Mutex
	// 私有注入仅覆盖握手初始化和发送，不暴露任何报单方法。
	handshakeStart func(*handshakeCollector)
	handshakeSend  func(*handshakeCollector, string, int) int32
	q              queryer
	// querySend 是只读查询的传输边界；nil 时使用 DLL，测试可注入确定性的回调。
	// 报单不经过此字段，也不能借此绕过 Insert 的安全阀。
	querySend      func(string, unsafe.Pointer, int) int32
	positionRows   queryBatch[def.CThostFtdcInvestorPositionField]
	orderRows      queryBatch[def.CThostFtdcOrderField]
	tradeRows      queryBatch[def.CThostFtdcTradeField]
	settlementRows queryBatch[def.CThostFtdcSettlementInfoField]
	accountRows    queryBatch[def.CThostFtdcTradingAccountField]
	commissionRows queryBatch[def.CThostFtdcInstrumentCommissionRateField]
	instrumentRows queryBatch[def.CThostFtdcInstrumentField]
	marketRows     queryBatch[def.CThostFtdcDepthMarketDataField]
	brokerRows     queryBatch[def.CThostFtdcBrokerTradingParamsField]
	book           orderBook
	// orderSeq 是本次会话的委托序号，**单调递增**。⚠️ 见 send 里的理由：
	// 不递增的 OrderRef 会被 CTP 拒（ErrorID=22），而它只在第二笔单上暴露。
	orderSeq int64
	// frontID / sessionID 来自登录应答，**撤单必须带**：
	// CTP 用 (FrontID, SessionID, OrderRef) 三元组定位一笔委托。
	// ⚠️ 少带一个不会报「参数缺失」，会报「找不到委托」——
	// 而那与「这笔单已经不在了」长得一模一样。
	frontID   int
	sessionID int
}

// New 建一个尚未连接的客户端。
func New(cred Credentials, logf func(string, ...any)) *Client {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Client{cred: cred, logf: logf}
}

// TradingDay 返回柜台报的交易日；登录之前是空串。
func (c *Client) TradingDay() string { c.mu.Lock(); defer c.mu.Unlock(); return c.tradingDay }

func (c *Client) call(name string, args ...uintptr) uintptr {
	r, _, _ := c.h.MustFindProc(name).Call(args...)
	return r
}

// nextRequestID 在发送前分配编号，使收集器先就位再等待可能立即到来的回调。
func (c *Client) nextRequestID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nReq++
	return c.nReq
}

// on 注册一个回调，并把它钉在 c.keep 里防止被 GC。
func (c *Client) on(name string, fn any) {
	c.keep = append(c.keep, fn)
	c.h.MustFindProc(name).Call(c.spi, syscall.NewCallback(fn))
}

func errOf(info *def.CThostFtdcRspInfoField) error {
	if info == nil || info.ErrorID == 0 {
		return nil
	}
	return fmt.Errorf("ErrorID=%d %s", info.ErrorID, text(info.ErrorMsg[:]))
}

// Connect 连上前置并完成 认证 → 登录 → 确认结算单。
//
// ⚠️ 确认结算单是登录之后**必须**做的一步：CTP 在未确认时会拒掉一部分查询。
// 而拒掉的表现可能是**回调根本不来**，不是返回一个错误 ——
// 那种「什么都没发生」最难查，所以这里把它做进连接流程，不留给调用方记得。
func (c *Client) Connect(timeout time.Duration) error {
	if timeout <= 0 {
		return fmt.Errorf("连接超时必须为正")
	}
	hs := newHandshake(timeout, func(d handshakeDiagnostic) {
		c.logf("[ctp.handshake] stage=%s request=%d event=%s code=%d elapsed=%s received_stage=%s received_request=%d last=%t has_payload=%t", d.stage, d.id, d.event, d.code, d.elapsed, d.receivedStage, d.receivedID, d.last, d.hasPayload)
	})
	c.mu.Lock()
	if c.closed || c.connectStarted {
		c.mu.Unlock()
		return fmt.Errorf("Client已关闭或已尝试Connect，必须新建客户端")
	}
	c.connectStarted, c.hs = true, hs
	c.mu.Unlock()
	defer func() { hs.seal(); hs.logEntries(c.logf) }()
	// 初始化、发送及Release互斥，Close先唤醒握手，再等待原生调用退出。
	c.setupMu.Lock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	var err error
	if closed {
		err = fmt.Errorf("Client已关闭")
	} else if c.handshakeStart != nil || c.handshakeSend != nil {
		if c.handshakeStart == nil || c.handshakeSend == nil {
			err = fmt.Errorf("握手传输注入不完整")
		}
	} else {
		err = c.initializeHandshake(hs)
	}
	c.setupMu.Unlock()
	if err != nil {
		return err
	}
	start := func() {
		c.setupMu.Lock()
		defer c.setupMu.Unlock()
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			hs.abort("closed", 0)
			return
		}
		if c.handshakeStart != nil {
			c.handshakeStart(hs)
		} else {
			c.call("Init", c.api)
		}
	}
	send := func(stage string, id int) int32 {
		c.setupMu.Lock()
		defer c.setupMu.Unlock()
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if closed {
			hs.abort("closed", 0)
			return 0
		}
		if c.handshakeSend != nil {
			return c.handshakeSend(hs, stage, id)
		}
		return c.sendHandshake(stage, id)
	}
	login, err := hs.run(start, c.nextRequestID, send)
	if err != nil {
		return err
	}
	n, _ := parseMaxOrderRef(login.maxOrderRef) // 执行器已拒绝缺失、负数及无法递增的引用。
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("Client已关闭，不发布握手结果")
	}
	c.tradingDay, c.frontID, c.sessionID = login.day, login.frontID, login.sessionID
	atomic.StoreInt64(&c.orderSeq, n)
	c.mu.Unlock()
	c.logf("[ctp] 握手完整成功，交易日=%s，最大报单引用=%d", login.day, n)
	return nil
}

// initializeHandshake 只接线，不在DLL回调中推进下一阶段。
func (c *Client) initializeHandshake(hs *handshakeCollector) error {
	dir, how, err := DLLDir()
	if err != nil {
		return err
	}
	if err := CheckDLLs(dir); err != nil {
		return err
	}
	if err := validateNativeHandshakeDLLs(dir); err != nil {
		return err
	}
	c.logf("[ctp] DLL 目录 %s（来自 %s）", dir, how)

	// ⚠️ CreateApi 会在**当前工作目录**下写 CTP 的流文件，目录不存在就直接崩
	// （RuntimeError: can not open CFlow file，接一个 0xc0000005）。
	// 这不是可选的整洁措施，是能不能启动的前提。
	if err := os.MkdirAll("log", 0o755); err != nil {
		return fmt.Errorf("建流文件目录失败：%w", err)
	}
	// ⚠️ 必须 chdir 过去再 load：ctp_trade.dll 依赖**同目录**的 thosttraderapi_se.dll，
	// 而 Windows 的 DLL 搜索路径不含被加载 DLL 自己的目录。
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	h, lerr := syscall.LoadDLL("ctp_trade.dll")
	_ = os.Chdir(wd)
	if lerr != nil {
		return fmt.Errorf("加载 ctp_trade.dll 失败（目录 %s）：%w", dir, lerr)
	}
	c.h = h
	c.nativeSDKModule, err = loadedNativeSDK(dir, uintptr(h.Handle))
	if err != nil {
		return err
	}
	c.api = c.call("CreateApi")
	c.spi = c.call("CreateSpi")
	if c.api == 0 || c.spi == 0 {
		return fmt.Errorf("CTP初始化未取得有效API/SPI句柄")
	}
	// ⚠️ 这里**不写** uintptr(unsafe.Pointer(c.spi))：c.spi 本来就是 uintptr，
	// 那个来回转是个恒等式，而 go vet 会（正确地）把它标成 possible misuse。
	// goctp 原样写着那一句，照抄会把一条本可以干净的 vet 变成有噪声的 vet ——
	// **而有噪声的检查最后一定会被忽略**。
	c.call("RegisterSpi", c.api, c.spi)

	c.registerHandshakeCallbacks(hs)

	c.registerQueryCallbacks()
	c.registerOrderCallbacks()
	c.registerMarketDataCallback()

	bs, err := syscall.BytePtrFromString(c.cred.Front)
	if err != nil {
		return err
	}
	c.call("RegisterFront", c.api, uintptr(unsafe.Pointer(bs)))
	c.call("SubscribePrivateTopic", c.api, uintptr(def.THOST_TERT_RESTART))
	c.call("SubscribePublicTopic", c.api, uintptr(def.THOST_TERT_RESTART))
	return nil
}

// handshakeErrorID 不把柜台自由文本带入握手诊断或错误返回。
func handshakeErrorID(info *def.CThostFtdcRspInfoField) int32 {
	if info == nil {
		return 0
	}
	return int32(info.ErrorID)
}

// sendHandshake 使用执行器预先登记的编号，保留原生即时返回码。
func (c *Client) sendHandshake(stage string, id int) int32 {
	var name string
	var pointer unsafe.Pointer
	switch stage {
	case handshakeAuth:
		f := &def.CThostFtdcReqAuthenticateField{}
		copy(f.BrokerID[:], c.cred.BrokerID)
		copy(f.UserID[:], c.cred.UserID)
		copy(f.AppID[:], c.cred.AppID)
		copy(f.AuthCode[:], c.cred.AuthCode)
		name, pointer = "ReqAuthenticate", unsafe.Pointer(f)
	case handshakeLogin:
		f := &def.CThostFtdcReqUserLoginField{}
		copy(f.BrokerID[:], c.cred.BrokerID)
		copy(f.UserID[:], c.cred.UserID)
		copy(f.Password[:], c.cred.Password)
		name, pointer = "ReqUserLogin", unsafe.Pointer(f)
	case handshakeConfirm:
		f := &def.CThostFtdcSettlementInfoConfirmField{}
		copy(f.BrokerID[:], c.cred.BrokerID)
		copy(f.InvestorID[:], c.cred.UserID)
		copy(f.AccountID[:], c.cred.UserID)
		name, pointer = "ReqSettlementInfoConfirm", unsafe.Pointer(f)
	default:
		return -1
	}
	result, err := invokeNativeHandshake(c.api, name, pointer, id)
	if err != nil {
		c.logf("[ctp.handshake.native] event=binding-error")
		return nativeHandshakeBindingError
	}
	runtime.KeepAlive(pointer)
	return int32(result)
}

// BrokerParams 查经纪商交易参数（simnow_pending#9）。
//
// ⚠️ 它返回的是**声明**，不是行为：查到的是柜台配置成什么，
// 而「它是否真按这个算」要有持仓才验得了。见 docs/ctp-oracle.md 第 3 节。
func (c *Client) BrokerParams(timeout time.Duration) (*def.CThostFtdcBrokerTradingParamsField, error) {
	c.q.wait()
	defer c.q.done()
	f := def.CThostFtdcQryBrokerTradingParamsField{}
	copy(f.BrokerID[:], c.cred.BrokerID)
	copy(f.InvestorID[:], c.cred.UserID)
	rows, err := queryRows(c, &c.brokerRows, "ReqQryBrokerTradingParams", unsafe.Pointer(&f), timeout)
	if err != nil {
		return nil, err
	}
	return uniqueQueryRow(rows, nil)
}

// Close 释放连接。
func (c *Client) Close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	hs := c.hs
	c.mu.Unlock()
	if hs != nil {
		hs.abort("closed", 0)
	}
	c.abortQueries()
	c.setupMu.Lock()
	defer c.setupMu.Unlock()
	if c.h == nil || c.api == 0 {
		return
	}
	// DLL不卸载：Release只释放API，沿用原有进程生命周期约束。
	c.call("Release", c.api)
}
