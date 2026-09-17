package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/shopspring/decimal"
)

// settleLeg 是一条跨过结算的持仓记录（按交易日 D 收盘后的截面）。
type settleLeg struct {
	Symbol       string
	Long         bool
	Volume       int64
	Multiplier   decimal.Decimal
	PositionCost decimal.Decimal // 盯市基线 × 手数 × 乘数：今仓是开仓成本、昨仓是昨结算价
	Settle       decimal.Decimal // 交易日 D 的结算价 = 交易日 D+1 行情里的 PreSettlementPrice
}

// settleInput 是 §13 #5 那条判法的全部输入。
type settleInput struct {
	PreBalance, Deposit, Withdraw, CloseProfit, Commission decimal.Decimal // 交易日 D 收盘后
	Legs                                                   []settleLeg
	NextPreBalance                                         decimal.Decimal // 交易日 D+1 的上日结存
}

// settleResidual 按事前登记的判法算「不重新取整时的推算」与残差。纯函数。
//
//	推算 = PreBalance(D) + 入金 − 出金 + CloseProfit(D) + Σ过夜腿按结算价的盯市盈亏 − Commission(D)
//	残差 = PreBalance(D+1) − 推算
//
// ⚠️ 盯市盈亏用 PositionCost 当基线（今仓是开仓成本、昨仓是昨结算价 × 手数 × 乘数），不用 OpenCost ——
// 跨过两次结算的昨仓，OpenCost 算出来的是累计浮盈，会把上一次结算已经兑现过的那部分再算一遍。
func settleResidual(in settleInput) (predicted, residual decimal.Decimal) {
	predicted = in.PreBalance.Add(in.Deposit).Sub(in.Withdraw).Add(in.CloseProfit).Sub(in.Commission)
	for _, l := range in.Legs {
		mv := l.Settle.Mul(decimal.NewFromInt(l.Volume)).Mul(l.Multiplier)
		if l.Long {
			predicted = predicted.Add(mv.Sub(l.PositionCost))
		} else {
			predicted = predicted.Add(l.PositionCost.Sub(mv))
		}
	}
	return predicted, in.NextPreBalance.Sub(predicted)
}

// settleVerdict 把残差与各候选的预言逐个比，返回**相等**的候选。纯函数。
//
// ⚠️ 判「相等」不判「接近」：两边都先四舍五入到 6 位（与登记里去尾巴的规则一致）再比。
// 一个都不等 ⇒ 返回空，调用方判 (ii) —— 不往最近的那个上凑（登记里写死的）。
func settleVerdict(residual decimal.Decimal, preds map[string]decimal.Decimal) []string {
	r := residual.Round(6)
	var out []string
	for _, name := range settleRoundingCandidates {
		if p, ok := preds[name]; ok && p.Round(6).Equal(r) {
			out = append(out, name)
		}
	}
	return out
}

// settleInputFrom 从两份 CTP 截面拼出 settleInput。纯函数（除了读已经解析好的截面）。
//
// ⚠️ 拒绝一切「补一个数」：过夜腿缺乘数、次日行情里缺结算价、账户缺字段，一律报错 ——
// 补进来的数不属于这两份证据，而残差判的正是分以下的差。
func settleInputFrom(end, next *ctp.Fixture, mult map[string]decimal.Decimal) (settleInput, error) {
	var in settleInput
	acc := func(f *ctp.Fixture, k string) (decimal.Decimal, error) {
		v, ok := f.Account[k].(float64)
		if !ok {
			return decimal.Zero, fmt.Errorf("⚠️ 截面 %s 的账户里没有数值字段 %s", f.CapturedAt, k)
		}
		// ⚠️ 柜台字段是 double（读到 19997481.341499999999995 这类）：按登记里去尾巴的规则先四舍五入到 6 位。
		return decimal.NewFromFloat(v).Round(6), nil
	}
	var err error
	for k, dst := range map[string]*decimal.Decimal{
		"PreBalance": &in.PreBalance, "Deposit": &in.Deposit, "Withdraw": &in.Withdraw,
		"CloseProfit": &in.CloseProfit, "Commission": &in.Commission,
	} {
		if *dst, err = acc(end, k); err != nil {
			return in, err
		}
	}
	if in.NextPreBalance, err = acc(next, "PreBalance"); err != nil {
		return in, err
	}
	if end.TradingDay == next.TradingDay {
		return in, fmt.Errorf("⚠️ 两份截面是同一个交易日 %s —— 次日的 PreBalance 要从下一个交易日读", end.TradingDay)
	}
	keys := make([]string, 0, len(end.Positions))
	for k := range end.Positions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p := end.Positions[key]
		vol, _ := p["Position"].(float64)
		if vol == 0 {
			continue
		}
		sym := key
		if i := strings.IndexByte(key, '/'); i >= 0 {
			sym = key[:i]
		}
		m, ok := mult[sym]
		if !ok {
			return in, fmt.Errorf("⚠️ 过夜腿 %s 没有给乘数（-mult %s=…）—— 不从截面里反解", sym, sym)
		}
		q, ok := next.Quotes[sym]
		if !ok {
			return in, fmt.Errorf("⚠️ 次日截面里没有 %s 的行情 —— 结算价补不上，不判", sym)
		}
		s, ok := q["PreSettlementPrice"].(float64)
		if !ok || s <= 0 {
			return in, fmt.Errorf("⚠️ 次日 %s 行情里的 PreSettlementPrice 缺或非正（%v）", sym, q["PreSettlementPrice"])
		}
		cost, _ := p["PositionCost"].(float64)
		dir, _ := p["PosiDirection"].(string)
		if dir != "2" && dir != "3" {
			return in, fmt.Errorf("⚠️ %s 的 PosiDirection 是 %q —— 认不得的方向不猜", key, dir)
		}
		in.Legs = append(in.Legs, settleLeg{Symbol: sym, Long: dir == "2", Volume: int64(vol),
			Multiplier: m, PositionCost: decimal.NewFromFloat(cost).Round(6), Settle: decimal.NewFromFloat(s).Round(6)})
	}
	return in, nil
}

