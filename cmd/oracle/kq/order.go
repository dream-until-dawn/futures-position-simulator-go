package kq

import (
	"fmt"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
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
// NoUseHistory 的合约（DCE / CZCE）只接受 Close。
//
// ⚠️ 上一句的最后半句原来写着「用错会被柜台拒单」，**已被实测推翻**：
// 20260909 的 position-frozen 实验在 DCE.m2701（今仓 3 手）上发 CLOSETODAY，
// 快期模拟**接受了**，且冻结结果与 CLOSE 逐字段相同
// （volume_long_frozen=1、volume_long_frozen_today=1）。
//
// 规则本身不改 —— 它是 CTP 的约定，本库照它建模；
// 改的是「柜台一定会拒」这个**关于口子的**断言：这个口子不拒。
// 两者混在一句里，会让人拿一次「没被拒」去否定整条规则，
// 或反过来拿规则去解释一次实测。见 state.md 的 kq_facts。
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

// Guard 是下单安全阀在 DIFF 这一侧的**接头**。
//
// ⚠️ 判断逻辑不在这里 —— 在 `safety` 包，与协议无关（理由见那个包的文档）。
// 这里只做一件事：**把 DIFF 的委托映射成 safety.Intent**。
//
// 映射点刻意留在协议包里，而不是让 safety 去认 DIFF 的枚举：
// 「BUY 平的是空头」这句话是 **DIFF 的语义**，写在 DIFF 这一侧才对。
// CTP 那一侧会有它自己的一份，而**两份长得不一样正是要被看见的事**。
type Guard struct {
	AllowOrder bool // 对应 .env 的 PROBE_ALLOW_ORDER
	MaxVolume  int  // 对应 .env 的 PROBE_MAX_VOLUME，单笔手数上限

	// Protected 是**今天不许平**的持仓腿。
	Protected []safety.ProtectedLeg
	// TradingDay 是柜台报的当前交易日。
	//
	// ⚠️ 空串表示**还不知道**（截面没回来），那时候按「拦」处理。
	TradingDay string
}

// intentOf 把一笔 DIFF 委托翻成与协议无关的意图。
//
// ⚠️ 方向取反在这里做：平掉**空**头持仓发的是 BUY。
// 这一步写反的话安全阀会掉个个 —— 放过真正危险的那一笔，
// 转去拦一笔无关的，而两种表现都不像 bug。
func intentOf(r OrderReq) safety.Intent {
	in := safety.Intent{
		Symbol:     r.Symbol(),
		Closing:    r.Offset == Close || r.Offset == CloseToday,
		Volume:     r.Volume,
		LimitPrice: r.LimitPrice,
		Desc:       r.String(),
	}
	if in.Closing {
		switch r.Direction {
		case Buy:
			in.ClosesSide = safety.Short // BUY 平**空**头
		case Sell:
			in.ClosesSide = safety.Long // SELL 平**多**头
		}
	}
	return in
}

// Check 校验一笔委托是否被安全阀放行。
func (g Guard) Check(r OrderReq) error {
	return safety.Valve{
		AllowOrder: g.AllowOrder,
		MaxVolume:  g.MaxVolume,
		Protected:  g.Protected,
		TradingDay: g.TradingDay,
	}.Check(intentOf(r))
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
