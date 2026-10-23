//go:build windows

package ctp

import (
	"fmt"
	"os"
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

	mu     sync.Mutex
	nReq   int
	closed bool

	// ⚠️ 回调用 syscall.NewCallback 注册，而 Go 的 GC **不认识**
	// C 侧持有的那个函数指针。keep 把它们钉住，否则回调可能在
	// 第一次被调用之前就被回收 —— 那种崩溃看起来像 DLL 的问题。
	keep []any

	tradingDay string
	loggedIn   chan error
	q          queryer
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
	return &Client{cred: cred, logf: logf,
		loggedIn: make(chan error, 1),
	}
}

// TradingDay 返回柜台报的交易日；登录之前是空串。
func (c *Client) TradingDay() string { return c.tradingDay }

func (c *Client) call(name string, args ...uintptr) uintptr {
	r, _, _ := c.h.MustFindProc(name).Call(args...)
	return r
}

// req 发一笔请求，自增 RequestID。
func (c *Client) req(name string, p unsafe.Pointer) {
	c.h.MustFindProc(name).Call(c.api, uintptr(p), uintptr(c.nextRequestID()))
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
	dir, how, err := DLLDir()
	if err != nil {
		return err
	}
	if err := CheckDLLs(dir); err != nil {
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
	c.api = c.call("CreateApi")
	c.spi = c.call("CreateSpi")
	// ⚠️ 这里**不写** uintptr(unsafe.Pointer(c.spi))：c.spi 本来就是 uintptr，
	// 那个来回转是个恒等式，而 go vet 会（正确地）把它标成 possible misuse。
	// goctp 原样写着那一句，照抄会把一条本可以干净的 vet 变成有噪声的 vet ——
	// **而有噪声的检查最后一定会被忽略**。
	c.call("RegisterSpi", c.api, c.spi)

	c.on("SetOnFrontConnected", func() uintptr {
		c.logf("[ctp] 前置已连接 —— 发认证")
		f := def.CThostFtdcReqAuthenticateField{}
		copy(f.BrokerID[:], c.cred.BrokerID)
		copy(f.UserID[:], c.cred.UserID)
		copy(f.AppID[:], c.cred.AppID)
		copy(f.AuthCode[:], c.cred.AuthCode)
		c.req("ReqAuthenticate", unsafe.Pointer(&f))
		return 0
	})
	c.on("SetOnRspAuthenticate", func(_ *def.CThostFtdcRspAuthenticateField,
		info *def.CThostFtdcRspInfoField, _ int, _ bool) uintptr {
		if err := errOf(info); err != nil {
			c.finishLogin(fmt.Errorf("认证失败：%w", err))
			return 0
		}
		c.logf("[ctp] 认证通过 —— 发登录")
		f := def.CThostFtdcReqUserLoginField{}
		copy(f.BrokerID[:], c.cred.BrokerID)
		copy(f.UserID[:], c.cred.UserID)
		copy(f.Password[:], c.cred.Password)
		c.req("ReqUserLogin", unsafe.Pointer(&f))
		return 0
	})
	c.on("SetOnRspUserLogin", func(lf *def.CThostFtdcRspUserLoginField,
		info *def.CThostFtdcRspInfoField, _ int, _ bool) uintptr {
		if err := errOf(info); err != nil {
			c.finishLogin(fmt.Errorf("登录失败：%w", err))
			return 0
		}
		c.tradingDay = text(lf.TradingDay[:])
		c.frontID, c.sessionID = int(lf.FrontID), int(lf.SessionID)
		// ⚠️ **`MaxOrderRef` 必须读，而漏读它的表现不是报错，是「第二笔单被拒」。**
		// 登录应答里给的是该投资者**当日已经用掉的最大报单引用**，
		// 客户端要从它往后续。20260910 夜盘之前这个字段一直没读 —— 见 probes.md §6.8。
		max := text(lf.MaxOrderRef[:])
		n, ok := parseMaxOrderRef(max)
		if !ok {
			// ⚠️ 解析不了就从 0 起，但要**说出来** —— 一个安静的回退会让
			// 「柜台给的是个怪值」和「柜台给的是空」长得一模一样。
			c.logf("[ctp] ⚠️ MaxOrderRef=%q 解析不出数字，报单引用从 0 起算", max)
			n = 0
		}
		atomic.StoreInt64(&c.orderSeq, n)
		c.logf("[ctp] 登录成功  交易日 %s  MaxOrderRef=%q ⇒ 报单引用从 %d 往后",
			c.tradingDay, max, n+1)
		f := def.CThostFtdcSettlementInfoConfirmField{}
		copy(f.BrokerID[:], c.cred.BrokerID)
		copy(f.InvestorID[:], c.cred.UserID)
		copy(f.AccountID[:], c.cred.UserID)
		c.req("ReqSettlementInfoConfirm", unsafe.Pointer(&f))
		return 0
	})
	c.on("SetOnRspSettlementInfoConfirm", func(_ *def.CThostFtdcSettlementInfoConfirmField,
		info *def.CThostFtdcRspInfoField, _ int, _ bool) uintptr {
		c.logf("[ctp] 结算单确认完成")
		c.finishLogin(errOf(info))
		return 0
	})
	c.on("SetOnRspError", func(info *def.CThostFtdcRspInfoField, id int, _ bool) uintptr {
		if err := errOf(info); err != nil {
			c.logf("[ctp] ⚠️ OnRspError %v", err)
			// 通用错误回调也带请求编号；不能只打印后让调用方误读为空成功。
			c.positionRows.receive(id, nil, err, true)
			c.orderRows.receive(id, nil, err, true)
			c.tradeRows.receive(id, nil, err, true)
			c.settlementRows.receive(id, nil, err, true)
			c.accountRows.receive(id, nil, err, true)
			c.commissionRows.receive(id, nil, err, true)
			c.instrumentRows.receive(id, nil, err, true)
			c.marketRows.receive(id, nil, err, true)
			c.brokerRows.receive(id, nil, err, true)
		}
		return 0
	})

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
	c.call("Init", c.api)

	select {
	case err := <-c.loggedIn:
		return err
	case <-time.After(timeout):
		// ⚠️ 超时是一个**没有结论**的结果，不是「连不上」——
		// 两者要人做的事不同（等一会儿再试 / 去查凭据与前置）。
		return fmt.Errorf("%v 内没有走完 认证→登录→确认结算单，"+
			"⚠️ 这是**没有结论**，不是「登录失败」：前置 %s", timeout, c.cred.Front)
	}
}

func (c *Client) finishLogin(err error) {
	select {
	case c.loggedIn <- err:
	default:
	}
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
	defer c.mu.Unlock()
	if c.closed || c.h == nil {
		return
	}
	c.closed = true
	// ⚠️ 不 Release DLL：goctp 的注释记着「函数结束后会释放导致后续函数执行失败」。
	// 一个进程里只连一次，让它随进程走。
	c.call("Release", c.api)
}
