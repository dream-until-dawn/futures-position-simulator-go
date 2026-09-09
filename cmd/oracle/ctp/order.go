package ctp

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
	def "gitee.com/haifengat/goctp/ctpdefine"
)

// OrderReq 是一笔限价委托。
//
// 与 `kq.OrderReq` **同形不同类型**，理由与两个客户端不做统一抽象相同：
// 枚举取值一个都不一样，硬共用会让「哪个常量属于哪个协议」变模糊 ——
// 而这里的常量**尤其容易混**，见 intentOf 的注释。
type OrderReq struct {
	Exchange   string
	Instrument string
	Direction  def.TThostFtdcDirectionType
	Offset     def.TThostFtdcOffsetFlagType
	Volume     int
	LimitPrice float64
}

// Symbol 返回 "SHFE.rb2701" 这样的全称。
func (r OrderReq) Symbol() string { return r.Exchange + "." + r.Instrument }

func (r OrderReq) String() string {
	return fmt.Sprintf("%s dir=%q off=%q %d手 @%.4f",
		r.Symbol(), string(r.Direction), string(r.Offset), r.Volume, r.LimitPrice)
}

// closingOffsets 是**已识别的平仓**开平标志。
//
// ⚠️ CTP 比 DIFF 多一个 `CloseYesterday`（'4'）—— DIFF 只有 Close/CloseToday。
// 而 kq 那侧的 Guard 注释里早写过这句预言：
//
//	「将来加 CloseYesterday 而漏改这里，后果从『安全阀失效』变成『多拦一次』」
//
// 那一天到了，而且是在**另一个协议**上到的。所以这里三个一起列，
// 并且判据是「**是不是已识别的平仓**」而不是「是不是开仓」——
// 后者会让任何未设置或拼错的开平标志绕过手数上限。
var closingOffsets = map[def.TThostFtdcOffsetFlagType]bool{
	def.THOST_FTDC_OF_Close:          true,
	def.THOST_FTDC_OF_CloseToday:     true,
	def.THOST_FTDC_OF_CloseYesterday: true,
}

// intentOf 把一笔 CTP 委托翻成与协议无关的意图。
//
// # ⚠️ 这一侧的常量**特别容易混**
//
//	Buy  = '0'      Open       = '0'    ← **同一个字符**，不同字段
//	Sell = '1'      Close      = '1'    ← 又是同一个字符
//	                CloseToday = '3'
//	                CloseYesterday = '4'
//	PosiDirection: Long = '2'  Short = '3'   ← 与买卖方向**又是另一套**
//
// 于是一处「读错了字段」的 bug 会在**部分组合上恰好正确**，
// 而恰好正确的 bug 比全错的 bug 难查得多。
// ⚠️ 这就是为什么这里不共用 kq 的映射，也不共用它的测试。
func intentOf(r OrderReq) safety.Intent {
	in := safety.Intent{
		Symbol:     r.Symbol(),
		Closing:    closingOffsets[r.Offset],
		Volume:     r.Volume,
		LimitPrice: r.LimitPrice,
		Desc:       r.String(),
	}
	if in.Closing {
		switch r.Direction {
		case def.THOST_FTDC_D_Buy:
			in.ClosesSide = safety.Short // 买平 → 平的是**空**头
		case def.THOST_FTDC_D_Sell:
			in.ClosesSide = safety.Long // 卖平 → 平的是**多**头
		}
	}
	return in
}

// Check 让一笔委托过安全阀。
//
// ⚠️ 它在 Insert 之前就存在，且 Insert 只能通过它发单 ——
// docs/ctp-oracle.md 第 5 节：**顺序是设计的一部分**，
// 先有报单、后补安全阀，中间那段时间它就是没有闸的。
func (c *Client) Check(r OrderReq) error {
	v := c.Valve
	v.TradingDay = c.TradingDay()
	return v.Check(intentOf(r))
}
