package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
	"github.com/shopspring/decimal"
)

// feeDelta 是一笔成交**前后**账户 Commission 的增量 —— 柜台自己记下的逐笔手续费。
//
// ⚠️ 为什么不从声明费率算：§13 #5 要判的是「结算时有没有重新取整」，输入必须是柜台当日实际累计进去的那个数。
// 用声明费率会混进第二个未知（上期所的 0.005 常数项 #19、平今改写之类），残差就归不了因。
type feeDelta struct {
	TradingDay string `json:"trading_day"`
	At         string `json:"at"`
	Symbol     string `json:"symbol"`
	Round      int    `json:"round"`
	Leg        string `json:"leg"` // open / close
	// Volume 是这一张单的手数（20260918 起；此前的文件没有这一项，读作 0 = 一手）。
	// ⚠️ F10 补测要分「每笔成交 / 每张单 / 每手」截断，手数是 (i-l) 的输入。
	Volume int `json:"volume,omitempty"`
	// Before / After / Fee 用字符串：float64 的 JSON 会把 9.603 写成 9.602999999999999 这类，而判的正是小数位。
	Before string `json:"commission_before"`
	After  string `json:"commission_after"`
	Fee    string `json:"fee"`
	Source string `json:"source"` // 永远是 probe
}

// commissionDelta 把两次账户读数的差算成十进制。纯函数。
//
// ⚠️ 柜台字段是 double：先各自按最短十进制表示转成 decimal 再相减，而不是 float 相减再转 ——
// 后者会把 9.603 − 0 算成 9.602999999999999。
func commissionDelta(before, after float64) decimal.Decimal {
	return decimal.NewFromFloat(after).Sub(decimal.NewFromFloat(before))
}

