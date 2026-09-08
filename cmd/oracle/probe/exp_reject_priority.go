package probe

import (
	"context"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
	"github.com/dream-until-dawn/futures-position-simulator-go/conformance/fixture"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
)

// violation 是一种可以单独构造、也可以与别的叠加的违规。
//
// Check 写的是本库 order 包里那一项的名字 —— 两边对得上才有意义。
type violation struct {
	Key   string // 简称，日志与归因用
	Check string // 对应 order.Check 的取值名
	// Rank 是**本库声称的**优先级，数字小的先报。
	//
	// ⚠️ 直接取 order 包的枚举，不手抄数字：手抄的那份改了库不会红，
	// 而这条实验的全部意义就是拿库的说法去和柜台对。
	Rank  order.Check

	// Abs 说明这一项**整个改写价格**（而不是在现价上加一个零头）。
	//
	// ⚠️ 这个字段是一次真实事故的产物。20260909 第一版里
	// 「越涨停」写成 r.LimitPrice = q.UpperLimit*1.05，它**覆盖**掉了
	// 「不是整数倍」刚加上去的零头 —— 于是「组合单」与「单违规单」
	// 变成了同一笔单，归因等于**拿一笔单和它自己比**。
	// 而实验照样跑完、照样打印出一行「与本库相反」的结论。
	// 现在改写价格的先应用，加零头的后应用，且下面有 Holds 当场核对。
	Abs bool

	Apply func(q kq.Quote, tick float64, r *kq.OrderReq)

	// Holds 回答「这笔单**真的**违反了这一项吗」。
	//
	// ⚠️ 它是这条实验的**零层**：一个没有真正构造出来的违规，
	// 与一个构造出来的违规，在日志里长得一模一样。
	Holds func(q kq.Quote, tick float64, r kq.OrderReq) bool
}

// violations 是四种能在**空仓合约**上安全构造的违规。
//
// ⚠️ 「安全」的判据是：这笔单**不可能成交**。三种平仓类违规靠的是
// 合约上一手都没有（柜台在本地就拒了，压根到不了交易所）；价格类违规
// 靠的是报在不可成交的一侧。手数一律 1，且仍然过 Guard。
//
// ⚠️ 手数上下限、限仓、资金不足**不在这里**，理由与 rejectCases 那份一样
// （前两类要 max/min_limit_order_volume，免费行情不下发；后一类要把账户打到
// 接近爆仓）。列出来是为了说明它们是被有意排除的，不是被忘掉的。
// ⚠️ 次序有意义：Abs 的排在前面，应用时也按本切片的次序，
// 于是「加零头」永远落在「改写价格」之后。
var violations = []violation{
	{Key: "limit", Check: "CheckPriceLimit", Rank: order.CheckPriceLimit, Abs: true,
		// ⚠️ 越界之后**仍然对齐 tick**：涨跌停价本身是 tick 的整数倍，
		// 加整数个 tick 还是整数倍。第一版写的是 UpperLimit*1.05，
		// 那个数（19514×1.05 = 20489.7）**不是**整数倍 ——
		// 于是「只越涨停」这一条其实同时违反了两项，标尺从一开始就是脏的。
		Apply: func(q kq.Quote, tick float64, r *kq.OrderReq) {
			if r.Direction == kq.Buy {
				r.LimitPrice = q.UpperLimit + 10*tick
			} else {
				r.LimitPrice = q.LowerLimit - 10*tick
			}
		},
		Holds: func(q kq.Quote, _ float64, r kq.OrderReq) bool {
			return r.LimitPrice > q.UpperLimit || r.LimitPrice < q.LowerLimit
		}},
	{Key: "tick", Check: "CheckPriceTick", Rank: order.CheckPriceTick,
		Apply: func(_ kq.Quote, tick float64, r *kq.OrderReq) { r.LimitPrice += tick / 3 },
		Holds: func(_ kq.Quote, tick float64, r kq.OrderReq) bool { return offTick(r.LimitPrice, tick) }},
	{Key: "close", Check: "CheckClosable", Rank: order.CheckClosable,
		Apply: func(_ kq.Quote, _ float64, r *kq.OrderReq) { r.Offset = kq.Close },
		Holds: func(_ kq.Quote, _ float64, r kq.OrderReq) bool { return r.Offset == kq.Close }},
	{Key: "closetoday", Check: "CheckClosable", Rank: order.CheckClosable,
		Apply: func(_ kq.Quote, _ float64, r *kq.OrderReq) { r.Offset = kq.CloseToday },
		Holds: func(_ kq.Quote, _ float64, r kq.OrderReq) bool { return r.Offset == kq.CloseToday }},
}

