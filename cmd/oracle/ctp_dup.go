// ⚠️ 本文件单独成文件，不塞进 main.go：main.go 已经装着六个子命令，
// 而这一个是**判别实验**不是日常工具，它的读者是来看实验设计的。

package main

import (
	"flag"
	"fmt"
	"time"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

// dupCell 是实验的一格：一笔单，加上它想回答什么。
type dupCell struct {
	Name  string
	PxOff int // 价格 = 跌停 + PxOff × step
	Want  string
}

// runCTPDup 判别 `ErrorID=22 不允许重复报单` 到底在查什么。
//
// # 为什么要做这个实验
//
// 20260910 夜盘两次平仓被回 ErrorID=22，**两次都留下了敞口**。
// 我先后给过两个原因，⚠️ **两个都是猜的，而且互相矛盾**：
//
//	一次说「ref 回绕了、不递增」            —— 那次 ref 确实变小了
//	一次说「ref 每进程重置、撞上今天用过的」 —— 而随后 p000000001 被重复接受，**当场否掉**
//
// ⚠️ **一个错的理由比没有理由更糟**：它让人以为问题解决了，
// 于是下一次照踩，而且不会再去查。
//
// # 三个候选，以及它们为什么不能一起测
//
//	H_dup    ref 在当日内不可重复
//	H_mono   ref 必须严格递增
//	H_elem   报单要素（合约/方向/开平/量/价）完全相同即算重复
//
// ⚠️ 「把 ref 拨回一个用过的小号」**同时**触发 H_dup 与 H_mono ——
// 那样测出来的红分不出是哪一个。所以第 5 格专门用一个
// **更小、但从未用过**的 ref：它只触发 H_mono。
//
// # 事前写下预期
//
// 每一格的 Want 是**发单之前**写死在代码里的。⚠️ 事后看着结果说「果然如此」
// 是零成本的，而这个模块栽过一次：第 44 条拒绝优先级得出过相反的结论
// 并真的改了代码。**先承诺，再观测**，不一致的那一格才有声音。
//
// # 为什么这些单不会成交
//
// 全部是**买开**、挂在**跌停价往上几个 tick**，而市价在几百点之上 ——
// 买单挂得比市价低就只能挂着。每格看完立刻撤。
// ⚠️ 挂单不产生手续费，所以整轮跑完账户应当回到起点。
func runCTPDup(args []string) error {
	fs := flag.NewFlagSet("ctp-dup", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	symbol := fs.String("symbol", "", "合约，形如 SHFE.rb2701（⚠️ 无默认值）")
	step := fs.Float64("step", 1, "相邻两格的价差，⚠️ **必须是该合约最小变动价位的整数倍**，"+
		"否则会撞上「不是整数倍」那条拒因，而那与本实验无关")
	// ⚠️ 曾有 -plan serial / elem 两个计划，靠 `SeedOrderSeq` 手工指定报单引用。
	// **两者连同那个旋钮一起删了**（20260910 评审）：它们唯一的用处是复跑
	// §6.8 里那两轮历史记录，而那两轮跑的是一个**已经修好的缺陷** ——
	// 修好之后本来就复跑不出当时的结果。
	// ⚠️ 为了复跑一件复跑不出来的事，留一个能重新引入 ref 冲突的旋钮，不划算。
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：一笔真实委托的合约必须显式指定")
	}
	cells := []dupCell{
		{"第 1 笔", 1, "接受（否则整轮作废）"},
		{"第 2 笔", 2, "接受"},
		{"第 3 笔", 3, "接受"},
		{"第 4 笔", 4, "接受"},
		{"第 5 笔", 5, "接受"},
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
	c.Valve = ctpValve(env, nil)
	defer c.Close()
	if err := c.Connect(*timeout); err != nil {
		return err
	}

	before, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	md, err := c.MarketData(*symbol, *timeout)
	if err != nil {
		return err
	}
	floor := float64(md.LowerLimitPrice)
	logf("[dup] 行情 最新=%.2f 跌停=%.2f ⇒ 五格挂在 %.2f–%.2f，全部买开、全部挂得住",
		float64(md.LastPrice), floor, floor+*step, floor+4**step)
	logf("[dup] ⚠️ 报单引用**不由本实验指定** —— 由客户端从登录应答的 MaxOrderRef 续号，")
	logf("       这正是被测的那条路径：手工指定引用等于绕开它，那样测的是别的东西。")

	ex, inst := ctp.SplitSymbol(*symbol)
	type row struct {
		cell     dupCell
		px       float64
		ref, msg string
		alive    bool
	}
	var rows []row
	var alive []ctp.OrderReq
	var aliveRef []string
	for _, cl := range cells {
		req := ctp.OrderReq{
			Exchange: ex, Instrument: inst,
			Direction: def.THOST_FTDC_D_Buy, Offset: def.THOST_FTDC_OF_Open,
			Volume: 1, LimitPrice: floor + float64(cl.PxOff)**step,
		}
		st, err := c.Insert(req, *timeout)
		if err != nil {
			// ⚠️ 超时是**没有结论**，不是「被拒」——不能当成一格结果记下来。
			return fmt.Errorf("第「%s」格没有结论：%w —— "+
				"⚠️ 整轮作废，缺一格就分不开候选；先去柜台查有没有挂着的单", cl.Name, err)
		}
		rows = append(rows, row{cl, req.LimitPrice, st.OrderRef, st.StatusMsg, st.Alive()})
		logf("[dup] %-28s ref=%s @%.2f → status=%q alive=%v %s",
			cl.Name, st.OrderRef, req.LimitPrice, string(st.Status), st.Alive(), st.StatusMsg)
		// ⚠️ **刻意不在中途撤**：本实验要回答的是「后一笔会不会被拒」，
		// 而中途撤单会引入「撤单本身是不是原因」这一条无关的可能。
		alive = append(alive, req)
		aliveRef = append(aliveRef, st.OrderRef)
	}
	{
		for i, r := range alive {
			if s, ok := c.Order(aliveRef[i]); ok && s.Alive() {
				if err := c.Cancel(aliveRef[i], r); err != nil {
					return err
				}
			}
		}
		time.Sleep(3 * time.Second)
	}

	logf("")
	logf("%-28s %-12s %-8s %-6s %s", "格", "ref", "价", "结果", "事前预期")
	for _, r := range rows {
		got := "拒"
		if r.alive {
			got = "受"
		}
		logf("%-28s %-12s %-8.2f %-6s %s", r.cell.Name, r.ref, r.px, got, r.cell.Want)
	}

	after, err := c.Account(*timeout)
	if err != nil {
		return err
	}
	logf("")
	logf("[dup] 账户 %.4f → %.4f 差 %+.4f；冻结保证金 %.4f",
		float64(before.Balance), float64(after.Balance),
		float64(after.Balance)-float64(before.Balance), float64(after.FrozenMargin))
	if float64(after.FrozenMargin) != 0 {
		// ⚠️ 冻结没归零 = 还有单挂着。这不是「实验失败」，是**要人去收拾**，
		// 两者要做的事不同，所以要说出来而不是安静结束。
		return fmt.Errorf("⚠️ 冻结保证金没有归零（%.4f）—— **还有单挂在柜台上**，去撤",
			float64(after.FrozenMargin))
	}
	return nil
}
