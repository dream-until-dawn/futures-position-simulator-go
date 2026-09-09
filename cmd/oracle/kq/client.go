// Package kq 是快期模拟（天勤）的 DIFF 协议客户端。
//
// DIFF = JSON over WebSocket + JSON Merge Patch（RFC 7386）。服务端推 rtn_data，
// 客户端把补丁合并进本地业务截面，然后再发一个 peek_message 换下一批。
//
// 它只做一件事：把柜台的业务截面**原样**搬到本地。任何解释、换算、口径调整都不在这里
// ——判别实验要的就是柜台原话，中间加工一层就等于把待验证的假设混进了证据。
package kq

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	authURL    = "https://auth.shinnytech.com/auth/realms/shinnytech/protocol/openid-connect/token"
	brokerFile = "https://files.shinnytech.com/%s.json"
	nsURL      = "https://api.shinnytech.com/ns"
	clientID   = "shinny_tq"

	// BrokerKQ 是快期模拟的经纪商标识，登录报文的 bid 用它。
	BrokerKQ = "快期模拟"

	userAgent = "futures-position-simulator-go/oracle"
)

// Credentials 是连接快期所需的凭据，来自 .env，不入库。
type Credentials struct {
	User     string
	Password string

	// ClientSecret 是 TqSdk 这个 OAuth 客户端的身份，**不是使用者的密钥**。
	//
	// ⚠️ 它虽然公开在 tqsdk-python 的开源代码里，本仓库仍然不内置它：
	// 那是**对方 SDK 的身份**，把它抄进一个公开仓库等于让本仓成为转发点。
	// 取值位置见 .env.example。
	ClientSecret string
}

// Client 持有一条交易连接与一条行情连接，以及各自的业务截面。
//
// 不并发安全的部分都在 mu 后面。探针是单线程驱动的，加锁只为读截面时不撕裂。
type Client struct {
	cred   Credentials
	token  string
	authID string // JWT 的 sub，同时是快期模拟的 user_name 与 password

	tdConn *websocket.Conn
	mdConn *websocket.Conn
	ctx    context.Context // 读循环用；随 Close 取消
	cancel context.CancelFunc

	mu       sync.RWMutex
	tdSnap   map[string]any // 交易业务截面
	mdSnap   map[string]any // 行情业务截面
	notified []Notify
	// seenNotify 给通知**去重后打日志**用，键是 码+文案。
	//
	// ⚠️ DIFF 每次把整张 notify 表重发，不去重会把日志淹掉。
	seenNotify map[string]bool

	tdUpdated chan struct{} // 每次合并完补丁后广播
	mdUpdated chan struct{}

	// deadTd / deadMd 记录读循环**终止的原因**。
	//
	// ⚠️ 它们存在的理由是一次真实的漏测：连接断了（`failed to read frame header: EOF`），
	// 读循环打印了一行日志就退出了，而**没有任何人被告知**。
	// 上层的盯盘还在按秒采样，采到的永远是最后那份截面 ——
	// 于是「连接死了」与「什么都没变」在日志上长得一模一样，
	// 而结算恰好发生在那段时间里。
	//
	// ⚠️ 靠「多久没消息」判断不行：休市时长时间没消息是**正常**的。
	// 而读循环**已经看见了那个 EOF** —— 准确的信号一直在，只是没被暴露出来。
	deadTd error
	deadMd error

	logf func(string, ...any)
}

// Notify 是柜台推来的通知，登录成败也走它。
type Notify struct {
	Type    string `json:"type"`
	Level   string `json:"level"`
	Code    int    `json:"code"`
	Content string `json:"content"`
}

// New 建一个尚未连接的客户端。
func New(cred Credentials, logf func(string, ...any)) *Client {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Client{
		cred:      cred,
		tdSnap:    map[string]any{},
		mdSnap:    map[string]any{},
		tdUpdated: make(chan struct{}, 1),
		mdUpdated: make(chan struct{}, 1),
		logf:      logf,
	}
}

// AuthID 返回本账户的 auth id（JWT 的 sub）。它同时是快期模拟的账号与密码。
//
// ⚠️ 它是 UUID，**属于必须脱敏的字段**，不要写进夹具。见 sanitize.go。
func (c *Client) AuthID() string { return c.authID }

