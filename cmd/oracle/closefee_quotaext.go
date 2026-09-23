package main

import (
	"flag"
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/shopspring/decimal"
)

// extCase 是 F11 两条外推的判别实验（design.md 门面形状 §15「决策点 2」；登记块「事前登记：F11 两条外推」在 docs/state.md）。
type extCase int

const (
	// extExplicit 是 B：显式平今之后的裸平 —— 显式平今扣不扣额度。
	extExplicit extCase = iota + 1
	// extOpposite 是 C：当日开过反方向之后的裸平 —— 额度分不分方向。
	extOpposite
	// extMulti 是 A：多手裸平、0 < 额度 < 平仓量 —— 按额度拆 / 全今 / 全昨。
	extMulti
	// extRewrite 是 §13 #25 的复现：显式平今是不是被柜台改写成通用平仓（改写说 / 标志说）。
	extRewrite
)

func (c extCase) String() string {
	switch c {
	case extExplicit:
		return "B 显式平今之后的裸平"
	case extOpposite:
		return "C 当日开过反方向之后的裸平"
	case extMulti:
		return "A 多手裸平（额度 1、平 2 手）"
	case extRewrite:
		return "§13 #25 复现 显式平今是不是被改写成通用平仓"
	}
	return fmt.Sprintf("⚠️ 认不得的外推 %d", int(c))
}

// extReading 是一条外推的一种读法，与它在判别那一笔上的预言：共 Lots 手，其中 TodayLots 手按平今档、其余按平昨档。
type extReading struct {
	Name            string
	TodayLots, Lots int
}

// fee 是这一读法预言的判别那一笔手续费合计。
func (r extReading) fee(rateToday, rateYd decimal.Decimal) decimal.Decimal {
	return rateToday.Mul(decimal.NewFromInt(int64(r.TodayLots))).Add(rateYd.Mul(decimal.NewFromInt(int64(r.Lots - r.TodayLots))))
}

// tiers 把预言写成人读的样子，例如「平昨档」「1 手平今 + 1 手平昨」。
func (r extReading) tiers() string {
	switch {
	case r.Lots == 1 && r.TodayLots == 1:
		return tierToday.String()
	case r.Lots == 1:
		return tierYd.String()
	}
	return fmt.Sprintf("%d 手平今 + %d 手平昨", r.TodayLots, r.Lots-r.TodayLots)
}

// extReadings 是登记表：起点 多头 今 0 / 昨 1 / 开过 0、空头 0 时，判别那一笔按各读法收哪一档。
//
//	B ② 买开 1 → ③ 显式平今 1（消耗今仓）→ ④ 裸平 1（消耗昨仓）：额度 扣 = 1 − 1 = 0、不扣 = 1 − 0 = 1
//	C ② 卖开 1 → ③ 裸平多头 1（消耗昨仓）：多头额度 分方向 = 0 − 0 = 0、不分方向 = (0 + 1) − 0 = 1
//	A ② 买开 1 → ③ 一笔裸平 2 手：额度 1 ⇒ 按额度拆 = 1 今 1 昨、全今 = 2 今、全昨 = 2 昨
//
// ⚠️ 这张表是**事前登记**的；TestExtReadingsMatchPreRegistration 把它与 state.md 登记块的表逐行比。
func extReadings(c extCase) []extReading {
	switch c {
	case extExplicit:
		return []extReading{{"扣", 0, 1}, {"不扣", 1, 1}}
	case extOpposite:
		return []extReading{{"分方向", 0, 1}, {"不分方向", 1, 1}}
	case extMulti:
		return []extReading{{"按额度拆", 1, 2}, {"全今", 2, 2}, {"全昨", 0, 2}}
	}
	return nil
}

// extVerdict 按判别那一笔的手续费增量判哪种读法活着：增量等于该读法预言的合计才活；谁的都不是 ⇒ 谁都不活。
func extVerdict(c extCase, delta, rateToday, rateYd decimal.Decimal) (alive []string, err error) {
	if rateToday.Equal(rateYd) {
		return nil, fmt.Errorf("⚠️ 平今档与平昨档都是 %s —— 没有判别力，不判", rateToday)
	}
	rs := extReadings(c)
	if len(rs) == 0 {
		return nil, fmt.Errorf("⚠️ %v 没有登记读法", c)
	}
	for _, r := range rs {
		if delta.Equal(r.fee(rateToday, rateYd)) {
			alive = append(alive, r.Name)
		}
	}
	return alive, nil
}

