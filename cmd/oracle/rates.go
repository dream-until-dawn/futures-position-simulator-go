package main

import (
	"flag"
	"fmt"
	"math"
	"strings"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

// runCTPRates 查柜台**声明**的手续费率，一个合约一行六个数。
//
// # ⚠️ 它要做两件事，而第二件才是它被写出来的理由
//
// 一、把手续费公式的六个参数从**反解**变成**直读**。此前它们全是从冻结额
// 解出来的（三个点解两个参数，余量当证据），而柜台一直能直接说出来。
//
// 二、**给 `rules_pending` #5 找一个能分开候选的合约。**
// #5 卡住的不是样本数：`rb` 的费率是 `1e-4`，而 `价 × 10 × 1e-4 = 价 × 1e-3`
// 天然三位、`+0.005` 还是三位 ⇒ **「不取整」与「取到三位或更细」给出同一个数**。
//
//	⚠️ 那是**一个盲区，不是两个** —— 再多跑几次同一个品种，
//	拿到的仍然是同一个盲区的第 N 个样本。
//
// ⇒ 要分开它得换一个**能产生更多小数位**的费率。本命令为此在每一行末尾
// 打印「按当前行情，名义金额 × 费率会产生几位小数」，并把位数 ≥ 4 的标出来。
//
// # ⚠️ 事前预测（写在跑之前，20260911 夜盘）
//
// 依 §13 #8 在 `SHFE.rb2701` 上实测的三类点（开仓挂单 / 平昨挂单 / 平昨成交 /
// 平今挂单），公式是 `名义 × 0.0001 + 手数 × 0.005`，且**三档同费率**。
// ⇒ 若声明与行为一致，rb 这一行应当读到：
//
//	OpenRatioByMoney        0.0001     OpenRatioByVolume        0.005
//	CloseRatioByMoney       0.0001     CloseRatioByVolume       0.005
//	CloseTodayRatioByMoney  0.0001     CloseTodayRatioByVolume  0.005
//
// ⚠️ **对不上才是新东西**：那说明我把从冻结额解出来的那组数，
// 读成了一句比它实际管得更宽的话 —— 与 §13 #1 `MarginPriceType` 那次同形。
// ⚠️ 而对得上也**不升格证据等级**：声明与行为来自同一个柜台，
// 换的是查询路径，不是来源。
func runCTPRates(args []string) error {
	fs := flag.NewFlagSet("ctp-rates", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbols := fs.String("symbols", "", "合约，逗号分隔，形如 SHFE.rb2701,DCE.m2701（⚠️ 无默认值）")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if strings.TrimSpace(*symbols) == "" {
		return fmt.Errorf("⚠️ -symbols 没有默认值")
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
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}
	logf("")
	logf("%-16s %12s %10s %12s %10s %12s %10s", "合约",
		"开仓/按额", "开仓/每手", "平昨/按额", "平昨/每手", "平今/按额", "平今/每手")
	var interesting []string
	for _, sym := range strings.Split(*symbols, ",") {
		sym = strings.TrimSpace(sym)
		if sym == "" {
			continue
		}
		r, err := c.CommissionRate(sym, *timeout)
		if err != nil {
			logf("%-16s ⚠️ %v", sym, err)
			continue
		}
		logf("%-16s %12.8g %10.8g %12.8g %10.8g %12.8g %10.8g", sym,
			float64(r.OpenRatioByMoney), float64(r.OpenRatioByVolume),
			float64(r.CloseRatioByMoney), float64(r.CloseRatioByVolume),
			float64(r.CloseTodayRatioByMoney), float64(r.CloseTodayRatioByVolume))

		// ⚠️ 三档是不是同一组数 —— #8 的后半格（平今档仍未测）就挂在这里。
		if !sameTier(r) {
			logf("      ⚠️ **三档不同**：#8 的「开仓档 = 平昨档 = 平今档」在这个合约上不成立")
		}
		if d := rateDigits(float64(r.OpenRatioByMoney)); d >= 4 {
			interesting = append(interesting, fmt.Sprintf("%s（按额费率 %g，%d 位有效小数）",
				sym, float64(r.OpenRatioByMoney), d))
		}
	}
	logf("")
	if len(interesting) == 0 {
		logf("⇒ ⚠️ 这一批里**没有**一个合约的按额费率比 1e-4 更细 ——")
		logf("   #5 的两个残余候选（不取整 / 取到三位或更细）在它们身上仍然同值。")
		logf("   ⚠️ 这是一条**有信息的空结果**：它说的是「再多测这些合约也分不开」，")
		logf("   不是「没测出来」。要接着走得换一批合约，或者换一个柜台。")
	} else {
		logf("⇒ 这些合约的按额费率够细，**可能**把 #5 的两个候选分开：")
		for _, s := range interesting {
			logf("     · %s", s)
		}
		logf("   ⚠️ 「够细」只是必要条件：还要 名义金额 × 费率 真的落在三位以外，")
		logf("   而那取决于当时的价格与乘数 —— 拿 `ctp-fee` 去那个合约上实测才算数。")
	}
	return nil
}

// sameTier 报告开仓 / 平昨 / 平今三档是不是同一组数。
//
// ⚠️ 六个数全比，不是只比按额那两个：#8 实测到的「三档同费率」是**两项都同**，
// 而只比一半的判据在「按额同、每手不同」的柜台上会说「一样」。
func sameTier(r *def.CThostFtdcInstrumentCommissionRateField) bool {
	return float64(r.OpenRatioByMoney) == float64(r.CloseRatioByMoney) &&
		float64(r.OpenRatioByMoney) == float64(r.CloseTodayRatioByMoney) &&
		float64(r.OpenRatioByVolume) == float64(r.CloseRatioByVolume) &&
		float64(r.OpenRatioByVolume) == float64(r.CloseTodayRatioByVolume)
}

// rateDigits 报一个费率有几位**有效小数**（末尾的零不算）。
//
// ⚠️ 它答的是「这个费率能不能把 #5 的两个候选分开」的必要条件那一半：
// 1e-4 给 4，而 rb 的 `价 × 10 × 1e-4` 抵掉一位、只剩三位 ——
// **所以 4 是门槛而不是保证**，函数名里不写「够细」正是为此。
// 返回 0 表示费率为零或读不出来。
func rateDigits(v float64) int {
	if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	s := fmt.Sprintf("%.12f", v)
	s = strings.TrimRight(s, "0")
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return 0
	}
	return len(s) - i - 1
}
