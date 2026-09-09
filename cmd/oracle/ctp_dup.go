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
	Seed  int64 // 拨到这个序号，于是 ref = p<Seed>
	PxOff int   // 价格 = 跌停 + PxOff × step
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
	base := fs.Int64("base", 0, "起始序号；0 表示按当前时刻取一个当日没用过的号")
	plan := fs.String("plan", "natural", "natural=**完全不碰 ref**，由客户端自己续号（最贴近真实用法）；"+
		"serial=手工指定全新递增 ref；elem=重用 ref 的原五格")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if *symbol == "" {
		return fmt.Errorf("⚠️ -symbol 没有默认值：一笔真实委托的合约必须显式指定")
	}
	// ⚠️ 起始序号要**避开当日已经用过的号**，否则第 1 格（基线）本身就被污染，
	// 而基线一旦不成立，后面四格全部读不出东西。今天用过的是 p000000001/2 这种小号，
	// 所以从当日秒数起跳：它远大于那些手写小号，且每次跑都不同。
	seed := *base
	if seed == 0 {
		n := time.Now()
		seed = int64(n.Hour()*3600+n.Minute()*60+n.Second()) * 10
	}

	// ⚠️ elem 计划跑过一轮（20260910 21:4x），**它的对照组自己红了**：
	// 第 3 格新 ref、新价、递增，本该接受，却同样回 ErrorID=22。
	// 于是 H_dup / H_mono / H_elem **一个都解释不了**，问题必须先收窄成
	// 「是不是从第二笔起就一律被拒」—— 那是 serial 计划要回答的，
	// 所以它是默认值。⚠️ 保留 elem 是因为它的原始记录已经进了文档，
	// 删掉计划等于让那条记录无法复跑。
	cells := []dupCell{
		{"1 基线", seed, 1, "接受（否则整轮作废）"},
		{"2 新 ref·新价·递增", seed + 1, 2, "接受；若拒 ⇒ **第二笔就被拒**，与 ref/要素都无关"},
		{"3 新 ref·新价·递增", seed + 2, 3, "接受"},
		{"4 新 ref·新价·递增", seed + 3, 4, "接受"},
		{"5 新 ref·新价·递增", seed + 4, 5, "接受"},
	}
	if *plan == "elem" {
		cells = []dupCell{
			{"1 基线", seed, 1, "接受（否则后四格没有对照，整轮作废）"},
			{"2 同要素·新 ref·递增", seed + 1, 1, "若拒 ⇒ H_elem 成立"},
			{"3 新要素·新 ref·递增", seed + 2, 2, "接受（排除「第 N 笔就是会被拒」）"},
			{"4 重复 ref·非递增·新要素", seed, 3, "若拒 ⇒ 与要素无关（H_dup 或 H_mono）"},
			{"5 更小但没用过的 ref·新要素", seed - 1, 4, "拒 ⇒ H_mono；受 ⇒ H_mono 被否"},
		}
	} else if *plan == "natural" {
		// ⚠️ natural 不碰 ref。它测的是**真实用法**：连上、连发五笔。
		// 20260910 的两轮实验都是「手工指定 ref」，而手工指定这个动作本身
		// 会盖掉登录时从 MaxOrderRef 续的号 —— **一个为了控制变量而引入的变量**。
		for i := range cells {
			cells[i].Name = fmt.Sprintf("%d 自然续号·新价", i+1)
			cells[i].Want = "接受"
		}
		cells[0].Want = "接受（否则整轮作废）"
	} else if *plan != "serial" {
		return fmt.Errorf("⚠️ -plan 只认 natural / serial / elem，拿到 %q —— "+
			"不给默认回退：跑错计划的结果长得和跑对了一模一样", *plan)
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
	logf("[dup] 起始序号 %d（当日已用过的是个位数小号，刻意避开）", seed)

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
		if *plan != "natural" {
			c.SeedOrderSeq(cl.Seed)
		}
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
		// ⚠️ serial 计划**刻意不在中途撤**：它要回答的是「后一笔会不会被拒」，
		// 而中途撤单会引入「撤单本身是不是原因」这一条无关的可能。
		// elem 计划必须撤 —— 它要重用 ref，不撤的话第 4 格会撞上一笔活单。
		if st.Alive() && *plan == "elem" {
			if err := c.Cancel(st.OrderRef, req); err != nil {
				return err
			}
			time.Sleep(2 * time.Second)
		}
		alive = append(alive, req)
		aliveRef = append(aliveRef, st.OrderRef)
	}
	if *plan != "elem" {
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