// offTick 判断价格是不是**不**为最小变动价位的整数倍。
//
// ⚠️ 容差取 tick 的千分之一而不是一个绝对常数：tick 从 0.5（DCE.i）
// 到 10（SHFE.cu）都有，一个绝对容差在两端各错一次，而错的方向是
// 「以为构造出了违规」—— 那正是这条实验最怕的方向。
func offTick(price, tick float64) bool {
	if tick <= 0 {
		return false
	}
	n := price / tick
	return math.Abs(n-math.Round(n)) > 1e-3
}

// applyAll 按 violations 的次序叠加若干项违规，然后**逐项核对它们真的成立**。
//
// ⚠️ 核对不是多余的：叠加的两项可能互相覆盖（第一版就是），
// 而覆盖之后的单子照样发得出去、照样被拒、照样打印一行结论。
func applyAll(q kq.Quote, tick float64, base kq.OrderReq, keys map[string]bool) (kq.OrderReq, error) {
	req := base
	for _, v := range violations {
		if keys[v.Key] {
			v.Apply(q, tick, &req)
		}
	}
	for _, v := range violations {
		if got := v.Holds(q, tick, req); got != keys[v.Key] {
			want := "不该成立"
			if keys[v.Key] {
				want = "应当成立"
			}
			return req, fmt.Errorf("⚠️ 构造出来的单子对不上：%s %s，实际 %v（%s）—— "+
				"**违规没被真正构造出来**，下面无论柜台答什么都不说明任何事",
				v.Key, want, got, req)
		}
	}
	return req, nil
}

// priorityPairs 是要测的组合。每一对**同时**违反两项，
// 柜台只会回一个拒因 —— 回的是哪一个，就是这个口子的优先级。
//
// ⚠️ 不测 close × closetoday：它们是同一项检查（CheckClosable）的两种形态，
// 组合起来问不出优先级。列出来说明它是被排除的。
var priorityPairs = [][2]string{
	{"tick", "limit"},
	{"tick", "close"},
	{"tick", "closetoday"},
	{"limit", "close"},
	{"limit", "closetoday"},
}

