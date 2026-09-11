package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/probe"
)

// runCTPCancel 撤掉账上**所有还挂着**的委托。
//
// ⚠️ 它补的是一整类缺失的动作：此前这个工具包**能下单、却不能清理自己下出去的单**。
//
// 20260911 夜盘的现场：我用 `timeout 150` 包着 `go run ctp-fee`，150 秒不够，
// 外部 kill 落在「下单之后、撤单之前」⇒ 两笔买开挂单留在账上，
// 而撤单需要 `OrderRef`，**ref 只活在下单的那个进程里** ——
//
//	⚠️ **一个只能由制造者清理的残留，在制造者死掉时就清理不了了。**
//
// ⇒ 于是只能等 GFD 在收盘时自动撤，而在那之前冻结额一直占着、
// 且**任何读账户级冻结量的测量都被污染**（当晚就因此报了一个错结论）。
//
// ⚠️ 本命令不接受 `-symbol`：残留的特征恰恰是**你不知道它是哪一笔**。
// 要撤某一笔而不是全部，那是另一个需求，别把它塞进这里。
func runCTPCancel(args []string) error {
	fs := flag.NewFlagSet("ctp-cancel", flag.ExitOnError)
	envPath := fs.String("env", ".env", "凭据文件路径")
	timeout := fs.Duration("timeout", 40*time.Second, "每一步的超时")
	dry := fs.Bool("dry", false, "只列出，不撤")
	if err := fs.Parse(args[2:]); err != nil {
		return err
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
	live, err := c.LiveOrders(*timeout)
	if err != nil {
		return err
	}
	if len(live) == 0 {
		logf("[cancel] 没有挂着的委托")
		return nil
	}
	logf("[cancel] 挂着 %d 笔：", len(live))
	failed := 0
	for _, o := range live {
		ex, inst := ctp.Text(o.ExchangeID[:]), ctp.Text(o.InstrumentID[:])
		ref := ctp.Text(o.OrderRef[:])
		logf("[cancel]   %s.%s ref=%s dir=%q off=%q %d 手 @%.2f",
			ex, inst, ref, string(o.Direction), string(o.CombOffsetFlag[0]),
			int(o.VolumeTotalOriginal), float64(o.LimitPrice))
		if *dry {
			continue
		}
		// ⚠️ Cancel 需要 (ref, req)，而 req 里真正被用到的是交易所与合约 ——
		// 其余字段撤单用不上，但**不能乱填**：将来若实现改成按这些字段定位，
		// 一个乱填的值会让撤单静默撤错单。
		// ⚠️ 按**交易所编号**撤，不按本地 ref —— 残留的特征就是「不是这个会话下的」，
		// 而按 ref 撤需要当前会话的 FrontID/SessionID，对它们无效且**不报错**。
		if err := c.CancelByOrder(o); err != nil {
			failed++
			logf("[cancel]   ⚠️ 撤单失败：%v", err)
		}
	}
	if *dry {
		logf("[cancel] （-dry：一笔都没撤）")
		return nil
	}
	if failed > 0 {
		return fmt.Errorf("⚠️ %d/%d 笔撤单失败 —— **那几笔还挂着**", failed, len(live))
	}
	// ⚠️ 撤单是异步的：发出去不等于撤掉了。回查一次才算数。
	time.Sleep(2 * time.Second)
	after, err := c.LiveOrders(*timeout)
	if err != nil {
		return err
	}
	if len(after) > 0 {
		return fmt.Errorf("⚠️ 撤单都发出去了，但回查还有 %d 笔挂着 —— "+
			"**发出去不等于撤掉了**，去看柜台回报", len(after))
	}
	logf("[cancel] ⇒ ✅ 全部撤掉，回查 0 笔")
	return nil
}
