package main

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// feeTier 是「这一笔收哪一档」的预言：平今档或平昨档。
type feeTier int

const (
	tierToday feeTier = iota + 1
	tierYd
)

func (t feeTier) String() string {
	if t == tierToday {
		return "平今档"
	}
	return "平昨档"
}

// feeCandidate 是 §13 #21「裸 CLOSE 收哪一档」在郑商所上的一个候选，以及它对两个实验各自的预言。
//
// ⚠️ **候选按郑商所另立**，不沿用大商所的 a/b/c（closeFeeCandidates）：
// 大商所的 (c) 判法「收到声明平今档 ⇒ c 活」依赖 m2701 上「行为平昨费率恰好等于声明平今」这个巧合
// （评审 20260916 深夜）。郑商所 MA701 声明 平昨 2 / 平今 6，那个巧合不存在。
//
// ⚠️ 这张表是**事前登记**的（提交 43101f7，20260917 09:14，早于任何观测），
// TestCZCECandidatesMatchPreRegistration 把它钉成与 state.md 那张表一字不差 ——
// 看到结果之后改预言，那条测试会红。
type feeCandidate struct {
	Name, Desc string
	// X1 是「今 0 昨 ≥1，通用平仓 1 手」时的预言（ctp-closefee -mode bare）。
	X1 feeTier
	// X2 是「今 1 昨 ≥1，通用平仓 1 手」时的预言（ctp-closeorder），**依赖消耗的是哪一片**。
	X2 func(consumedYd bool) feeTier
	// X0 是「今 1 昨 0，通用平仓 1 手」时的预言（ctp-closeorder 在 X2 消耗昨仓之后接着发）—— 这一笔只能消耗今仓。
	//
	// ⚠️ 它是**观测之前补登记**的（43101f7 之后、今晚 21:05 之前）：TestCZCEExperimentsDiscriminate 抓到
	// 43101f7 那句「X1 + X2 合起来唯一」是错的 —— X2 消耗昨仓时 (d) 与 (e) 在两个实验里预言完全相同，
	// 只有消耗到今仓才分得开。
	X0 feeTier
}

var czceFeeCandidates = []feeCandidate{
	{Name: "a", Desc: "同大商所：min(平仓量, 平仓前今仓量) 走平今，其余走平昨",
		X1: tierYd, X2: func(bool) feeTier { return tierToday }, X0: tierToday},
	{Name: "b", Desc: "裸平一律走平今",
		X1: tierToday, X2: func(bool) feeTier { return tierToday }, X0: tierToday},
	{Name: "d", Desc: "按实际消耗的那一片拆档",
		X1: tierYd, X2: func(consumedYd bool) feeTier {
			if consumedYd {
				return tierYd
			}
			return tierToday
		}, X0: tierToday},
	{Name: "e", Desc: "裸平一律走平昨",
		X1: tierYd, X2: func(bool) feeTier { return tierYd }, X0: tierYd},
}

// aliveOf 按实测增量判哪些候选还活着。纯函数。
//
// ⚠️ 判据是「增量**等于**预言档位的费率」，不是「更接近」：落在两档之外时一个都不活，并且要说出来。
// ⚠️ 两档费率相同时拒判（每个候选都会活，看起来像「全部一致」，其实是没有判别力）。
func aliveOf(set []feeCandidate, predict func(feeCandidate) feeTier,
	delta, rateToday, rateYd decimal.Decimal, label string) (alive []string, why string, err error) {

	if rateToday.Equal(rateYd) {
		return nil, "", fmt.Errorf("⚠️ %s：平今档与平昨档都是 %s —— 每个候选都会活，这一笔没有判别力，不判", label, rateToday)
	}
	var preds []string
	for _, c := range set {
		t := predict(c)
		rate := rateYd
		if t == tierToday {
			rate = rateToday
		}
		preds = append(preds, fmt.Sprintf("%s→%s %s", c.Name, t, rate))
		if delta.Equal(rate) {
			alive = append(alive, c.Name)
		}
	}
	why = fmt.Sprintf("%s：实测收 %s；预言 %s", label, delta, strings.Join(preds, "、"))
	if len(alive) == 0 {
		why += fmt.Sprintf("；⚠️ **谁都没预言到** —— 声明费率与行为不一致，或第五种可能。先别改本库，把截面拿去重看")
	}
	return alive, why, nil
}

// czceX1 判 X1（今 0 昨 ≥1，通用平仓 1 手）。
func czceX1(delta, rateToday, rateYd decimal.Decimal) ([]string, string, error) {
	return aliveOf(czceFeeCandidates, func(c feeCandidate) feeTier { return c.X1 },
		delta, rateToday, rateYd, "X1 郑商所 今0昨≥1 通用平仓 1 手")
}

// czceX2 判 X2（今 1 昨 ≥1，通用平仓 1 手）。consumedYd 由 closeOrderVerdict 先判出来 ——
// ⚠️ **先读消耗了哪一片，再读手续费**：(d) 的预言取决于它。
func czceX2(consumedYd bool, delta, rateToday, rateYd decimal.Decimal) ([]string, string, error) {
	part := "消耗今仓"
	if consumedYd {
		part = "消耗昨仓"
	}
	return aliveOf(czceFeeCandidates, func(c feeCandidate) feeTier { return c.X2(consumedYd) },
		delta, rateToday, rateYd, "X2 郑商所 今1昨≥1 通用平仓 1 手（"+part+"）")
}

// czceX0 判 X0（今 1 昨 0，通用平仓 1 手 —— 只能消耗今仓）。
func czceX0(delta, rateToday, rateYd decimal.Decimal) ([]string, string, error) {
	return aliveOf(czceFeeCandidates, func(c feeCandidate) feeTier { return c.X0 },
		delta, rateToday, rateYd, "X0 郑商所 今1昨0 通用平仓 1 手")
}

// closeFeeRegistered 在**下单之前**判这个交易所、这个实验有没有事前登记过候选。
//
// ⚠️ 没登记就不许跑：跑出来的数没有预言可比，事后再列候选就是看着答案写题目。
// ⚠️ 大商所那套 a/b/c 只在大商所上有意义 —— 拿到别的交易所上打印，名字对得上、意思对不上。
func closeFeeRegistered(exchange string, m closeFeeMode) error {
	switch {
	case exchange == "DCE":
		return nil
	case exchange == "CZCE" && m == closeFeeBare:
		return nil
	case exchange == "CZCE":
		return fmt.Errorf("⚠️ **不跑**：郑商所上只事前登记了 X1（-mode bare），%s 没有登记候选", m)
	}
	return fmt.Errorf("⚠️ **不跑**：%s 上没有事前登记过「裸 CLOSE 收哪一档」的候选 —— "+
		"先把候选与预言写进 state.md 并提交，再跑。大商所的 a/b/c 不能直接搬过来", exchange)
}