// expRejectPriority 量的是**拒绝优先级**：一笔单同时违反两项时，柜台报哪一个。
//
// # 为什么这条必须实测
//
// order 包的 Check 取值顺序**就是**拒绝优先级，注释里写着「照 §9，别重排」——
// 而 §9 是文档，不是观测。柜台只回一个 last_msg：顺序错了，两边都判「拒绝」，
// ⚠️ **差异不会以失败的形式出现**。这正是本仓库反复栽的那一类。
//
// # 归因不靠关键字匹配
//
// 同一次运行里先把**每一种单违规**各发一笔，记下柜台原话；
// 再发组合单，把组合的原话与两条单违规的原话逐字比。
//
//	等于 A 的原话  ⇒ A 赢
//	等于 B 的原话  ⇒ B 赢
//	两条都不等于   ⇒ 第三种东西，照抄报出来，不硬塞进两选一
//
// ⚠️ 关键字匹配（比如找「涨跌停」三个字）会把「归因」变成「我猜柜台怎么措辞」，
// 而措辞一改，一条错误的归因会**继续绿着**。逐字比对没有这个问题。
//
// # 两处盲区，代码里当场检出
//
//	两种单违规的原话**一模一样** ⇒ 这一对归因不成立
//	某种单违规柜台**根本不拒**   ⇒ 这一对归因不成立，且这本身是条要记的事实
func (r *Runner) expRejectPriority(ctx context.Context) error {
	if len(r.Symbols) == 0 {
		return fmt.Errorf("reject-priority 需要 -symbols 指定一个合约")
	}
	sym := r.Symbols[0]
	cli := r.cli

	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(sym); err != nil {
		return err
	}
	q, ok := cli.WaitQuoteReady(sym, 30*time.Second)
	if !ok {
		return fmt.Errorf("%s 行情未就绪，拿不到涨跌停与最小变动价位，无法构造违规", sym)
	}
	tick, err := tickFromSpecs(r.Specs, sym)
	if err != nil {
		return err
	}
	// ⚠️ 只要求**这个合约**空仓，不要求整个账户空仓：
	// 别的合约上的隔夜仓是后续实验的种子，不该为这条实验清掉。
	if n := symbolLots(cli, sym); n > 0 {
		return fmt.Errorf("⚠️ %s 上已有 %.0f 手持仓 —— "+
			"平仓类违规要靠「一手都没有」才必然被拒，有仓就可能真的成交", sym, n)
	}
	ex, inst := splitSymbol(sym)

	r.Logf("")
	r.Logf("== 拒绝优先级：一笔单同时违反两项，柜台报哪一个 ==")
	r.Logf("  合约 %s  涨停=%.2f 跌停=%.2f 最新=%.2f tick=%v",
		sym, q.UpperLimit, q.LowerLimit, q.LastPrice, tick)

	byKey := map[string]violation{}
	for _, v := range violations {
		byKey[v.Key] = v
	}

	// 基准单：BUY/OPEN 报在跌停价 —— 买在地板上，不会成交。
	// 叠加违规之后它必然被拒；万一两项违规柜台都不认，
	// 这个价格也让它成不了交（而收尾仍然会撤单并核对持仓）。
	base := func() kq.OrderReq {
		return kq.OrderReq{Exchange: ex, Instrument: inst, Direction: kq.Buy,
			Offset: kq.Open, Volume: 1, LimitPrice: q.LowerLimit}
	}

	send := func(label string, req kq.OrderReq) (status, msg string) {
		id, err := cli.InsertOrder(r.guard(), req)
		if err != nil {
			return "本地拦截", err.Error()
		}
		st, done := cli.WaitOrderFinished(id, 15*time.Second)
		if !done {
			if st.Status == "ALIVE" {
				_ = cli.CancelOrder(id)
			}
			return "未在 15s 内到终态(status=" + st.Status + ")", st.LastMsg
		}
		return st.Status, st.LastMsg
	}

	// 第一轮：每种违规各发一笔，取柜台原话作为**归因的标尺**。
	single := map[string]string{}
	for _, v := range violations {
		req, err := applyAll(q, tick, base(), map[string]bool{v.Key: true})
		if err != nil {
			return err
		}
		status, msg := send(v.Key, req)
		single[v.Key] = msg
		r.Logf("")
		r.Logf("  单违规 %-11s %s  status=%s", v.Key, v.Check, status)
		r.Logf("      柜台：%s", msg)
		if msg == "" {
			r.Logf("      ⚠️ 原话是空的 —— 用它做标尺，任何组合都会「等于」它")
		}
	}

	// 第二轮：组合单。
	r.Logf("")
	r.Logf("  —— 组合 ——")
	agree, disagree, unusable := 0, 0, 0
	for _, pair := range priorityPairs {
		a, b := byKey[pair[0]], byKey[pair[1]]
		req, err := applyAll(q, tick, base(), map[string]bool{a.Key: true, b.Key: true})
		if err != nil {
			return err
		}
		status, msg := send(a.Key+"+"+b.Key, req)

		// 本库声称谁赢：Rank 小的。
		winner, loser := a, b
		if b.Rank < a.Rank {
			winner, loser = b, a
		}
		r.Logf("")
		r.Logf("  %s + %s  status=%s", a.Key, b.Key, status)
		r.Logf("      柜台：%s", msg)
		r.Logf("      本库声称 %s（%s）优先于 %s（%s）",
			winner.Key, winner.Check, loser.Key, loser.Check)

		switch {
		case single[a.Key] == single[b.Key]:
			unusable++
			r.Logf("      ⚠️ 归因不成立：两种单违规的柜台原话**一模一样**，" +
				"组合等于哪一条都分不出来")
		case msg == "" || (single[a.Key] == "" && single[b.Key] == ""):
			unusable++
			r.Logf("      ⚠️ 归因不成立：原话为空")
		case msg == single[winner.Key]:
			agree++
			r.Logf("      ✅ 与本库一致：柜台报的是 %s", winner.Key)
		case msg == single[loser.Key]:
			disagree++
			r.Logf("      ❌ 与本库**相反**：柜台报的是 %s —— "+
				"order.Check 的取值顺序在这个口子上是错的", loser.Key)
		default:
			unusable++
			r.Logf("      ⚠️ 第三种原话：既不等于 %s 也不等于 %s 的单违规原话。"+
				"照抄在上面，不硬塞进两选一", a.Key, b.Key)
		}
	}

	r.Logf("")
	r.Logf("  合计：一致 %d，相反 %d，归因不成立 %d（共 %d 对）",
		agree, disagree, unusable, len(priorityPairs))
	// ⚠️ 「全部归因不成立」要当成失败报出来，不是「跑完了」。
	// 一次什么都没问出来的实验，与一次全部一致的实验，
	// 在汇总行里长得一模一样 —— 除非这里分开说。
	if agree+disagree == 0 {
		r.Logf("  ⚠️ **一对都没问出来** —— 这次运行对优先级没有任何判别力")
	}

	// 收尾：撤掉一切还活着的委托，并核对这个合约仍然空仓。
	alive := 0
	for id, v := range cli.Orders() {
		if m, ok := v.(map[string]any); ok {
			if s, _ := m["status"].(string); s == "ALIVE" {
				_ = cli.CancelOrder(id)
				alive++
			}
		}
	}
	if alive > 0 {
		r.Logf("  收尾撤单 %d 条", alive)
		cli.WaitTrade(5 * time.Second)
	}
	if n := symbolLots(cli, sym); n > 0 {
		return fmt.Errorf("⚠️ 结束时 %s 上出现了 %.0f 手持仓 —— "+
			"有反例意外成交了，请人工检查", sym, n)
	}

	return r.dump("exp-reject-priority",
		"拒绝优先级：同时违反两项时柜台报哪一个（归因靠同一次运行的单违规原话逐字比）")
}

