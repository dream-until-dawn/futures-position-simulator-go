package kq

import (
	"fmt"
	"strings"
	"time"
)

// Direction 买卖方向，取值与 DIFF 线格式一致。
type Direction string

const (
	Buy  Direction = "BUY"
	Sell Direction = "SELL"
)

// Offset 开平标志，取值与 DIFF 线格式一致。
//
// ⚠️ CloseToday 与 Close 的区别不是可选风格：
// PositionDateType = UseHistory 的合约（SHFE / INE / CFFEX）**必须**显式声明平今或平昨；
// NoUseHistory 的合约（DCE / CZCE）只接受 Close。用错会被柜台拒单。
type Offset string

const (
	Open       Offset = "OPEN"
	Close      Offset = "CLOSE"
	CloseToday Offset = "CLOSETODAY"
)

// OrderReq 是一笔限价委托。
//
// 本客户端**只发限价单**。市价单在各交易所的支持程度不一，且成交价不可控——
// 判别实验要的是可复现的输入，成交价必须由我们决定，不能由盘口决定。
type OrderReq struct {
	Exchange   string
	Instrument string
	Direction  Direction
	Offset     Offset
	Volume     int
	LimitPrice float64
}

// Symbol 返回 DIFF 的合约键，形如 SHFE.rb2601。
func (r OrderReq) Symbol() string { return r.Exchange + "." + r.Instrument }

func (r OrderReq) String() string {
	return fmt.Sprintf("%s %s/%s %d手 @%.4f", r.Symbol(), r.Direction, r.Offset, r.Volume, r.LimitPrice)
}

// Guard 是下单的安全阀。探针在真实账户上操作，即便是模拟资金，
// 也不该因为一处参数写错就把账户状态搞脏到后续实验没法做。
type Guard struct {
	AllowOrder bool // 对应 .env 的 PROBE_ALLOW_ORDER
	MaxVolume  int  // 对应 .env 的 PROBE_MAX_VOLUME，单笔手数上限
}

// Check 校验一笔委托是否被安全阀放行。
//
// ⚠️ MaxVolume 放过的是**已识别的平仓**，不是「非开仓」。
//
// 上限原先对所有委托一视同仁，结果是：一次实验意外建到 2 手，
// 收尾平仓要发 2 手的单，被 MaxVolume=1 挡下——**账上留着仓，平不掉**。
// 一个用来防止扩大风险的守卫，反过来阻止了缩小风险。
// 平仓只会让敞口变小或归零，所以它不该受开仓上限约束。
//
// ⚠️ 但修那次故障时我写成了「是开仓才拦」（`r.Offset == Open`），**那是个洞**：
// Offset 的底层类型是 string，零值是 ""，而常量只有三个——
// 于是任何未设置或拼错的 Offset 都绕过了上限，实测 ""、"BUYOPEN"、
// "open"、"CLOSE_TODAY" 全部放行。这是真账户上的安全阀，
// 而它的**失败方向朝着「不拦」**。
//
// 更要记的是这个洞的来历：人在修「阀门太紧」的故障时，会本能地往松了调，
// 而**松的方向恰好是危险的方向**。
//
// 所以判据翻过来：不是已识别的平仓，就按开仓对待、就拦。
// 将来加 CloseYesterday 而漏改这里，后果从「安全阀失效」变成「多拦一次」——
// 后者会立刻被看见。
func (g Guard) Check(r OrderReq) error {
	if !g.AllowOrder {
		return fmt.Errorf("下单被安全阀拦下：PROBE_ALLOW_ORDER 未开启（%s）", r)
	}
	if r.Volume <= 0 {
		return fmt.Errorf("手数必须为正，得到 %d", r.Volume)
	}
	isClose := r.Offset == Close || r.Offset == CloseToday
	if g.MaxVolume > 0 && !isClose && r.Volume > g.MaxVolume {
		return fmt.Errorf("手数 %d 超过上限 PROBE_MAX_VOLUME=%d（开平标志 %q 不是已识别的平仓，按开仓对待）（%s）",
			r.Volume, g.MaxVolume, r.Offset, r)
	}
	if r.LimitPrice <= 0 {
		return fmt.Errorf("限价必须为正，得到 %v（%s）", r.LimitPrice, r)
	}
	return nil
}