// Auth 取访问令牌并解出 authID。
func (c *Client) Auth(ctx context.Context) error {
	if c.cred.ClientSecret == "" {
		return fmt.Errorf("KQ_CLIENT_SECRET 为空；" +
			"它是 TqSdk 这个 OAuth 客户端的身份，不是你的密钥，本仓库不内置它。" +
			"取值：tqsdk-python 的 tqsdk/auth.py，_request_token 里的 client_secret" +
			"（https://raw.githubusercontent.com/shinnytech/tqsdk-python/master/tqsdk/auth.py），" +
			"填进 .env 的 KQ_CLIENT_SECRET=")
	}
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {c.cred.ClientSecret},
		"grant_type":    {"password"},
		"username":      {c.cred.User},
		"password":      {c.cred.Password},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("鉴权请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("鉴权失败 HTTP %d", resp.StatusCode) // 不回显响应体，可能含账号信息
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("鉴权成功但未返回 access_token")
	}
	c.token = tok.AccessToken

	parts := strings.Split(tok.AccessToken, ".")
	if len(parts) < 2 {
		return fmt.Errorf("access_token 不是 JWT 形态")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("解 JWT 载荷失败: %w", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return err
	}
	if claims.Sub == "" {
		return fmt.Errorf("JWT 缺 sub")
	}
	c.authID = claims.Sub
	if c.ctx == nil {
		c.ctx, c.cancel = context.WithCancel(context.Background())
	}
	c.logf("[auth] OK")
	return nil
}

func (c *Client) headers() http.Header {
	return http.Header{
		"User-Agent":    {userAgent},
		"Accept":        {"application/json"},
		"Authorization": {"Bearer " + c.token},
	}
}

func (c *Client) getJSON(ctx context.Context, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header = c.headers()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s -> HTTP %d", u, resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// TradeURL 解析快期模拟的交易网关地址。
func (c *Client) TradeURL(ctx context.Context) (string, error) {
	u := fmt.Sprintf(brokerFile, url.PathEscape(BrokerKQ)) + "?" +
		url.Values{"account_id": {c.authID}, "auth": {c.cred.User}}.Encode()
	var brokers map[string]struct {
		URL      string   `json:"url"`
		Category []string `json:"category"`
	}
	if err := c.getJSON(ctx, u, &brokers); err != nil {
		return "", err
	}
	b, ok := brokers[BrokerKQ]
	if !ok || b.URL == "" {
		return "", fmt.Errorf("响应里没有 %s 的网关地址", BrokerKQ)
	}
	return b.URL, nil
}

// QuoteURL 解析行情网关地址。
func (c *Client) QuoteURL(ctx context.Context) (string, error) {
	var r struct {
		MdURL string `json:"mdurl"`
	}
	u := nsURL + "?" + url.Values{"stock": {"false"}, "backtest": {"false"}}.Encode()
	if err := c.getJSON(ctx, u, &r); err != nil {
		return "", err
	}
	if r.MdURL == "" {
		return "", fmt.Errorf("服务发现未返回 mdurl")
	}
	return r.MdURL, nil
}

// dial 建一条 WebSocket。
//
// ⚠️ **必须开 permessage-deflate**，否则天勤的行情网关直接回 400 Bad Request。
// 而且必须用 coder/websocket 而不是 gorilla/websocket：服务端应答
//
//	Sec-WebSocket-Extensions: permessage-deflate; server_max_window_bits="15"
//
// gorilla 的协商解析器不接受 server_max_window_bits，会在**握手已经 101 之后**
// 报 "invalid compression negotiation" 断开。这个失败很难查：不开压缩是 400，
// 开了压缩是 101 后失败，两种症状都不指向真正的原因。
func (c *Client) dial(ctx context.Context, wsURL string) (*websocket.Conn, error) {
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		CompressionMode: websocket.CompressionContextTakeover,
		HTTPHeader:      c.headers(),
	})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		return nil, fmt.Errorf("dial %s 失败 (http=%d): %w", wsURL, code, err)
	}
	conn.SetReadLimit(64 << 20) // 业务截面首帧很大，默认 32KB 不够
	return conn, nil
}

// ConnectTrade 连接交易网关并登录。
//
// ⚠️ 登录成功与否**看业务截面里出没出 trade/{authID}，不看回调**。
// 这条是踩过坑写下的：goctp 的 OnRspUserLogin 要等整条初始化链路跑完才触发，
// 链路卡住时「登录早已成功」看起来像「登录无应答」。判定看状态，不看回调。
func (c *Client) ConnectTrade(ctx context.Context) error {
	u, err := c.TradeURL(ctx)
	if err != nil {
		return err
	}
	c.logf("[td] gateway %s", u)
	conn, err := c.dial(ctx, u)
	if err != nil {
		return err
	}
	c.tdConn = conn
	go c.readLoop(conn, c.tdSnap, c.tdUpdated, "td")

	if err := c.send(conn, map[string]any{
		"aid": "req_login", "bid": BrokerKQ,
		"user_name": c.authID, "password": c.authID,
	}); err != nil {
		return err
	}
	return c.peek(conn)
}

