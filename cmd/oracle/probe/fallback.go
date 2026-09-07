package probe

// fallbackDecision 是「CLOSETODAY 打完之后该怎么办」的判定结果。
//
// ⚠️ 抽成纯函数是刻意的：这三条修法全是**行为变更**，
// 而行为变更正是金文件守卫看不见、洞最容易长出来的地方（见 roadmap 2026-09-08 那条）。
// 判定一旦是纯函数，它就能被穷举、被破坏验证，而不必等一个活柜台。
type fallbackDecision uint8

const (
	// fallbackDone 已全成，收工。
	fallbackDone fallbackDecision = iota
	// fallbackStopTimeout 超时未到终态：撤单并停止，**不换开平标志重试**。
	//
	// ⚠️ 超时不等于被拒。原实现把两者混为一谈，于是「限价没被打到」也会触发回退，
	// 而回退发的是 CLOSE ——实测它在 UseHistory 交易所上被解释为平昨。
	fallbackStopTimeout
	// fallbackForbiddenYesterday 被拒了，但账上有昨仓：禁止回退。
	//
	// ⚠️ 这条不看拒因，只看账上有没有昨仓。
	// **一个不需要解析对方措辞的守卫，比一个需要的可靠。**
	fallbackForbiddenYesterday
	// fallbackAllowed 被拒且账上没有昨仓：可以回退到 CLOSE。
	fallbackAllowed
)

// decideFallback 给出 CLOSETODAY 一笔委托之后的动作。
//
//	volHis      该合约该方向的**昨仓**手数
//	done        委托是否已到终态
//	volumeLeft  终态时的未成交手数
func decideFallback(volHis float64, done bool, volumeLeft int) fallbackDecision {
	if done && volumeLeft == 0 {
		return fallbackDone
	}
	if !done {
		return fallbackStopTimeout
	}
	if volHis > 0 {
		return fallbackForbiddenYesterday
	}
	return fallbackAllowed
}