// runSettleResidual 是离线命令：读交易日 D 收盘后与 D+1 的两份截面，按事前登记的判法算残差、判候选。**不连柜台。**
func runSettleResidual(args []string) error {
	fs := flag.NewFlagSet("settle-residual", flag.ExitOnError)
	endPath := fs.String("end", "", "交易日 D 收盘后的 CTP 截面（⚠️ 必填）")
	nextPath := fs.String("next", "", "交易日 D+1 的 CTP 截面，要带过夜腿的行情（⚠️ 必填）")
	multArg := fs.String("mult", "", "过夜腿的合约乘数，形如 CZCE.MA701=10（⚠️ 必填；不从截面反解）")
	feesArg := fs.String("fees", "", "交易日 D 的全部逐笔手续费，逗号分隔（⚠️ 必填；合计要等于账户 Commission）")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *endPath == "" || *nextPath == "" || *feesArg == "" {
		return fmt.Errorf("⚠️ -end / -next / -fees 都没有默认值")
	}
	load := func(p string) (*ctp.Fixture, error) {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var f ctp.Fixture
		return &f, json.Unmarshal(b, &f)
	}
	end, err := load(*endPath)
	if err != nil {
		return err
	}
	next, err := load(*nextPath)
	if err != nil {
		return err
	}
	mult := map[string]decimal.Decimal{}
	for _, kv := range strings.Split(*multArg, ",") {
		if kv = strings.TrimSpace(kv); kv == "" {
			continue
		}
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("⚠️ -mult 写法不对：%q", kv)
		}
		m, err := decimal.NewFromString(parts[1])
		if err != nil {
			return err
		}
		mult[parts[0]] = m
	}
	var fees []decimal.Decimal
	for _, s := range strings.Split(*feesArg, ",") {
		f, err := decimal.NewFromString(strings.TrimSpace(s))
		if err != nil {
			return err
		}
		fees = append(fees, f.Round(6))
	}
	in, err := settleInputFrom(end, next, mult)
	if err != nil {
		return err
	}
	total := decimal.Zero
	for _, f := range fees {
		total = total.Add(f)
	}
	if !total.Equal(in.Commission.Round(6)) {
		return fmt.Errorf("⚠️ -fees 合计 %s 不等于交易日 D 账户 Commission %s —— 逐笔费不全或多了，残差归不了因", total, in.Commission.Round(6))
	}
	preds := settleRoundingResiduals(fees)
	predicted, residual := settleResidual(in)
	fmt.Printf("交易日 %s → %s\n", end.TradingDay, next.TradingDay)
	fmt.Printf("推算（不重新取整）%s，实际 PreBalance %s ⇒ 残差 %s\n", predicted, in.NextPreBalance, residual.Round(6))
	for _, n := range settleRoundingCandidates {
		fmt.Printf("  (%s) 预言 %s\n", n, preds[n])
	}
	if hit := settleVerdict(residual, preds); len(hit) == 0 {
		fmt.Println("⇒ 五个候选**一个都不等** —— 判 (ii)，不往最近的凑（事前登记写死的）")
	} else {
		fmt.Printf("⇒ 命中：%v\n", hit)
	}
	return nil
}
