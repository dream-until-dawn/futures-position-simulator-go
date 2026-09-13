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
		// ⚠️ 今仓排在昨仓前。不按字符串比：「今」U+4ECA <「昨」U+6628，字典序会把昨仓排前面。
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
// remaining 是**平完之后重新查**的持仓（同一个作用域）。判据：
//
//	有任何一笔真没平掉                   ⇒ 报错，**全部列出**（不是只点第一条）
//	平完仍在、且不是被保护跳过的那条腿   ⇒ 报错，列出
//	否则                                 ⇒ 平干净了；被保护跳过的腿在账上是**预期**
//
// ⚠️ 原先的收尾是「占用保证金必须归零」。在受保护的交易日里种子本就该留着，
// 那一条**永远**会触发 —— 于是它要么被人习惯性忽略，要么每次都去收拾一个不存在的事故。
// ⇒ 换成按腿比：它知道哪条腿是该留的。
func flattenVerdict(t flattenTally, remaining []flattenLeg) error {
	protected := map[string]bool{}
	for _, l := range t.Protected {
		protected[l.Symbol+"|"+l.Side.String()] = true
	}
	var unexpected []string
	for _, l := range remaining {
		if !protected[l.Symbol+"|"+l.Side.String()] {
			unexpected = append(unexpected, l.String())
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
		fmt.Fprintf(&b, "；平完重查仍在、且不受保护：%s", strings.Join(unexpected, "；"))
	}
	if len(t.Protected) > 0 {
		names := make([]string, len(t.Protected))
		for i, l := range t.Protected {
			names[i] = l.String()
		}
		fmt.Fprintf(&b, "（另按保护跳过 %d 条，那几条留着是预期：%s）", len(t.Protected), strings.Join(names, "；"))
	}
	return fmt.Errorf("%s", b.String())
}