// runCTPFeeProbe 在一个合约上做 N 个「开一手、平今一手」，逐笔记下柜台累计的手续费增量，并落盘全部成交。
//
// ⚠️ 它是 §13 #5 那个判别实验的**输入端**（事前登记见 state.md 与提交 309922a）：
// 造出带三位以上小数的逐笔费，今晚读次日 PreBalance 时，五个取整候选才分得开。
func runCTPFeeProbe(args []string) error {
	fs := flag.NewFlagSet("ctp-feeprobe", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 DCE.j2701（⚠️ 无默认值：会真的成交）")
	rounds := fs.Int("rounds", 0, "做几个「开 N 手、平今 N 手」（⚠️ 无默认值）")
	volume := fs.Int("volume", 1, "每张单几手（F10 补测分粒度用 2；受 PROBE_MAX_VOLUME 约束）")
	dump := fs.String("dump", "", "落盘目录（⚠️ 必填；只能是仓库根下的 testdata/ctp）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *dump != "" {
		abs, err := ctpDumpDir(*dump)
		if err != nil {
			return err
		}
		*dump = abs
	}
	switch {
	case *symbol == "":
		return fmt.Errorf("⚠️ -symbol 没有默认值：本命令会真的成交")
	case *rounds <= 0:
		return fmt.Errorf("⚠️ -rounds 没有默认值")
	case *volume <= 0:
		return fmt.Errorf("⚠️ -volume 要为正")
	case *dump == "":
		return fmt.Errorf("⚠️ -dump 没有给 —— 逐笔费用不许只打 console")
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

	pos, err := c.Positions(*timeout)
	if err != nil {
		return err
	}
	if s := longSidesOf(pos, inst); s.Today != 0 || s.Yd != 0 {
		return fmt.Errorf("⚠️ **不跑**：%s 上已有多头 今 %d / 昨 %d —— 平今那一笔可能碰到别人的仓", *symbol, s.Today, s.Yd)
	}

	var deltas []feeDelta
	record := func(round int, leg string, before, after float64) {
		d := commissionDelta(before, after)
		deltas = append(deltas, feeDelta{TradingDay: c.TradingDay(), At: nowClock(), Symbol: *symbol,
			Round: round, Leg: leg, Volume: *volume, Before: decimal.NewFromFloat(before).String(),
			After: decimal.NewFromFloat(after).String(), Fee: d.String(), Source: "probe"})
		logf("[fee] 第 %d 轮 %s：手续费 %s → %s，这一笔 %s", round, leg,
			decimal.NewFromFloat(before), decimal.NewFromFloat(after), d)
	}
	var runErr error
	for r := 1; r <= *rounds && runErr == nil; r++ {
		md, err := c.MarketData(*symbol, *timeout)
		if err != nil {
			runErr = err
			break
		}
		a0, err := c.Account(*timeout)
		if err != nil {
			runErr = err
			break
		}
		st, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
			Volume: *volume, LimitPrice: float64(md.UpperLimitPrice)}, *timeout)
		if err != nil || int(st.VolumeTraded) != *volume {
			runErr = fmt.Errorf("第 %d 轮开仓没成交 %d 手：status=%q %s err=%v", r, *volume, string(st.Status), st.StatusMsg, err)
			break
		}
		a1, err := c.Account(*timeout)
		if err != nil {
			runErr = err
			break
		}
		record(r, "open", float64(a0.Commission), float64(a1.Commission))
		cs, err := c.Insert(ctp.OrderReq{Exchange: ex, Instrument: inst,
			Direction: def.THOST_FTDC_D_Sell, Offset: def.THOST_FTDC_OF_CloseToday,
			Volume: *volume, LimitPrice: float64(md.LowerLimitPrice)}, *timeout)
		if err != nil || int(cs.VolumeTraded) != *volume {
			runErr = fmt.Errorf("⚠️⚠️ 第 %d 轮平今没成交 %d 手，**账上留着今仓** —— 跑 `ctp-flatten -symbol %s`：status=%q %s err=%v",
				r, *volume, *symbol, string(cs.Status), cs.StatusMsg, err)
			break
		}
		a2, err := c.Account(*timeout)
		if err != nil {
			runErr = err
			break
		}
		record(r, "close", float64(a1.Commission), float64(a2.Commission))
	}

	// ⚠️ **先落盘再报错**：跑到一半失败时，已经成交的那几笔同样是 #5 的输入，丢了今晚就对不上。
	if len(deltas) > 0 {
		// ⚠️ 逐笔增量落在 testdata/refdata，**不**落在截面目录：testdata/ctp 的加载器把每个 json 当截面读，
		// 读不懂就红（那是对的）。它与拒因语料是同一类东西 —— 探针写的机器可读语料。
		refdata := filepath.Join(filepath.Dir(*dump), "refdata")
		if werr := writeFeeDeltas(refdata, deltas, logf); werr != nil && runErr == nil {
			runErr = werr
		}
		// ⚠️ 走 dumpSlices（本包唯一的 AttachTrades 调用点）：补不上成交明细就整份不落盘。
		cerr := dumpSlices(c, env, *dump, *timeout, *symbol, feeProbeStage{}, logf)
		if cerr != nil && runErr == nil {
			runErr = cerr
		}
	}
	return runErr
}

// writeFeeDeltas 把逐笔增量落成 JSON。⚠️ 只有数、合约名与时刻，没有账户标识。
func writeFeeDeltas(dir string, deltas []feeDelta, logf func(string, ...any)) error {
	b, err := json.MarshalIndent(deltas, "", "  ")
	if err != nil {
		return err
	}
	day := deltas[0].TradingDay
	path := filepath.Join(dir, fmt.Sprintf("ctp-fee-deltas-%s.json", day))
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			break
		}
		path = filepath.Join(dir, fmt.Sprintf("ctp-fee-deltas-%s-%d.json", day, i))
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return err
	}
	logf("[fee] 逐笔手续费落盘 %s（%d 笔）", path, len(deltas))
	return nil
}

// feeProbeStage 是 ctp-feeprobe 收尾那一份截面的阶段注记。
type feeProbeStage struct{}

func (feeProbeStage) note() string {
	return "ctp-feeprobe：逐笔手续费实验收尾 —— 全部「开一手、平今一手」之后（§13 #5 的输入）"
}

// tradesExpected：收尾时本合约当日一定已有成交。
func (feeProbeStage) tradesExpected() bool { return true }