// InsertOrder 发一笔限价委托，返回本地委托号。
func (c *Client) InsertOrder(g Guard, r OrderReq) (string, error) {
	if err := g.Check(r); err != nil {
		return "", err
	}
	orderID := fmt.Sprintf("probe-%d", time.Now().UnixNano())
	pack := map[string]any{
		"aid":              "insert_order",
		"user_id":          c.authID,
		"order_id":         orderID,
		"exchange_id":      r.Exchange,
		"instrument_id":    r.Instrument,
		"direction":        string(r.Direction),
		"offset":           string(r.Offset),
		"volume":           r.Volume,
		"price_type":       "LIMIT",
		"limit_price":      r.LimitPrice,
		"volume_condition": "ANY",
		"time_condition":   "GFD",
	}
	c.logf("[order] 发出 %s  order_id=%s", r, orderID)
	if err := c.send(c.tdConn, pack); err != nil {
		return "", err
	}
	return orderID, nil
}

// CancelOrder 撤一笔委托。
func (c *Client) CancelOrder(orderID string) error {
	c.logf("[order] 撤单 order_id=%s", orderID)
	return c.send(c.tdConn, map[string]any{
		"aid": "cancel_order", "user_id": c.authID, "order_id": orderID,
	})
}

// OrderState 是一笔委托在截面里的状态。
type OrderState struct {
	OrderID    string
	Status     string // ALIVE / FINISHED
	VolumeLeft int
	LastMsg    string
	Raw        map[string]any
}

// OrderOf 取一笔委托的当前状态。第二个返回值报告它是否已出现在截面里。
func (c *Client) OrderOf(orderID string) (OrderState, bool) {
	o, ok := Dig(c.TradeSnapshot(), "trade", c.authID, "orders", orderID).(map[string]any)
	if !ok {
		return OrderState{}, false
	}
	st := OrderState{OrderID: orderID, Raw: o}
	st.Status, _ = o["status"].(string)
	st.LastMsg, _ = o["last_msg"].(string)
	if f, ok := o["volume_left"].(float64); ok {
		st.VolumeLeft = int(f)
	}
	return st, true
}

// WaitOrderFinished 等一笔委托走到终态。
//
// ⚠️ 返回 (state,false) 表示**超时**，不表示委托失败。委托可能还挂着，
// 也可能截面还没推过来。调用方必须把这两种情形分开处理，不得把超时读成拒单。
func (c *Client) WaitOrderFinished(orderID string, timeout time.Duration) (OrderState, bool) {
	var last OrderState
	ok := c.WaitUntil(timeout, func() bool {
		st, found := c.OrderOf(orderID)
		if !found {
			return false
		}
		last = st
		return strings.EqualFold(st.Status, "FINISHED")
	})
	return last, ok
}

// PositionOf 取一个合约的持仓截面。
func (c *Client) PositionOf(symbol string) map[string]any {
	v, _ := Dig(c.TradeSnapshot(), "trade", c.authID, "positions", symbol).(map[string]any)
	return v
}

// Num 从截面里取一个数值字段。第二个返回值区分「值是零」与「没有这个字段」。
//
// ⚠️ 这个区分是必须的：本仓库的方法论里，「没有强平价」和「强平价是零」
// 混为一谈是一类静默错误。零值不是安全的默认。
func Num(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

// MustNum 取数值字段，缺失时返回 0。**只在调用方已确认字段存在时使用。**
func MustNum(m map[string]any, key string) float64 {
	f, _ := Num(m, key)
	return f
}

// OpenLots 统计账户上真实的持仓手数（多空今昨全部相加）。
//
// ⚠️ **不能用 len(Positions()) 判断「有没有持仓」。**
// 实测：只要对某个合约下过单，截面里就会出现一条该合约的持仓记录，
// 即便报单被全部拒绝、手数全为零。数记录数会把「碰过这个合约」
// 读成「持有这个合约」——而这个错误在空账户上看起来完全合理。
func (c *Client) OpenLots() float64 {
	var total float64
	for _, v := range c.Positions() {
		p, ok := v.(map[string]any)
		if !ok {
			continue
		}
		for _, k := range []string{"volume_long_today", "volume_long_his",
			"volume_short_today", "volume_short_his"} {
			total += MustNum(p, k)
		}
	}
	return total
}
