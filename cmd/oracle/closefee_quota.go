package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/shopspring/decimal"
)

// quotaStep 是一笔「通用平仓 1 手」发出之前的账上形状，外加它实际消耗了哪一片。
// §13 #23 的每个候选都只是这几个数的函数 —— 预言写成纯函数，登记表由测试从这里渲染、与 state.md 逐格比。
type quotaStep struct {
	// TodayBefore / YdBefore 是平仓之前的今 / 昨手数（longSidesOf 的口径）。
	TodayBefore, YdBefore int
	// OpenedToday 是本交易日本合约开过几手（含已平掉的）。
	OpenedToday int
	// TodayCharged 是本交易日本合约**已经按平今档收过**的手数（本笔之前）。
	TodayCharged int
	// ConsumedYd：这一笔实际消耗的是昨仓（由持仓前后差判出，不是由手续费反推）。
	ConsumedYd bool
}

// quotaCandidate 是 §13 #23「裸平收哪一档」的一个候选。
//
// ⚠️ 这张表是**事前登记**的（登记块「事前登记：§13 #23」在 docs/state.md 交接段，提交早于 20260918 日盘种子、早于当晚观测）。
// TestQuotaCandidatesMatchPreRegistration 把渲染出的预言表钉成与 state.md 一字不差。
type quotaCandidate struct {
	Name, Desc string
	Tier       func(quotaStep) feeTier
}

var quotaCandidates = []quotaCandidate{
	{Name: "a", Desc: "按平仓前今仓手数：今仓 > 0 就走平今（大商所 §13 #21 收敛到的那个）",
		Tier: func(s quotaStep) feeTier {
			if s.TodayBefore > 0 {
				return tierToday
			}
			return tierYd
		}},
	{Name: "f", Desc: "按当日开仓额度：当日开仓量 − 当日已按平今档收过的手数 > 0 就走平今，与平的哪一片无关",
		Tier: func(s quotaStep) feeTier {
			if s.OpenedToday-s.TodayCharged > 0 {
				return tierToday
			}
			return tierYd
		}},
	{Name: "k", Desc: "消耗的是昨仓、且账上有今仓时走平今；其余走平昨（「错位」：收费的片与消耗的片相反）",
		Tier: func(s quotaStep) feeTier {
			if s.ConsumedYd && s.TodayBefore > 0 {
				return tierToday
			}
			return tierYd
		}},
}

// quotaPlan 是今晚的实验序列：今 0 昨 1（种子）→ 开 1 → 开 1 → 通用平仓 1 手 × 3。
// 预期消耗顺序 昨、今、今（两个交易所上「今昨都在时裸平消耗昨仓」都量过：大商所 20260915、郑商所 X2）。
//
// ⚠️ 若实测消耗顺序不同，判定按**实测**的消耗重算每个候选的预言（Tier 是消耗的函数），登记表只是预期顺序下的展开。
func quotaPlan(consumedYd [3]bool) [3]quotaStep {
	var out [3]quotaStep
	today, yd := 2, 1
	for i := range out {
		out[i] = quotaStep{TodayBefore: today, YdBefore: yd, OpenedToday: 2, ConsumedYd: consumedYd[i]}
		if consumedYd[i] {
			yd--
		} else {
			today--
		}
	}
	return out
}

// quotaExpectedConsumption 是登记表展开时用的消耗顺序。
var quotaExpectedConsumption = [3]bool{true, false, false}

// quotaPredict 给出一个候选在一串平仓上的档位序列。TodayCharged 按**该候选自己**的预言累计 ——
// 每个候选活在自己的世界里；判定时改用实测收费累计（见 quotaVerdict）。
func quotaPredict(c quotaCandidate, steps [3]quotaStep) [3]feeTier {
	var out [3]feeTier
	charged := 0
	for i, s := range steps {
		s.TodayCharged = charged
		out[i] = c.Tier(s)
		if out[i] == tierToday {
			charged++
		}
	}
	return out
}