// extRegistered 在下单之前判：只对大商所、郑商所事前登记过。
func extRegistered(exchange string, c extCase) error {
	if c != extExplicit && c != extOpposite && c != extMulti && c != extRewrite {
		return fmt.Errorf("⚠️ -case 要显式给 explicit（B）、opposite（C）、multi（A）或 rewrite（§13 #25 复现）")
	}
	if exchange == "DCE" || exchange == "CZCE" {
		return nil
	}
	return fmt.Errorf("⚠️ **不跑**：F11 外推只对大商所、郑商所事前登记过预言，%s 没有", exchange)
}

// extPremise：多头 今 0 / 昨 1 / 本交易日开过 0，空头一手都没有（持仓与开过都为 0）。
//
// ⚠️ 「开过 0」是额度从 0 起算的前提；空头开过 0 是 C 的前提（C 要的正是「只有这一手反方向」）。
func extPremise(long, short longSides) error {
	if long.Today != 0 || long.Yd != 1 || long.OpenedToday != 0 {
		return fmt.Errorf("⚠️ **前提不成立，不跑**：要多头 今 0 / 昨 1 / 开过 0，此刻 今 %d / 昨 %d / 开过 %d",
			long.Today, long.Yd, long.OpenedToday)
	}
	if short.Today != 0 || short.Yd != 0 || short.OpenedToday != 0 {
		return fmt.Errorf("⚠️ **前提不成立，不跑**：要空头 0，此刻 今 %d / 昨 %d / 开过 %d", short.Today, short.Yd, short.OpenedToday)
	}
	return nil
}

// extStage 是 ctp-quota-ext 的落盘阶段。
type extStage struct {
	c extCase
	n int // 1 起点 / 2 开仓之后 / 3 第三步之后 / 4 第四步之后
}

func (s extStage) tradesExpected() bool { return s.n != 1 }

func (s extStage) note() string {
	b := map[int]string{
		1: "ctp-quota-ext B ①：只有跨过结算的种子（多头 今 0 昨 1）",
		2: "ctp-quota-ext B ②：买开一手之后（今 1 昨 1）",
		3: "ctp-quota-ext B ③：显式平今（OF_CloseToday）一手之后",
		4: "ctp-quota-ext B ④：通用平仓（OF_Close）一手之后 —— 判别那一笔",
	}
	c := map[int]string{
		1: "ctp-quota-ext C ①：只有跨过结算的种子（多头 今 0 昨 1，空头 0）",
		2: "ctp-quota-ext C ②：卖开一手之后（空头 今 1）",
		3: "ctp-quota-ext C ③：通用平仓（OF_Close）卖一手平多头之后 —— 判别那一笔",
		4: "ctp-quota-ext C ④：收尾，显式平今买一手平掉空头之后",
	}
	a := map[int]string{
		1: "ctp-quota-ext A ①：只有跨过结算的种子（多头 今 0 昨 1）",
		2: "ctp-quota-ext A ②：买开一手之后（今 1 昨 1）",
		3: "ctp-quota-ext A ③：一笔通用平仓（OF_Close）卖 2 手之后 —— 判别那一笔",
	}
	w := map[int]string{
		1: "ctp-quota-ext #25 ①：只有跨过结算的种子（多头 今 0 昨 1）",
		2: "ctp-quota-ext #25 ②：买开一手之后（今 1 昨 1，额度 1）",
		3: "ctp-quota-ext #25 ③：第 1 笔显式平今（OF_CloseToday）之后 —— 判消耗了哪一片",
		4: "ctp-quota-ext #25 ④：第 2 笔显式平今之后（额度 0）—— 判收哪一档 / 被不被拒",
	}
	m := b
	switch s.c {
	case extOpposite:
		m = c
	case extMulti:
		m = a
	case extRewrite:
		m = w
	}
	if t, ok := m[s.n]; ok {
		return t
	}
	return fmt.Sprintf("⚠️ ctp-quota-ext：认不得的阶段 %v / %d", s.c, s.n)
}