// ConnectQuote 连接行情网关。
func (c *Client) ConnectQuote(ctx context.Context) error {
	u, err := c.QuoteURL(ctx)
	if err != nil {
		return err
	}
	c.logf("[md] gateway %s", u)
	conn, err := c.dial(ctx, u)
	if err != nil {
		return err
	}
	c.mdConn = conn
	go c.readLoop(conn, c.mdSnap, c.mdUpdated, "md")
	return c.peek(conn)
}

func (c *Client) send(conn *websocket.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.baseCtx(), 15*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}

func (c *Client) baseCtx() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c *Client) peek(conn *websocket.Conn) error {
	return c.send(conn, map[string]any{"aid": "peek_message"})
}

func (c *Client) readLoop(conn *websocket.Conn, snap map[string]any, updated chan struct{}, tag string) {
	for {
		_, data, err := conn.Read(c.baseCtx())
		if err != nil {
			c.logf("[%s] 读取结束: %v", tag, err)
			// ⚠️ 记下来，别只打印。打印进日志的东西**没有人在读**，
			// 而上层需要的是一个能查的状态。
			c.mu.Lock()
			if tag == "td" {
				c.deadTd = err
			} else {
				c.deadMd = err
			}
			c.mu.Unlock()
			return
		}
		var pack struct {
			Aid  string `json:"aid"`
			Data []any  `json:"data"`
		}
		if err := json.Unmarshal(data, &pack); err != nil {
			continue
		}
		if pack.Aid != "rtn_data" {
			continue
		}
		c.mu.Lock()
		for _, p := range pack.Data {
			MergePatch(snap, p)
		}
		c.collectNotifyLocked(snap)
		c.mu.Unlock()

		select {
		case updated <- struct{}{}:
		default:
		}
		_ = c.peek(conn)
	}
}

func (c *Client) collectNotifyLocked(snap map[string]any) {
	n, ok := snap["notify"].(map[string]any)
	if !ok {
		return
	}
	c.notified = c.notified[:0]
	for _, v := range n {
		b, err := json.Marshal(v)
		if err != nil {
			continue
		}
		var one Notify
		if json.Unmarshal(b, &one) == nil {
			c.notified = append(c.notified, one)
			c.logNotifyOnce(one)
		}
	}
}

// Notifies 返回当前截面里的通知副本。
func (c *Client) Notifies() []Notify {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Notify, len(c.notified))
	copy(out, c.notified)
	return out
}

// MergePatch 实现 RFC 7386：null 删键，对象递归合并，其余整体替换。
func MergePatch(target map[string]any, patch any) map[string]any {
	p, ok := patch.(map[string]any)
	if !ok {
		return target
	}
	for k, v := range p {
		if v == nil {
			delete(target, k)
			continue
		}
		if sub, ok := v.(map[string]any); ok {
			cur, _ := target[k].(map[string]any)
			if cur == nil {
				cur = map[string]any{}
			}
			target[k] = MergePatch(cur, sub)
			continue
		}
		target[k] = v
	}
	return target
}

// TradeSnapshot 返回交易业务截面的深拷贝。
func (c *Client) TradeSnapshot() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return deepCopy(c.tdSnap)
}

// QuoteSnapshot 返回行情业务截面的深拷贝。
func (c *Client) QuoteSnapshot() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return deepCopy(c.mdSnap)
}

func deepCopy(m map[string]any) map[string]any {
	b, err := json.Marshal(m)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if json.Unmarshal(b, &out) != nil {
		return map[string]any{}
	}
	return out
}

