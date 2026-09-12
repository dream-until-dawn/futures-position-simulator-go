package main

// holdKind 是 `ctp-hold` 那张表能给出的**全部**结论形状。
//
// ⚠️ 它被抽成一个纯函数，理由是 20260911 夜盘那次假结论：
// 判据当时长在一个 `switch` 里、混在打印语句中间，
// **于是它唯一的检验方式是把真仓开到账上再看它印什么** ——
// 而那种检验一晚上只做得了几次，还每次都要花钱。
//
//	⚠️ 一个只能在盘中检验的判据，实际上等于没被检验过。
//
// ⇒ 现在它是 `holdVerdict`，五种取值全部离线可测。
type holdKind int

const (
	// holdNoPremise 账上不止一条今仓 ⇒ **不下判断**。
	//
	// ⚠️ 这一支存在的全部理由，是不让它说出那句结论。
	// 原判据默认「只有这一条腿、只有价格在动」，多一条腿就多一个变动来源，
	// 而**两个来源在那张表里长得一模一样**。
	holdNoPremise holdKind = iota
	// holdNoData 一轮都没读到该合约的今仓 ⇒ 没有观测，不是结论。
	holdNoData
	// holdNoMove 行情全程没动 ⇒ 开仓价与今结算价给出同一个数，**什么都没分开**。
	holdNoMove
	// holdStatic 行情动了而占用保证金不变 ⇒ 基准是开仓价。
	holdStatic
	// holdDynamic 行情动了且占用保证金跟着变 ⇒ 基准是某个动态价。
	holdDynamic
)

// holdVerdict 按**优先级**挑一种结论，顺序本身就是被测的性质：
//
//	前提不成立  >  没有观测  >  没有判别力  >  真结论
//
// ⚠️ 这个顺序不能交换。把「真结论」排到前面去，
// 前三种情形就会各自得到一个**看起来完全正常的结论** ——
// 而它们的共同点是：这一轮其实什么都没测出来。
func holdVerdict(legs int, first, last, firstPx, lastPx float64) holdKind {
	if legs > 1 {
		return holdNoPremise
	}
	if first == 0 {
		return holdNoData
	}
	if firstPx == 0 || firstPx == lastPx {
		return holdNoMove
	}
	if first == last {
		return holdStatic
	}
	return holdDynamic
}
