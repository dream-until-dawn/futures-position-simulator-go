package types

import "fmt"

// Direction 是买卖方向。
type Direction uint8

const (
	DirectionUnknown Direction = iota // 零值即未知，任何解析失败都落在这里
	Buy
	Sell
)

// Offset 是开平标志。
//
// ⚠️ `Close` 与 `CloseToday` 的区别不是风格问题：
//
//   - `PositionDateType = UseHistory` 的合约必须显式声明平今或平昨
//   - 2026-09-07 在快期模拟上实测，SHFE 合约收到裸 `Close` 时柜台答
//     「平**昨**手数超过**昨仓**持仓量」——即它被解释为**平昨**，不是由柜台择优
//
// ⚠️ 但那条证据来自**快期模拟**，不是 CTP 柜台。按 cn-futures-rules.md 的双坐标
// 证据等级，那一档明确标着「不等于真实柜台」。因此本库在 SimNow 复核之前，
// 对 `UseHistory` 合约上的裸 `Close` **报错**，不按平昨处理。
// **「没测出来」和「测出来是平昨」在代码里长得一模一样**，见 state.md 的
// simnow_pending#1。
type Offset uint8

const (
	OffsetUnknown   Offset = iota
	Open                   // 开仓
	Close                  // 平仓（未声明今昨）
	CloseToday             // 平今
	CloseYesterday         // 平昨
	ForceClose             // 交易所强平
	ForceOff               // 强减
	LocalForceClose        // 本地强平
)

// HedgeFlag 是投机套保标志。
//
// ⚠️ 它不是标签，**它决定保证金率**。套保持仓的保证金率通常低于投机；
// 把套保仓按投机记账会高估保证金占用，进而低估可用资金——
// 看起来「保守」，实际上会让回测里本可以开的仓开不出来，策略被无声地阉割。
//
// v1.0 只做投机，但字段从第一天就带上。
type HedgeFlag uint8

const (
	HedgeUnknown HedgeFlag = iota
	Speculation            // 投机
	Arbitrage              // 套利
	Hedge                  // 套保
)

// ---- CTP 线格式映射 ----
//
// 取值来自 CTP v6.5.1 的 ThostFtdcUserApiDataType.h，经 goctp 的 ctpdefine 清点，
// 见 docs/probes.md §3。

var (
	directionCTP = map[Direction]string{Buy: "0", Sell: "1"}
	offsetCTP    = map[Offset]string{
		Open: "0", Close: "1", ForceClose: "2", CloseToday: "3",
		CloseYesterday: "4", ForceOff: "5", LocalForceClose: "6",
	}
	hedgeCTP = map[HedgeFlag]string{Speculation: "1", Arbitrage: "2", Hedge: "3"}

	directionDIFF = map[Direction]string{Buy: "BUY", Sell: "SELL"}
	offsetDIFF    = map[Offset]string{
		Open: "OPEN", Close: "CLOSE", CloseToday: "CLOSETODAY",
	}
	hedgeDIFF = map[HedgeFlag]string{Speculation: "SPEC", Arbitrage: "ARBI", Hedge: "HEDGE"}
)

// CTP 返回 CTP 线格式取值；第二个返回值报告该取值在 CTP 侧是否存在。
func (d Direction) CTP() (string, bool) { s, ok := directionCTP[d]; return s, ok }

// DIFF 返回 DIFF 线格式取值；第二个返回值报告该取值在 DIFF 侧是否存在。
func (d Direction) DIFF() (string, bool) { s, ok := directionDIFF[d]; return s, ok }

// CTP 返回 CTP 线格式取值。
func (o Offset) CTP() (string, bool) { s, ok := offsetCTP[o]; return s, ok }

// DIFF 返回 DIFF 线格式取值。
//
// ⚠️ 第二个返回值经常是 false，而这是**信息不是缺陷**：
// DIFF 只认 OPEN / CLOSE / CLOSETODAY 三种，`CloseYesterday` 与三种强平标志
// 在 DIFF 侧**没有对应取值**。调用方必须处理 false，
// 而不是拿零值串发出去——那会变成一笔开仓。
func (o Offset) DIFF() (string, bool) { s, ok := offsetDIFF[o]; return s, ok }

// CTP 返回 CTP 线格式取值。
func (h HedgeFlag) CTP() (string, bool) { s, ok := hedgeCTP[h]; return s, ok }

