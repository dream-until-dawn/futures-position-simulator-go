package main

import (
	"fmt"
	"sort"
	"strings"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/safety"
)

// flattenLeg 是 ctp-flatten 要发的**一笔**平仓（价格发单前再取）。
type flattenLeg struct {
	Symbol string
	Side   safety.Side // **持仓**方向
	Dir    def.TThostFtdcDirectionType
	Offset def.TThostFtdcOffsetFlagType
	Volume int
	Name   string // "今仓" / "昨仓"
}

func (l flattenLeg) String() string {
	return fmt.Sprintf("%s %s %s %d 手", l.Symbol, l.Side, l.Name, l.Volume)
}

// flattenPlan 把一次持仓查询排成 ctp-flatten 的平仓计划。
//
// # ⚠️ 为什么要**排序**
//
// 20260913 评审指出：原先是 `for key, p := range pos` —— **map，遍历顺序随机** ——
// 而循环里任何一笔没平掉就当场 return。接上种子保护之后，`-all` 在受保护的交易日里：
//
//	排在种子前面的仓   被平掉
//	撞到种子           阀门拒 ⇒ 报「DCE.m2701 没平掉」⇒ return
//	排在种子后面的仓   **不平，也不报**
//
// ⇒ 每次平掉的集合不一样，而报错只点了种子。循环不再提前返回（见 runCTPFlatten）；
// 排序是另一半：**同一个账户跑两次，发单顺序与日志要一样**，否则「这次怎么多平了一个」没法查。
//
// # ⚠️ 昨仓按 `Position − TodayPosition` 算，不读 `YdPosition`
//
// 理由与 longSidesOf 相同：`YdPosition` 是**日初**值，平掉昨仓之后可能不减 ——
// 拿它下单，会去平一手已经不存在的昨仓，被拒，报「没平掉」。
// 两种持仓表示下这个算式都对：分开的历史记录 `TodayPosition=0`；合成的一条里差值就是昨仓。
func flattenPlan(pos map[string]*def.CThostFtdcInvestorPositionField, only string) (legs []flattenLeg, skipped []string) {
	seenSkip := map[string]bool{}
	for _, p := range pos {
		position, today := int(p.Position), int(p.TodayPosition)
		if position == 0 {
			continue
		}
		symbol := ctp.Text(p.ExchangeID[:]) + "." + ctp.Text(p.InstrumentID[:])
		if only != "" && symbol != only {
			if !seenSkip[symbol] {
				seenSkip[symbol] = true
				skipped = append(skipped, symbol)
			}
			continue
		}
		side, dir := safety.Long, def.TThostFtdcDirectionType(def.THOST_FTDC_D_Sell)
		if p.PosiDirection == def.THOST_FTDC_PD_Short {
			side, dir = safety.Short, def.THOST_FTDC_D_Buy
		}
		if today > 0 {
			legs = append(legs, flattenLeg{symbol, side, dir,
				def.TThostFtdcOffsetFlagType(def.THOST_FTDC_OF_CloseToday), today, "今仓"})
		}
		if yd := position - today; yd > 0 {
			legs = append(legs, flattenLeg{symbol, side, dir,
				def.TThostFtdcOffsetFlagType(def.THOST_FTDC_OF_CloseYesterday), yd, "昨仓"})
		}
	}
	sort.Slice(legs, func(i, j int) bool {
		a, b := legs[i], legs[j]
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		if a.Side != b.Side {
			return a.Side < b.Side
		}
		// ⚠️ 今仓排在昨仓前，显式写出来、不靠字符串序。
		// （「今」U+4ECA <「昨」U+6628，升序恰好也是今在前 —— 20260913 我先写反过一次，
		// 评审实跑纠正。⇒ 次序是**设计**，不该取决于两个汉字的码位碰巧怎么排。）
		return a.Name == "今仓" && b.Name != "今仓"
	})
	sort.Strings(skipped)
	return legs, skipped
}

// flattenTally 是一轮 ctp-flatten 发完之后的账。
type flattenTally struct {
	Closed    []flattenLeg
	Protected []flattenLeg // 被受保护腿拦下、**按预期跳过**的
	Failed    []string     // 真没平掉的，每条带原因
}