// Dig 沿路径取值，任一层缺失即返回 nil。
func Dig(m any, path ...string) any {
	cur := m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// Account 返回 CNY 资金账户截面。
func (c *Client) Account() map[string]any {
	v, _ := Dig(c.TradeSnapshot(), "trade", c.authID, "accounts", "CNY").(map[string]any)
	return v
}

// Positions 返回全部持仓截面，键为 exchange.instrument。
func (c *Client) Positions() map[string]any {
	v, _ := Dig(c.TradeSnapshot(), "trade", c.authID, "positions").(map[string]any)
	return v
}

// Orders 返回全部委托截面。
func (c *Client) Orders() map[string]any {
	v, _ := Dig(c.TradeSnapshot(), "trade", c.authID, "orders").(map[string]any)
	return v
}

// Trades 返回全部成交截面。
func (c *Client) Trades() map[string]any {
	v, _ := Dig(c.TradeSnapshot(), "trade", c.authID, "trades").(map[string]any)
	return v
}

// TradingDay 返回柜台给的交易日。
//
// ⚠️ 它不是自然日：夜盘 21:00 之后属于下一个交易日，周五夜盘属于下周一。
// 本库任何地方都不自行推算交易日，一律以此为准。
func (c *Client) TradingDay() string {
	v, _ := Dig(c.TradeSnapshot(), "trade", c.authID, "trading_day").(string)
	return v
}

// WaitTrade 阻塞到交易截面下一次更新，或超时。
func (c *Client) WaitTrade(timeout time.Duration) bool { return wait(c.tdUpdated, timeout) }

// WaitQuote 阻塞到行情截面下一次更新，或超时。
func (c *Client) WaitQuote(timeout time.Duration) bool { return wait(c.mdUpdated, timeout) }

func wait(ch chan struct{}, timeout time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// WaitUntil 反复等待交易截面更新，直到 cond 为真或超时。
//
// ⚠️ 返回 false 只说明「在期限内没等到」，**不说明条件为假**。
// 调用方不得把超时读成否定结论——这是本仓库方法论第 1 条。
func (c *Client) WaitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		c.WaitTrade(500 * time.Millisecond)
	}
}

// Close 关闭两条连接。
func (c *Client) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	if c.tdConn != nil {
		_ = c.tdConn.Close(websocket.StatusNormalClosure, "")
	}
	if c.mdConn != nil {
		_ = c.mdConn.Close(websocket.StatusNormalClosure, "")
	}
}

// Dead 报告两条读循环有没有终止，以及终止的原因。
//
// ⚠️ 它回答的是「连接还在不在」，**不是**「最近有没有消息」——
// 后者在休市时长时间为假，而那正是最容易把「死了」读成「安静」的时候。
//
// 用法：任何长时间运行的观测都应当每一拍查一次。
// 不查的话，采到的永远是最后那份截面，而日志上看不出区别。
func (c *Client) Dead() (trade, quote error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.deadTd, c.deadMd
}

// DeadErr 把两条读循环的终止原因合成一个 error；都活着时返回 nil。
func (c *Client) DeadErr() error {
	td, md := c.Dead()
	switch {
	case td != nil && md != nil:
		return fmt.Errorf("交易与行情两条流都断了（交易：%v；行情：%v）", td, md)
	case td != nil:
		return fmt.Errorf("**交易流断了**：%v —— 账户与持仓截面从此不再更新", td)
	case md != nil:
		return fmt.Errorf("行情流断了：%v —— 行情字段从此不再更新", md)
	}
	return nil
}

// logNotifyOnce 把一条通知的**文案**打进本次运行的日志，同一条只打一次。
//
// ⚠️ 这是补一个具体的洞。夹具刻意只收 type/level/code，**不收 content**
// （仓库公开、泄漏不可逆，见 Fixture.Notifies；收不收由使用者裁决）。
// 而守卫 TestNotifyCodesAreKnown 在见到没登记的码时会红，
// 并告诉人「去日志里看它是什么」——
//
//	⚠️ 而在这行日志之前，**根本没有那样一份日志**：Content 被解析出来
//	就地丢掉了。于是那条守卫的补救动作是做不到的，只能等下一次复现。
//	20260909 09:02 就这么发生了一次：新出现 404/417/419/420 四个码，
//	而它们说了什么**已经无从查起**。
//
// ⚠️ 它**不预判**「content 该不该进夹具」那个待裁决：
// 打进本地日志与写进公开仓库是两件事，前者不留档、不推送。
func (c *Client) logNotifyOnce(n Notify) {
	key := fmt.Sprintf("%d|%s", n.Code, n.Content)
	if c.seenNotify == nil {
		c.seenNotify = map[string]bool{}
	}
	if c.seenNotify[key] {
		return
	}
	c.seenNotify[key] = true
	c.logf("[notify] code=%d level=%s type=%s  %s", n.Code, n.Level, n.Type, n.Content)
}
