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

// closeFeeMode 是 §13 #21 的两个判别实验。
//
//	E1（bare）只有昨仓时发**通用**平仓 1 手
//	E2（yd）  只有昨仓时发**显式平昨** 1 手
//
// ⚠️ 两个实验各消耗一手昨仓，所以种子要留 2 手；本命令**一次只跑一个**，跑完那一手就没了。
type closeFeeMode int

const (
	closeFeeBare closeFeeMode = iota + 1
	closeFeeYd
)

func (m closeFeeMode) String() string {
	if m == closeFeeYd {
		return "E2（显式平昨）"
	}
	return "E1（通用平仓）"
}

func (m closeFeeMode) offset() def.TThostFtdcOffsetFlagType {
	if m == closeFeeYd {
		return def.TThostFtdcOffsetFlagType(def.THOST_FTDC_OF_CloseYesterday)
	}
	return def.TThostFtdcOffsetFlagType(def.THOST_FTDC_OF_Close)
}

// closeFeeStage 是两份落盘的阶段（与 closeOrderStage 同一条理由：注记由阶段派生，调用点不写中文）。
type closeFeeStage struct {
	mode  closeFeeMode
	after bool
}

func (s closeFeeStage) note() string {
	if s.after {
		return "ctp-closefee " + s.mode.String() + " ②：平仓之后（这一笔收了哪一档手续费，看账户 Commission 的增量）"
	}
	return "ctp-closefee " + s.mode.String() + " ①：只有昨仓、平仓之前"
}

// tradesExpected：①落在本合约当日第一笔成交之前（本命令要求今 0，且不自己开仓）；②在成交之后。
//
// ⚠️ ① 那一份**可能**本来就有当日成交（例如先跑过 E1 再跑 E2）—— 那时附上成交明细只会更全，
// 但 AttachTrades 对空报错是对的，所以这里按「保守不附」处理，证据落在 ② 那一份上。
func (s closeFeeStage) tradesExpected() bool { return s.after }

// closeFeeCandidates 按实测到的手续费增量，判 §13 #21 残余三个候选里哪些还活着。
//
//	(a) 平今档按**平仓前的今仓量**认定：今仓 0 ⇒ 这一笔全走平昨档
//	(b) 大商所上**裸** CLOSE 一律走平今档（显式平昨不是裸平 ⇒ 走平昨档）
//	(c) 按消耗拆档没错，是大商所**行为上的平昨费率** ≠ 声明 ⇒ 两个实验都收「行为平昨费率」
//
// ⚠️ 只判「与谁的预言一致」，不判「谁对」：c 的预言是「行为费率」，它此刻只能由 E1、E2 收到同一个数来支持，
// 而那个数等于声明平今档只是 m2701 上的巧合（0.1 = 0.1）。
func closeFeeCandidates(mode closeFeeMode, delta, rateToday, rateYd decimal.Decimal) (alive []string, why string) {
	eq := func(a, b decimal.Decimal) bool { return a.Equal(b) }
	switch mode {
	case closeFeeBare:
		// a → 平昨档；b → 平今档；c → 行为平昨费率（未知，只能说「不等于声明平昨档就与 a 不符」）
		if eq(delta, rateYd) {
			alive = append(alive, "a")
		}
		if eq(delta, rateToday) {
			alive = append(alive, "b", "c")
		}
		why = fmt.Sprintf("E1 只有昨仓、通用平仓 1 手：a 预言收平昨档 %s、b 预言收平今档 %s、c 预言收「行为平昨费率」（未知，若等于 %s 则与 b 同值）",
			rateYd, rateToday, rateToday)
	case closeFeeYd:
		// a → 平昨档；b → 平昨档（不是裸平）；c → 行为平昨费率
		if eq(delta, rateYd) {
			alive = append(alive, "a", "b")
		}
		if eq(delta, rateToday) {
			alive = append(alive, "c")
		}
		why = fmt.Sprintf("E2 只有昨仓、显式平昨 1 手：a 与 b 都预言收平昨档 %s；c 预言收「行为平昨费率」——"+
			"若收到 %s（= 声明平今档）则 c 活、a 与 b 都被否", rateYd, rateToday)
	}
	if len(alive) == 0 {
		why += fmt.Sprintf("；⚠️ 实测 %s **谁都没预言到** —— 那是第四种可能，先别改本库，把这份截面拿去重看", delta)
	}
	return alive, why
}