// flattenVerdict 判一轮 ctp-flatten 算不算平干净了。
//
// remaining 是**平完之后重新查**的持仓（同一个作用域）；allowed 给出某个「合约 + 方向」上
// 保护**覆盖几手**（ProtectedLeg.Volume）。判据：
//
//	有任何一笔真没平掉                                 ⇒ 报错，**全部列出**（不是只点第一条）
//	平完仍在、且不是被保护跳过的那个「合约 + 方向」     ⇒ 报错，列出
//	被保护跳过的「合约 + 方向」上，仍在的手数超过保护覆盖 ⇒ 报错，列出多出几手（**不自动平**）
//	否则                                               ⇒ 平干净了；保护覆盖的那几手留着是**预期**
//
// ⚠️ 原先的收尾是「占用保证金必须归零」。在受保护的交易日里种子本就该留着，
// 那一条**永远**会触发 —— 于是它要么被人习惯性忽略，要么每次都去收拾一个不存在的事故。
//
// ⚠️⚠️ 第三条是 20260913 评审第二轮补的：上一版只按「合约 + 方向」认，于是
// 「种子 + #4 收尾失败遗留的 2 手今仓」「种子不在、5 手今天开的仓」**都判平干净、正常退出** ——
// 那是本命令自己记过的最坏形状：报「无事可做」的平仓命令比报错的更坏。
// ⚠️ 多出的部分**只报不平**：分不出哪一手是种子，而大商所上一笔平今会不会吃到种子，正是 #4 要答的问题。
// 遗留今仓的恢复路径是 `ctp-closeorder -cleanup`。
func flattenVerdict(t flattenTally, remaining []flattenLeg, allowed func(symbol string, side safety.Side) int) error {
	key := func(l flattenLeg) string { return l.Symbol + "|" + l.Side.String() }
	protected := map[string]bool{}
	for _, l := range t.Protected {
		protected[key(l)] = true
	}
	held := map[string]int{}
	first := map[string]flattenLeg{}
	var order, unexpected []string
	for _, l := range remaining {
		k := key(l)
		if !protected[k] {
			unexpected = append(unexpected, l.String())
			continue
		}
		if _, ok := first[k]; !ok {
			first[k] = l
			order = append(order, k)
		}
		held[k] += l.Volume
	}
	for _, k := range order {
		l := first[k]
		if a := allowed(l.Symbol, l.Side); held[k] > a {
			unexpected = append(unexpected, fmt.Sprintf(
				"%s %s 共 %d 手，保护只覆盖 %d 手 —— **多出 %d 手**（不自动平：分不出哪一手是种子；"+
					"遗留今仓用 `ctp-closeorder -cleanup -symbol %s`）",
				l.Symbol, l.Side, held[k], a, held[k]-a, l.Symbol))
		}
	}
	if len(t.Failed) == 0 && len(unexpected) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("⚠️⚠️ **没平干净**")
	if len(t.Failed) > 0 {
		fmt.Fprintf(&b, "；没平掉 %d 笔：%s", len(t.Failed), strings.Join(t.Failed, "；"))
	}
	if len(unexpected) > 0 {
		fmt.Fprintf(&b, "；平完重查仍在、且不在保护覆盖内：%s", strings.Join(unexpected, "；"))
	}
	if len(t.Protected) > 0 {
		names := make([]string, len(t.Protected))
		for i, l := range t.Protected {
			names[i] = l.String()
		}
		fmt.Fprintf(&b, "（另按保护跳过 %d 条：%s）", len(t.Protected), strings.Join(names, "；"))
	}
	return fmt.Errorf("%s", b.String())
}

// protectedVolume 造 flattenVerdict 用的 allowed：某个「合约 + 方向」上一张保护表覆盖的手数。
//
// ⚠️ 按日期**不过滤**、取各条里的**最大值**，不相加：走到这里的腿已经被安全阀按交易日拦过一次
// （它在 tally.Protected 里），日期已经核过；而同一手种子在 20260914/15 两天各挂一条 ——
// 相加会把 1 手种子当成 2 手，于是「种子 + 1 手遗留今仓」被判平干净。
func protectedVolume(legs []safety.ProtectedLeg) func(symbol string, side safety.Side) int {
	return func(symbol string, side safety.Side) int {
		v := 0
		for _, l := range legs {
			if l.Symbol == symbol && l.Side == side && l.Volume > v {
				v = l.Volume
			}
		}
		return v
	}
}