// DIFF 返回 DIFF 线格式取值。
func (h HedgeFlag) DIFF() (string, bool) { s, ok := hedgeDIFF[h]; return s, ok }

// DirectionFromCTP 解析 CTP 线格式的买卖方向。
func DirectionFromCTP(s string) (Direction, error) { return lookupDir(directionCTP, s, "CTP") }

// DirectionFromDIFF 解析 DIFF 线格式的买卖方向。
func DirectionFromDIFF(s string) (Direction, error) { return lookupDir(directionDIFF, s, "DIFF") }

// OffsetFromCTP 解析 CTP 线格式的开平标志。
func OffsetFromCTP(s string) (Offset, error) { return lookupOffset(offsetCTP, s, "CTP") }

// OffsetFromDIFF 解析 DIFF 线格式的开平标志。
func OffsetFromDIFF(s string) (Offset, error) { return lookupOffset(offsetDIFF, s, "DIFF") }

// HedgeFromCTP 解析 CTP 线格式的投机套保标志。
func HedgeFromCTP(s string) (HedgeFlag, error) { return lookupHedge(hedgeCTP, s, "CTP") }

// HedgeFromDIFF 解析 DIFF 线格式的投机套保标志。
func HedgeFromDIFF(s string) (HedgeFlag, error) { return lookupHedge(hedgeDIFF, s, "DIFF") }

// ⚠️ 三个 lookup 都在未知取值时**返回错误**，不返回零值。
// 零值会让「没见过的开平标志」悄悄变成 OffsetUnknown 然后被下游当成某种默认——
// 而本库的静默风险清单里，「零值不是安全的默认」排在最前面。

func lookupDir(m map[Direction]string, s, wire string) (Direction, error) {
	for k, v := range m {
		if v == s {
			return k, nil
		}
	}
	return DirectionUnknown, fmt.Errorf("%s 线格式里没有买卖方向 %q", wire, s)
}

func lookupOffset(m map[Offset]string, s, wire string) (Offset, error) {
	for k, v := range m {
		if v == s {
			return k, nil
		}
	}
	return OffsetUnknown, fmt.Errorf("%s 线格式里没有开平标志 %q", wire, s)
}

func lookupHedge(m map[HedgeFlag]string, s, wire string) (HedgeFlag, error) {
	for k, v := range m {
		if v == s {
			return k, nil
		}
	}
	return HedgeUnknown, fmt.Errorf("%s 线格式里没有投机套保标志 %q", wire, s)
}

func (d Direction) String() string {
	switch d {
	case Buy:
		return "买"
	case Sell:
		return "卖"
	}
	return "未知方向"
}

func (o Offset) String() string {
	switch o {
	case Open:
		return "开仓"
	case Close:
		return "平仓"
	case CloseToday:
		return "平今"
	case CloseYesterday:
		return "平昨"
	case ForceClose:
		return "交易所强平"
	case ForceOff:
		return "强减"
	case LocalForceClose:
		return "本地强平"
	}
	return "未知开平标志"
}

func (h HedgeFlag) String() string {
	switch h {
	case Speculation:
		return "投机"
	case Arbitrage:
		return "套利"
	case Hedge:
		return "套保"
	}
	return "未知投机套保标志"
}

// IsClose 报告该开平标志是否会**减少**持仓。
//
// ⚠️ 三种强平标志（ForceClose / ForceOff / LocalForceClose）也算平仓。
// 漏掉它们会让强平产生的成交被当成开仓处理，而账面依旧平——
// CTP 里强平有三种取值这件事本身就说明它不是单一算法，见 cn-futures-rules.md §10。
func (o Offset) IsClose() bool {
	switch o {
	case Close, CloseToday, CloseYesterday, ForceClose, ForceOff, LocalForceClose:
		return true
	}
	return false
}

// SpecifiesPositionDate 报告该开平标志是否**显式声明**了平今还是平昨。
//
// ⚠️ `Close` 返回 false —— 这正是 simnow_pending#1 要裁决的那条：
// 在 `UseHistory` 合约上，裸 `Close` 到底等于平昨、还是由柜台择优，
// 目前只有快期模拟侧的证据。在 SimNow 复核之前，`order` 包对它**报错**。
func (o Offset) SpecifiesPositionDate() bool {
	return o == CloseToday || o == CloseYesterday
}