// runCTPCloseFee 是 §13 #21 的判别实验 E1 / E2。
//
// ⚠️ **会真的平掉一手昨仓**（那正是实验：种子留了 2 手，两个实验各消耗一手）。
// 前提：该合约多头 今 0、昨 ≥ 1 —— 有今仓就读不清这一笔收的是哪一档，拒绝跑。
// ⚠️ 受保护腿按交易日钉在 20260914/15，今天不在保护期内 ⇒ 用标准安全阀，不豁免。
func runCTPCloseFee(args []string) error {
	fs := flag.NewFlagSet("ctp-closefee", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 DCE.m2701（⚠️ 无默认值：会真的平一手昨仓）")
	mode := fs.String("mode", "", "bare = E1 通用平仓；yd = E2 显式平昨（⚠️ 无默认值）")
	dump := fs.String("dump", "", "两份截面落盘目录（⚠️ 只能是仓库根下的 testdata/ctp —— 落别处会被拒，不是只写在这句里）")
	check := fs.Bool("check", false, "**只读**：打印今昨、当日开过几手与声明费率，不下单")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	// ⚠️ CTP 夹具只有一个家（仓库根下的 testdata/ctp）；落错在此之前不报错，理由见 dumpdir.go
	if *dump != "" {
		abs, err := ctpDumpDir(*dump)
		if err != nil {
			return err
		}
		*dump = abs
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：本命令会真的平一手昨仓")
	}
	var m closeFeeMode
	switch {
	case *check:
	case *mode == "bare":
		m = closeFeeBare
	case *mode == "yd":
		m = closeFeeYd
	default:
		return fmt.Errorf("⚠️ -mode 要显式给 bare（E1 通用平仓）或 yd（E2 显式平昨）—— 两个实验分开的正是这一项")
	}
	if !*check {
		// ⚠️ 在连柜台**之前**判：没有事前登记过候选的交易所，一笔都不许下（见 closeFeeRegistered）。
		exch, _ := ctp.SplitSymbol(*symbol)
		if err := closeFeeRegistered(exch, m); err != nil {
			return err
		}
	}
	if *dump == "" && !*check {
		// ⚠️ 不落盘就不跑：#13 的教训是「判别性的读数躺在一个没拍下来的瞬间里」。
		return fmt.Errorf("⚠️ -dump 没有给 —— 本命令不许只打 console：判别力在两份截面的 Commission 增量上")
	}
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
	ex, inst := ctp.SplitSymbol(*symbol)

	pos0, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	s0 := longSidesOf(pos0, inst)
	logf("[cf] 开始：今 %d / 昨 %d / 本交易日开过 %d（YdPosition 字段 %d，记录 %d 条）",
		s0.Today, s0.Yd, s0.OpenedToday, s0.YdField, s0.Records)

	rateToday, rateYd, err := closeFeeRates(c, *symbol, *timeout, logf)
	if err != nil {
		return err
	}
	acc0, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	logf("[cf] 起点 手续费合计=%.4f", float64(acc0.Commission))
	if *check {
		logf("[cf] 只读模式：不下单。要跑实验加 -mode bare|yd -dump ../../testdata/ctp（在 cmd/oracle 下跑）")
		return nil
	}
	if s0.Yd < 1 {
		return fmt.Errorf("⚠️ **前提不成立，不跑**：%s 多头昨仓 %d 手 —— 两个实验都要「只有昨仓」，"+
			"而昨仓只能靠跨过一次结算拿到，今天补不出来", *symbol, s0.Yd)
	}
	if s0.Today != 0 {
		return fmt.Errorf("⚠️ **前提不成立，不跑**：%s 上有 %d 手多头今仓 —— "+
			"候选 (a) 按「平仓前的今仓量」认定档位，有今仓时这一笔收哪一档读不清", *symbol, s0.Today)
	}
	if err := dumpSlices(c, env, *dump, *timeout, *symbol, closeFeeStage{mode: m}, logf); err != nil {
		return err
	}

	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	// ⚠️ 卖平挂**跌停价**：那是「一定成得了」的那一端（买一必然高于它）。
	// 与 ctp-order 的「挂得上、成不了」相反 —— 本命令要的正是成交。
	req := ctp.OrderReq{Exchange: ex, Instrument: inst,
		Direction: def.THOST_FTDC_D_Sell, Offset: m.offset(),
		Volume: 1, LimitPrice: float64(md.LowerLimitPrice)}
	st, err := insertOrCancel(c, req, *timeout, logf)
	if err != nil {
		return fmt.Errorf("%s 没成交：status=%q %s err=%v", m, string(st.Status), st.StatusMsg, err)
	}
	logf("[cf] %s 成交：%d 手（挂单价 %.2f = 跌停）", m, st.VolumeTraded, float64(md.LowerLimitPrice))

	pos1, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	s1 := longSidesOf(pos1, inst)
	logf("[cf] 平仓之后：今 %d / 昨 %d（YdPosition 字段 %d，记录 %d 条）", s1.Today, s1.Yd, s1.YdField, s1.Records)
	acc1, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	if err := dumpSlices(c, env, *dump, *timeout, *symbol, closeFeeStage{mode: m, after: true}, logf); err != nil {
		return err
	}

	delta := decimal.NewFromFloat(float64(acc1.Commission)).Sub(decimal.NewFromFloat(float64(acc0.Commission)))
	var alive []string
	var why string
	if ex == "CZCE" {
		var verr error
		if alive, why, verr = czceX1(delta, rateToday, rateYd); verr != nil {
			return verr
		}
	} else {
		alive, why = closeFeeCandidates(m, delta, rateToday, rateYd)
	}
	logf("")
	logf("[cf] 手续费 %.4f → %.4f，这一笔收 %s（声明：平今档 %s / 平昨档 %s）",
		float64(acc0.Commission), float64(acc1.Commission), delta, rateToday, rateYd)
	logf("[cf] %s", why)
	logf("[cf] ⇒ 仍然活着的候选：%v", alive)
	// ⚠️ 昨仓真的少了一手才算这一笔平的是昨仓；没少就是别的东西动了，判定不作数。
	if s1.Yd != s0.Yd-1 {
		logf("[cf] ⚠️ 昨仓 %d → %d，不是少一手 —— 这一笔平的未必是昨仓，上面的判定不作数", s0.Yd, s1.Yd)
	}
	return nil
}

// closeFeeRates 取柜台**声明**的平今档 / 平昨档（每手），两档都要求「按手」而不是「按额」。
//
// ⚠️ 按额费率在这个品种上为零（m 是按手收），若哪天不是，这里报错而不是拿一个零去比 ——
// 「收了 0.1」与「按额算出来恰好 0.1」在数上分不开。
func closeFeeRates(c *ctp.Client, symbol string, timeout time.Duration,
	logf func(string, ...any)) (rateToday, rateYd decimal.Decimal, err error) {

	r, err := c.CommissionRate(symbol, timeout)
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	byMoney := decimal.NewFromFloat(float64(r.CloseRatioByMoney)).Add(decimal.NewFromFloat(float64(r.CloseTodayRatioByMoney)))
	if !byMoney.IsZero() {
		return decimal.Zero, decimal.Zero, fmt.Errorf("⚠️ %s 的平仓费率里有**按额**的部分（平昨 %v / 平今 %v）—— "+
			"本实验按「每手固定额」比对，按额时收到的数与档位对不起来，先把口径写清楚再跑",
			symbol, r.CloseRatioByMoney, r.CloseTodayRatioByMoney)
	}
	rateYd = decimal.NewFromFloat(float64(r.CloseRatioByVolume))
	rateToday = decimal.NewFromFloat(float64(r.CloseTodayRatioByVolume))
	logf("[cf] 声明费率（每手）：平昨 %s / 平今 %s", rateYd, rateToday)
	if rateYd.Equal(rateToday) {
		return rateToday, rateYd, fmt.Errorf("⚠️ %s 的平今档与平昨档声明费率相同（都是 %s）—— "+
			"这个品种上两个实验都分不开任何候选，换一个两档不同的品种", symbol, rateYd)
	}
	return rateToday, rateYd, nil
}