// quotaRegistrationRows 渲染登记表的数据行（与 state.md 那张表逐行比）。
func quotaRegistrationRows(rateTodayDCE, rateYdDCE, rateTodayCZCE, rateYdCZCE string) []string {
	steps := quotaPlan(quotaExpectedConsumption)
	var rows []string
	for _, c := range quotaCandidates {
		p := quotaPredict(c, steps)
		fee := func(rt, ry string) string {
			var xs []string
			for _, t := range p {
				if t == tierToday {
					xs = append(xs, rt)
				} else {
					xs = append(xs, ry)
				}
			}
			return strings.Join(xs, " / ")
		}
		rows = append(rows, fmt.Sprintf("| (%s) | %s / %s / %s | %s | %s |", c.Name, p[0], p[1], p[2],
			fee(rateTodayDCE, rateYdDCE), fee(rateTodayCZCE, rateYdCZCE)))
	}
	return rows
}

// quotaVerdict 按实测判：每一笔的收费增量要**等于**该候选预言档位的费率，三笔都等才算活。
//
// ⚠️ TodayCharged 用**实测**累计（柜台已经按平今档收了几手），不用候选自己的预言累计 ——
// 否则一个在第一笔就错了的候选，后面几笔会在一个柜台没走过的状态上被判。
// 反过来这让它对「第一笔错、后面碰巧对」的候选也只判它错的那一笔；判定取三笔的交。
func quotaVerdict(steps []quotaStep, deltas []decimal.Decimal, rateToday, rateYd decimal.Decimal) (alive []string, lines []string, err error) {
	if rateToday.Equal(rateYd) {
		return nil, nil, fmt.Errorf("⚠️ 平今档与平昨档都是 %s —— 每个候选都会活，没有判别力，不判", rateToday)
	}
	if len(steps) != len(deltas) || len(steps) == 0 {
		return nil, nil, fmt.Errorf("⚠️ 平仓 %d 笔、手续费增量 %d 个 —— 对不上，不判", len(steps), len(deltas))
	}
	ok := map[string]bool{}
	for _, c := range quotaCandidates {
		ok[c.Name] = true
	}
	charged := 0
	for i, s := range steps {
		var actual feeTier
		switch {
		case deltas[i].Equal(rateToday):
			actual = tierToday
		case deltas[i].Equal(rateYd):
			actual = tierYd
		}
		s.TodayCharged = charged
		var preds []string
		for _, c := range quotaCandidates {
			t := c.Tier(s)
			preds = append(preds, fmt.Sprintf("%s→%s", c.Name, t))
			if actual == 0 || t != actual {
				ok[c.Name] = false
			}
		}
		piece := "今仓"
		if s.ConsumedYd {
			piece = "昨仓"
		}
		got := "⚠️ 两档之外"
		if actual != 0 {
			got = actual.String()
		}
		lines = append(lines, fmt.Sprintf("第 %d 笔 今%d昨%d 当日开%d 已收平今%d 消耗%s：收 %s（%s）；预言 %s",
			i+1, s.TodayBefore, s.YdBefore, s.OpenedToday, s.TodayCharged, piece, deltas[i], got, strings.Join(preds, "、")))
		if actual == tierToday {
			charged++
		}
	}
	for _, c := range quotaCandidates {
		if ok[c.Name] {
			alive = append(alive, c.Name)
		}
	}
	if len(alive) == 0 {
		lines = append(lines, "⚠️ **谁都没预言到** —— 先别改本库，把截面拿去重看")
	}
	return alive, lines, nil
}

// quotaRegistered 在下单之前判交易所有没有事前登记。只登记了大商所与郑商所。
func quotaRegistered(exchange string) error {
	if exchange == "DCE" || exchange == "CZCE" {
		return nil
	}
	return fmt.Errorf("⚠️ **不跑**：§13 #23 只对大商所、郑商所事前登记过预言，%s 没有", exchange)
}