// symbolLots 数**某一个合约**上的持仓手数（多空今昨相加）。
//
// ⚠️ 不能用「截面里有没有这条记录」判断：只要对某合约下过单，
// 记录就会出现，哪怕手数全为零（见 kq.OpenLots 的注释）。
func symbolLots(cli *kq.Client, symbol string) float64 {
	p := cli.PositionOf(symbol)
	var n float64
	for _, k := range []string{"volume_long_today", "volume_long_his",
		"volume_short_today", "volume_short_his"} {
		n += kq.MustNum(p, k)
	}
	return n
}

// tickFromSpecs 从规格文件里取最小变动价位。
//
// ⚠️ **不回落到行情里的 price_tick**：免费行情不下发它，恒为 0，
// 而用 0 构造出来的「违规价」是任何 tick 的整数倍 —— 那一项**根本没被违反**，
// 实验却照样跑完、照样打印一行结果。一个没有违反的「违规」与一个真的违规，
// 在日志里长得一模一样。
func tickFromSpecs(path, symbol string) (float64, error) {
	if path == "" {
		return 0, fmt.Errorf("需要 -specs 指定合约规格文件（refdata-sync -specs 的产物）—— " +
			"⚠️ 最小变动价位没有默认值：免费行情不下发它")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("打开规格文件 %s：%w", path, err)
	}
	defer f.Close()
	specs, err := fixture.LoadSpecs(f)
	if err != nil {
		return 0, fmt.Errorf("读规格文件 %s：%w", path, err)
	}
	sp, ok := specs[symbol]
	if !ok {
		return 0, fmt.Errorf("规格文件 %s 里没有 %s", path, symbol)
	}
	t, _ := sp.PriceTick.Float64()
	if t <= 0 {
		return 0, fmt.Errorf("%s 的最小变动价位是 %s，必须为正", symbol, sp.PriceTick)
	}
	return t, nil
}