func explicitCloseTodayReq(ex, inst string, lowerLimit float64) ctp.OrderReq {
	return ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: lowerLimit}
}

// rejectedByCounter 判一笔没成交的委托是不是**柜台拒的**（有数值错误码）——
// 与「没有结论」（超时 / 撤单没核干净）分开：#25 的 ④ 上「被拒」本身是判据之一，不是故障（登记块写明）。
func rejectedByCounter(st ctp.OrderState, err error) bool {
	return err != nil && st.ErrorID != 0
}

// rewriteVerdict 判 §13 #25 复现的两说谁活着（纯函数；登记块「事前登记：§13 #25 复现」在 docs/state.md）。
//
//	③ 显式平今：改写说 ⇒ 消耗昨仓；标志说 ⇒ 消耗今仓（费用两说同为平今档，不判别）
//	④ 额度 0 时再发显式平今：改写说 ⇒ 按通用平仓收**平昨档**（不论 ③ 消耗的是哪一片）；
//	   标志说 ⇒ ③ 消耗今时账上只剩昨仓、可平今 0 ⇒ **柜台拒单**；③ 消耗昨时账上剩今仓 ⇒ 收平今档
//
// rejected = ④ 被柜台拒（有错误码）。两处同向才算复现。
func rewriteVerdict(consumedYd, rejected bool, delta, rateToday, rateYd decimal.Decimal) (alive []string, why string) {
	rewriteOK, flagOK := consumedYd, !consumedYd
	switch {
	case rejected:
		// 拒单：改写说预言成交（平昨档），标志说只在「③ 消耗今、账上只剩昨仓」时预言拒单
		rewriteOK = false
		flagOK = flagOK && true
	case consumedYd:
		rewriteOK = rewriteOK && delta.Equal(rateYd)
		flagOK = flagOK && delta.Equal(rateToday)
	default:
		rewriteOK = rewriteOK && delta.Equal(rateYd)
		flagOK = false // 标志说在这一支预言拒单，而 ④ 成交了
	}
	if rewriteOK {
		alive = append(alive, "改写说")
	}
	if flagOK {
		alive = append(alive, "标志说")
	}
	piece, tail := "今仓", fmt.Sprintf("④ 收 %s（平今档 %s / 平昨档 %s）", delta, rateToday, rateYd)
	if consumedYd {
		piece = "昨仓"
	}
	if rejected {
		tail = "④ 被柜台拒单"
	}
	return alive, fmt.Sprintf("③ 消耗%s；%s", piece, tail)
}

// closeRecordsOn 数某合约的卖平成交记录：条数与手数合计（A 用它判 ③ 是不是**一条** 2 手的记录）。
func closeRecordsOn(trades []*def.CThostFtdcTradeField, inst string) (n, lots int) {
	for _, tr := range trades {
		if tr == nil || ctp.Text(tr.InstrumentID[:]) != inst || tr.Direction != def.THOST_FTDC_D_Sell || tr.OffsetFlag == def.THOST_FTDC_OF_Open {
			continue
		}
		n++
		lots += int(tr.Volume)
	}
	return n, lots
}

// multiRecordVerdict 判 ③ 新增的卖平成交记录是不是恰好一条 2 手。
//
// ⚠️ 本库在 F11 里报错的形状是**一条** 2 手的成交记录；柜台若拆成两条 1 手，本库本来就逐条算（额度 1 ⇒ 平今、额度 0 ⇒ 平昨），
// 合计与「按额度拆」同值 —— 回答不了 A 的问题（评审 20260922）。那时 A 不判，「柜台拆成 N 条」另记一条观测。
func multiRecordVerdict(nBefore, lotsBefore, nAfter, lotsAfter int) error {
	dn, dl := nAfter-nBefore, lotsAfter-lotsBefore
	if dn == 1 && dl == 2 {
		return nil
	}
	return fmt.Errorf("⚠️ **A 不判**：③ 新增 %d 条卖平成交记录、合计 %d 手（要恰好 1 条 2 手）—— "+
		"柜台把这笔 2 手拆成了多条回报，这本身记一条观测，但不是 A 的判据", dn, dl)
}