// quotaPremise 判开跑前提：今 0、昨恰好 1、本交易日本合约没开过仓。
//
// ⚠️ 「没开过」是 (f) 的前提本身：额度按当日开仓量算，当日已有开仓会让登记表的展开不成立。
// ⚠️ 昨恰好 1（不是 ≥1）：昨 2 时第二笔可能再消耗昨仓，(k) 的预言跟着变，登记表只展开了昨 1 这一种。
func quotaPremise(s longSides) error {
	if s.Today != 0 || s.Yd != 1 || s.OpenedToday != 0 {
		return fmt.Errorf("⚠️ **前提不成立，不跑**：要 今 0 / 昨 1 / 本交易日开过 0，此刻 今 %d / 昨 %d / 开过 %d",
			s.Today, s.Yd, s.OpenedToday)
	}
	return nil
}

// quotaStage 是 ctp-quota 的落盘阶段。
type quotaStage int

const (
	qStageSeed   quotaStage = iota + 1 // ① 只有种子（今 0 昨 1）
	qStageOpened                       // ② 开今两手之后（今 2 昨 1）
	qStageClose1                       // ③ 第 1 笔通用平仓之后
	qStageClose2                       // ④ 第 2 笔之后
	qStageClose3                       // ⑤ 第 3 笔之后
)

func (s quotaStage) tradesExpected() bool { return s != qStageSeed }

func (s quotaStage) note() string {
	switch s {
	case qStageSeed:
		return "ctp-quota ①：只有跨过结算的种子（今 0 昨 1），尚未开今仓"
	case qStageOpened:
		return "ctp-quota ②：开今两手之后（今 2 昨 1），通用平仓之前"
	case qStageClose1:
		return "ctp-quota ③：第 1 笔通用平仓（OF_Close）一手之后"
	case qStageClose2:
		return "ctp-quota ④：第 2 笔通用平仓一手之后"
	case qStageClose3:
		return "ctp-quota ⑤：第 3 笔通用平仓一手之后"
	}
	return fmt.Sprintf("⚠️ ctp-quota：认不得的阶段 %d", int(s))
}

