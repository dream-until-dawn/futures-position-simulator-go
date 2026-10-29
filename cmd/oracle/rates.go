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
	return reportCTPRates(c, *symbols, *timeout, logf)
}

// investorRange 把这条费率记录的**适用范围**翻成人话。
//
// # ⚠️ 它补的是本批里最实在的一个洞
//
// 20260911 夜盘读到 `SHFE.rb2701` 声明 `每手 0`，而行为是 `每手 0.005`，
// 我把它写成「声明不完整」（§13 第 19 条）。
//
//	⚠️ **而那句话有一个我当时没排除的替代解释**：
//	CTP 的费率记录带 `InvestorRange`（所有 / 投资者组 / 单一投资者），
//	我读到的可能**不是实际适用的那一条** —— 那就不是「声明不完整」，
//	是「我读错了记录」。
//
// ⚠️ 而当时它既没被打印、也没落盘 ⇒ **那一轮的原始数据里根本没有能排除它的信息**。
// 一个说不清自己适用于谁的费率，与一个适用范围不对的费率，在输出上长得一模一样。
//
// ⇒ 现在每一行都带上它。⚠️ **只打范围，不打 `InvestorID`** ——
// 那是凭据一类的值，落进日志或夹具都算泄漏（同 `kq.Scrubbed` 那条纪律）。
// 只报「带没带投资者代码」这一个比特，够用来判记录的粒度。
func investorRange(r *def.CThostFtdcInstrumentCommissionRateField) string {
	name := map[byte]string{
		'1': "所有",
		'2': "投资者组",
		'3': "单一投资者",
	}[byte(r.InvestorRange)]
	if name == "" {
		// ⚠️ 认不得就原样报出**字节值**，不留空 ——
		// 空白与「柜台给了一个我没见过的取值」在这一列上分不开。
		name = fmt.Sprintf("未知取值 %q", string(rune(r.InvestorRange)))
	}
	who := "无投资者代码"
	if ctp.Text(r.InvestorID[:]) != "" {
		who = "带投资者代码"
	}
	return name + "/" + who
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