// multiCloseReq 是 A 的判别那一笔：一笔通用平仓（OF_Close）卖 2 手。标志必须是通用的，同 genericCloseReq。
func multiCloseReq(ex, inst string, lowerLimit float64) ctp.OrderReq {
	r := genericCloseReq(ex, inst, lowerLimit)
	r.Volume = 2
	return r
}

func shortOpenReq(ex, inst string, lowerLimit float64) ctp.OrderReq {
	return ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_Open,
		Volume: 1, LimitPrice: lowerLimit}
}

func shortCloseTodayReq(ex, inst string, upperLimit float64) ctp.OrderReq {
	return ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_CloseToday,
		Volume: 1, LimitPrice: upperLimit}
}

// closeShortTodayOnly 用 CloseToday 买平该合约空头的今仓（C 的收尾）；多头一手不碰。
func closeShortTodayOnly(c *ctp.Client, ex, inst string, timeout time.Duration, logf func(string, ...any)) error {
	for i := 0; i < 3; i++ {
		pos, err := c.Positions(timeout)
		if err != nil {
			return err
		}
		s := shortSidesOf(pos, inst)
		if s.Today == 0 {
			logf("[qx] 收尾：空头今仓已清空")
			return nil
		}
		md, err := c.MarketData(ex+"."+inst, timeout)
		if err != nil {
			return err
		}
		if _, err := insertOrCancel(c, shortCloseTodayReq(ex, inst, float64(md.UpperLimitPrice)), timeout, logf); err != nil {
			return fmt.Errorf("空头平今：%w", err)
		}
	}
	return fmt.Errorf("⚠️ 平了 3 次空头今仓还没清空 —— 停手，去看账户")
}