// runCTPQuota 跑 §13 #23 的事前登记实验：今 0 昨 1 → 开今两手 → 通用平仓 1 手 × 3，逐笔读手续费增量。
func runCTPQuota(args []string) error {
	fs := flag.NewFlagSet("ctp-quota", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 DCE.m2701（⚠️ 无默认值：会真的开两手、平三手）")
	dump := fs.String("dump", "", "截面落盘目录（⚠️ 只能是仓库根下的 testdata/ctp）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：本命令会真的开两手、平三手")
	}
	ex, inst := ctp.SplitSymbol(*symbol)
	// ⚠️ 没登记就不许跑 —— 排在连柜台之前（TestQuotaRegisteredRunsBeforeConnect）。
	if err := quotaRegistered(ex); err != nil {
		return err
	}
	if *dump == "" {
		return fmt.Errorf("⚠️ -dump 没有给 —— 本命令不许只打 console")
	}
	abs, err := ctpDumpDir(*dump)
	if err != nil {
		return err
	}
	*dump = abs
	env, err := probe.LoadEnv(*envPath)
	if err != nil {
		return err
	}
	logf := func(f string, a ...any) { fmt.Printf(f+"\n", a...) }
	c := ctp.New(ctp.Credentials{
		Front: env.CTPTdFront, BrokerID: env.CTPBrokerID, UserID: env.CTPUserID,
		Password: env.CTPPassword, AppID: env.CTPAppID, AuthCode: env.CTPAuthCode,
	}, logf)
	c.Valve = ctpValve(env)
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}

	pos0, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	s0 := longSidesOf(pos0, inst)
	logf("[q] 开始：今 %d / 昨 %d / 本交易日开过 %d（记录 %d 条）", s0.Today, s0.Yd, s0.OpenedToday, s0.Records)
	if err := quotaPremise(s0); err != nil {
		return err
	}
	rateToday, rateYd, err := closeFeeRates(c, *symbol, *timeout, logf)
	if err != nil {
		return err
	}
	if rateToday.Equal(rateYd) {
		return fmt.Errorf("⚠️ **不跑**：%s 平今 / 平昨声明同为 %s —— 没有判别力", *symbol, rateToday)
	}

	// 收尾：注册在第一笔委托之前。只平今仓，昨仓不碰（此实验正常走完时账上已空）。
	defer func() {
		if err := closeTodayOnly(c, ex, inst, *timeout, logf); err != nil {
			logf("[q] ⚠️⚠️ **今仓没平干净，仓留在账上了**：%v", err)
		}
	}()
	if err := dumpSlices(c, env, *dump, *timeout, *symbol, qStageSeed, logf); err != nil {
		return err
	}
	for i := 0; i < 2; i++ {
		if _, err := openOneLot(c, ex, inst, 1, *timeout, logf); err != nil {
			return err
		}
	}
	pos1, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	prev := longSidesOf(pos1, inst)
	logf("[q] 开今两手之后：今 %d / 昨 %d / 开过 %d", prev.Today, prev.Yd, prev.OpenedToday)
	if prev.Today != 2 || prev.Yd != 1 {
		return fmt.Errorf("⚠️ 开今两手之后不是 今 2 / 昨 1 —— 不往下平")
	}
	if err := dumpSlices(c, env, *dump, *timeout, *symbol, qStageOpened, logf); err != nil {
		return err
	}

	var steps []quotaStep
	var deltas []decimal.Decimal
	for i, stage := range []quotaStage{qStageClose1, qStageClose2, qStageClose3} {
		accB, err := c.Account(*timeout)
		if err != nil {
			return err
		}
		md, err := c.MarketData(*symbol, *timeout)
		if err != nil {
			return err
		}
		st, err := c.Insert(genericCloseReq(ex, inst, float64(md.LowerLimitPrice)), *timeout)
		if err != nil || st.VolumeTraded == 0 {
			return fmt.Errorf("第 %d 笔通用平仓没成交：status=%q %s err=%v", i+1, string(st.Status), st.StatusMsg, err)
		}
		pos, err := c.Positions(*timeout)
		if err != nil {
			return err
		}
		cur := longSidesOf(pos, inst)
		accA, err := c.Account(*timeout)
		if err != nil {
			return err
		}
		if err := dumpSlices(c, env, *dump, *timeout, *symbol, stage, logf); err != nil {
			return err
		}
		k := closeOrderPieceVerdict(prev, cur)
		logf("[q] 第 %d 笔：今 %d→%d / 昨 %d→%d", i+1, prev.Today, cur.Today, prev.Yd, cur.Yd)
		if k != coConsumedToday && k != coConsumedYesterday {
			return fmt.Errorf("⚠️ 第 %d 笔消耗了哪一片判不出来（判定 %d）—— 手续费判定不作数，停在这里", i+1, int(k))
		}
		steps = append(steps, quotaStep{TodayBefore: prev.Today, YdBefore: prev.Yd, OpenedToday: 2,
			ConsumedYd: k == coConsumedYesterday})
		deltas = append(deltas, decimal.NewFromFloat(float64(accA.Commission)).Sub(decimal.NewFromFloat(float64(accB.Commission))).Round(6))
		prev = cur
	}
	alive, lines, err := quotaVerdict(steps, deltas, rateToday, rateYd)
	if err != nil {
		return err
	}
	logf("")
	for _, l := range lines {
		logf("[q] %s", l)
	}
	logf("[q] ⇒ 三笔都对上的候选：%v", alive)
	return nil
}

// closeOrderPieceVerdict 判一笔「总数少 1」的平仓消耗了哪一片；不要求平之前是今 1（closeOrderVerdict 那条前提只属于 #4）。
func closeOrderPieceVerdict(before, after longSides) closeOrderKind {
	if (before.Today+before.Yd)-(after.Today+after.Yd) != 1 {
		return coNotOneLot
	}
	switch {
	case after.Today == before.Today-1 && after.Yd == before.Yd:
		return coConsumedToday
	case after.Yd == before.Yd-1 && after.Today == before.Today:
		return coConsumedYesterday
	}
	return coNotOneLot
}