// runCTPQuotaExt 跑 F11 两条外推的事前登记实验，逐笔读手续费增量。
//
// ⚠️ 会真的下单：B 开 1、显式平今 1、裸平 1（种子被平掉）；C 卖开 1、裸平多头 1（种子被平掉）、买平今 1。每笔一手。
func runCTPQuotaExt(args []string) error {
	fs := flag.NewFlagSet("ctp-quota-ext", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 DCE.m2701（⚠️ 无默认值：会真的下单、平掉种子）")
	caseName := fs.String("case", "", "explicit = B 显式平今之后的裸平；opposite = C 当日开过反方向；multi = A 一笔裸平 2 手；rewrite = §13 #25 复现（⚠️ 无默认值；multi 要 PROBE_MAX_VOLUME ≥ 2）")
	dump := fs.String("dump", "", "截面落盘目录（⚠️ 只能是仓库根下的 testdata/ctp）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：本命令会真的下单")
	}
	var cs extCase
	switch *caseName {
	case "explicit":
		cs = extExplicit
	case "opposite":
		cs = extOpposite
	case "multi":
		cs = extMulti
	case "rewrite":
		cs = extRewrite
	}
	ex, inst := ctp.SplitSymbol(*symbol)
	// ⚠️ 没登记就不许跑 —— 排在连柜台之前（TestExtRegisteredRunsBeforeConnect）。
	if err := extRegistered(ex, cs); err != nil {
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
	sides := func() (longSides, longSides, error) {
		pos, err := c.Positions(*timeout)
		if err != nil {
			return longSides{}, longSides{}, err
		}
		return longSidesOf(pos, inst), shortSidesOf(pos, inst), nil
	}
	long0, short0, err := sides()
	if err != nil {
		return err
	}
	logf("[qx] %v 开始：多头 今 %d / 昨 %d / 开过 %d；空头 今 %d / 昨 %d / 开过 %d",
		cs, long0.Today, long0.Yd, long0.OpenedToday, short0.Today, short0.Yd, short0.OpenedToday)
	if err := extPremise(long0, short0); err != nil {
		return err
	}
	rateToday, rateYd, err := closeFeeRates(c, *symbol, *timeout, logf)
	if err != nil {
		return err
	}
	if rateToday.Equal(rateYd) {
		return fmt.Errorf("⚠️ **不跑**：%s 平今 / 平昨声明同为 %s —— 没有判别力", *symbol, rateToday)
	}
	// 收尾：注册在第一笔委托之前。只平今仓（多头与空头），昨仓（种子）不碰。
	defer func() {
		// ⚠️ 先撤挂单、再平仓（评审 20260922）：挂着的平仓单冻住可平量，先平的话平今单会因可平量不足被拒、今仓留在账上。
		if err := sweepLive(c, inst, *timeout, logf); err != nil {
			logf("[qx] ⚠️⚠️ **收尾第一道撤单没撤干净，去看账户**：%v", err)
		}
		if err := closeTodayOnly(c, ex, inst, *timeout, logf); err != nil {
			logf("[qx] ⚠️⚠️ **多头今仓没平干净**：%v", err)
		}
		if err := closeShortTodayOnly(c, ex, inst, *timeout, logf); err != nil {
			logf("[qx] ⚠️⚠️ **空头今仓没平干净**：%v", err)
		}
		// 最后一道：本合约不许留活委托（没成交的单挂在柜台上，工具退出后随时会成交）。
		if err := sweepLive(c, inst, *timeout, logf); err != nil {
			logf("[qx] ⚠️⚠️ **本合约还有活委托，去看账户**：%v", err)
		} else {
			logf("[qx] 收尾：%s 没有活委托", inst)
		}
	}()
	commission := func() (decimal.Decimal, error) {
		a, err := c.Account(*timeout)
		if err != nil {
			return decimal.Zero, err
		}
		return decimal.NewFromFloat(float64(a.Commission)).Round(6), nil
	}
	// step 发一笔一手委托，返回手续费增量与前后持仓。
	step := func(n int, mk func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq) (decimal.Decimal, longSides, longSides, error) {
		b, err := commission()
		if err != nil {
			return decimal.Zero, longSides{}, longSides{}, err
		}
		md, err := c.MarketData(*symbol, *timeout)
		if err != nil {
			return decimal.Zero, longSides{}, longSides{}, err
		}
		if _, err := insertOrCancel(c, mk(md), *timeout, logf); err != nil {
			return decimal.Zero, longSides{}, longSides{}, fmt.Errorf("第 %d 步：%w", n, err)
		}
		l, s, err := sides()
		if err != nil {
			return decimal.Zero, longSides{}, longSides{}, err
		}
		a, err := commission()
		if err != nil {
			return decimal.Zero, longSides{}, longSides{}, err
		}
		if err := dumpSlices(c, env, *dump, *timeout, *symbol, extStage{cs, n}, logf); err != nil {
			return decimal.Zero, longSides{}, longSides{}, err
		}
		d := a.Sub(b)
		logf("[qx] 第 %d 步：手续费增量 %s；多头 今 %d / 昨 %d；空头 今 %d / 昨 %d", n, d, l.Today, l.Yd, s.Today, s.Yd)
		return d, l, s, nil
	}

	if err := dumpSlices(c, env, *dump, *timeout, *symbol, extStage{cs, 1}, logf); err != nil {
		return err
	}
	var delta decimal.Decimal
	switch cs {
	case extExplicit:
		_, l, _, err := step(2, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return ctp.OrderReq{Exchange: ex, Instrument: inst, Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
				Volume: 1, LimitPrice: float64(md.UpperLimitPrice)}
		})
		if err != nil {
			return err
		}
		if l.Today != 1 || l.Yd != 1 {
			return fmt.Errorf("⚠️ 买开之后不是 今 1 / 昨 1 —— 不往下平")
		}
		d3, l3, _, err := step(3, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return explicitCloseTodayReq(ex, inst, float64(md.LowerLimitPrice))
		})
		if err != nil {
			return fmt.Errorf("⚠️ B 不判（③ 显式平今没成交）：%w", err)
		}
		logf("[qx] ③ 显式平今收 %s（平今档 %s / 平昨档 %s；既有行为，不是判据）", d3, rateToday, rateYd)
		if k := closeOrderPieceVerdict(l, l3); k != coConsumedToday {
			return fmt.Errorf("⚠️ **B 不判**：③ 显式平今没有恰好消耗一手今仓（今 %d→%d / 昨 %d→%d）—— "+
				"消耗了昨仓说明显式平今被改写成了通用平仓，这本身是一条观测，记进 §13", l.Today, l3.Today, l.Yd, l3.Yd)
		}
		d4, l4, _, err := step(4, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return genericCloseReq(ex, inst, float64(md.LowerLimitPrice))
		})
		if err != nil {
			return err
		}
		if k := closeOrderPieceVerdict(l3, l4); k != coConsumedYesterday {
			return fmt.Errorf("⚠️ **B 不判**：④ 没有恰好消耗一手昨仓（今 %d→%d / 昨 %d→%d）", l3.Today, l4.Today, l3.Yd, l4.Yd)
		}
		delta = d4
	case extOpposite:
		_, l, s, err := step(2, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return shortOpenReq(ex, inst, float64(md.LowerLimitPrice))
		})
		if err != nil {
			return err
		}
		if s.Today != 1 || l.Today != 0 || l.Yd != 1 {
			return fmt.Errorf("⚠️ 卖开之后不是 空头今 1、多头 今 0 / 昨 1 —— 不往下平")
		}
		d3, l3, s3, err := step(3, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return genericCloseReq(ex, inst, float64(md.LowerLimitPrice))
		})
		if err != nil {
			return err
		}
		if l3.Today != 0 || l3.Yd != 0 || s3.Today != 1 || s3.Yd != 0 {
			return fmt.Errorf("⚠️ **C 不判**：③ 之后不是 多头 0 / 空头今 1（多头 今 %d 昨 %d；空头 今 %d 昨 %d）", l3.Today, l3.Yd, s3.Today, s3.Yd)
		}
		delta = d3
		d4, _, s4, err := step(4, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return shortCloseTodayReq(ex, inst, float64(md.UpperLimitPrice))
		})
		if err != nil {
			return fmt.Errorf("⚠️ 判别那一笔已记下（%s），收尾没成交：%w", delta, err)
		}
		logf("[qx] ④ 收尾平空头收 %s（不进判据）；空头剩 今 %d", d4, s4.Today)
	case extMulti:
		_, l, _, err := step(2, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return ctp.OrderReq{Exchange: ex, Instrument: inst, Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
				Volume: 1, LimitPrice: float64(md.UpperLimitPrice)}
		})
		if err != nil {
			return err
		}
		if l.Today != 1 || l.Yd != 1 {
			return fmt.Errorf("⚠️ 买开之后不是 今 1 / 昨 1 —— 不往下平")
		}
		tr0, err := c.Trades(*timeout)
		if err != nil {
			return fmt.Errorf("⚠️ ③ 之前查不到成交记录（没有结论）—— 不往下平：%w", err)
		}
		n0, lots0 := closeRecordsOn(tr0, inst)
		d3, l3, _, err := step(3, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return multiCloseReq(ex, inst, float64(md.LowerLimitPrice))
		})
		if err != nil {
			return fmt.Errorf("⚠️ **A 不判**（③ 2 手没有全部成交，余量已撤）：%w", err)
		}
		if l3.Today != 0 || l3.Yd != 0 {
			return fmt.Errorf("⚠️ **A 不判**：③ 之后不是 今 0 / 昨 0（今 %d 昨 %d）", l3.Today, l3.Yd)
		}
		tr1, err := c.Trades(*timeout)
		if err != nil {
			return fmt.Errorf("⚠️ **A 不判**：③ 之后查不到成交记录（没有结论；增量 %s 照记）：%w", d3, err)
		}
		n1, lots1 := closeRecordsOn(tr1, inst)
		logf("[qx] ③ 新增卖平成交记录 %d 条、合计 %d 手", n1-n0, lots1-lots0)
		if err := multiRecordVerdict(n0, lots0, n1, lots1); err != nil {
			return fmt.Errorf("%w（增量 %s 照记）", err, d3)
		}
		delta = d3
	case extRewrite:
		_, l, _, err := step(2, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return ctp.OrderReq{Exchange: ex, Instrument: inst, Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
				Volume: 1, LimitPrice: float64(md.UpperLimitPrice)}
		})
		if err != nil {
			return err
		}
		if l.Today != 1 || l.Yd != 1 {
			return fmt.Errorf("⚠️ 买开之后不是 今 1 / 昨 1 —— 不往下平")
		}
		d3, l3, _, err := step(3, func(md *def.CThostFtdcDepthMarketDataField) ctp.OrderReq {
			return explicitCloseTodayReq(ex, inst, float64(md.LowerLimitPrice))
		})
		if err != nil {
			return fmt.Errorf("⚠️ **#25 不判**（③ 显式平今没成交）：%w", err)
		}
		k := closeOrderPieceVerdict(l, l3)
		if k != coConsumedToday && k != coConsumedYesterday {
			return fmt.Errorf("⚠️ **#25 不判**：③ 消耗了哪一片判不出来（今 %d→%d / 昨 %d→%d）", l.Today, l3.Today, l.Yd, l3.Yd)
		}
		consumedYd := k == coConsumedYesterday
		logf("[qx] ③ 显式平今收 %s，消耗%s（改写说预言昨仓、标志说预言今仓；这一笔的费用两说同为平今档 %s，不判别）",
			d3, map[bool]string{true: "昨仓", false: "今仓"}[consumedYd], rateToday)

		// ④ 额度已被 ③ 用掉：改写说预言平昨档成交；标志说在「③ 消耗今仓」那一支预言柜台拒单。
		// ⚠️ 被拒是**判据之一**，不是故障 —— 所以这里把「有错误码的拒单」与「没有结论」分开。
		accB, err := commission()
		if err != nil {
			return err
		}
		md, err := c.MarketData(*symbol, *timeout)
		if err != nil {
			return err
		}
		st4, err4 := insertOrCancel(c, explicitCloseTodayReq(ex, inst, float64(md.LowerLimitPrice)), *timeout, logf)
		l4, _, err := sides()
		if err != nil {
			return err
		}
		accA, err := commission()
		if err != nil {
			return err
		}
		if derr := dumpSlices(c, env, *dump, *timeout, *symbol, extStage{cs, 4}, logf); derr != nil {
			return derr
		}
		d4 := accA.Sub(accB)
		rejected := rejectedByCounter(st4, err4)
		logf("[qx] ④ 手续费增量 %s；多头 今 %d / 昨 %d；状态 %q 错误码 %d", d4, l4.Today, l4.Yd, string(st4.Status), st4.ErrorID)
		if err4 != nil && !rejected {
			return fmt.Errorf("⚠️ **#25 不判**：④ 既没成交也没有错误码（没有结论）：%w", err4)
		}
		if !rejected && (l4.Today != 0 || l4.Yd != 0) {
			return fmt.Errorf("⚠️ **#25 不判**：④ 成交了而账上不是 今 0 / 昨 0（今 %d 昨 %d）", l4.Today, l4.Yd)
		}
		alive, why := rewriteVerdict(consumedYd, rejected, d4, rateToday, rateYd)
		logf("")
		logf("[qx] %s ⇒ 活着的读法：%v", why, alive)
		if len(alive) == 0 {
			logf("[qx] ⚠️ **谁都没预言到** —— 先别改本库，把截面拿去重看")
		}
		return nil
	}
	alive, err := extVerdict(cs, delta, rateToday, rateYd)
	if err != nil {
		return err
	}
	logf("")
	for _, r := range extReadings(cs) {
		logf("[qx] 读法 %s 预言 %s = %s", r.Name, r.tiers(), r.fee(rateToday, rateYd))
	}
	logf("[qx] 判别那一笔收 %s（平今档 %s / 平昨档 %s）⇒ 活着的读法：%v", delta, rateToday, rateYd, alive)
	if len(alive) == 0 {
		logf("[qx] ⚠️ **谁都没预言到** —— 先别改本库，把截面拿去重看")
	}
	return nil
}
