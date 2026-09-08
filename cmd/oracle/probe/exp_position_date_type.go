package probe

import (
	"context"
	"fmt"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expPositionDateType 测**这个合约区不区分今昨仓**。
//
// ⚠️ 它是 `refdata` 快照最后一块拿不到的规则数据（`PositionDateType`），
// 而它**只能靠柜台的行为来测** —— 字典里没有，日行情里也没有。
//
// # 判据
//
// 在一个**只有昨仓、没有今仓**的合约上发一笔平今：
//
//	被拒   柜台区分今昨 —— 没有今仓可平，所以拒
//	被受   ⚠️ 要么不区分（平今被当成普通平仓），要么它接受了一笔平不出来的单
//
// 再发一笔**裸平**（`CLOSE`，不指定今昨）：
//
//	被受   柜台接受裸平，由它自己决定消耗哪一边 —— 那就落到「消耗顺序」问题上
//	被拒   柜台要求显式指定今昨
//
// # ⚠️ 两条设计上的讲究
//
// **一、用挂不上的限价。** 被接受的那一支若用对手价，会当场成交并吃掉种子，
// 而种子是后面几个实验的输入。用 `FarPrice` 挂着，
// **知道它被接受了**就够了，随即撤掉 —— 被接受的情形因此不花任何代价。
//
// **二、前置条件必须先查。** 今仓不为零时，平今被接受什么都不说明 ——
// 那时它本来就该被接受。⚠️ 这一条是本实验最容易空转的地方：
// 一个在错误前提下跑出来的「接受」，与真正的「不区分今昨」长得一模一样。
func (r *Runner) expPositionDateType(ctx context.Context) error {
	if len(r.Symbols) == 0 {
		return fmt.Errorf("本实验需要 -symbols 指定合约（可多个，逐个测）")
	}
	cli := r.cli
	if err := cli.ConnectQuote(ctx); err != nil {
		return err
	}
	if err := cli.SubscribeQuotes(r.Symbols...); err != nil {
		return err
	}

	r.Logf("")
	r.Logf("== PositionDateType：这个合约区不区分今昨仓 ==")
	r.Logf("  ⚠️ 用挂不上的限价，被接受的那一支不成交、随即撤掉 —— 不吃种子")

	tested := 0
	for _, sym := range r.Symbols {
		q, ok := cli.WaitQuoteReady(sym, 30*time.Second)
		if !ok {
			r.Logf("  %s 行情未就绪，跳过", sym)
			continue
		}
		p := cli.PositionOf(sym)
		if p == nil {
			r.Logf("  %s 没有持仓截面，跳过", sym)
			continue
		}
		r.Logf("")
		r.Logf("  —— %s ——", sym)

		// 找一个「只有昨仓、没有今仓」的方向。
		dir, side, ok := historyOnlySide(p)
		if !ok {
			// ⚠️ 明说前提不成立，而不是跳过了事：
			// 「没跑」与「跑了没结论」在报告里必须分得开。
			r.Logf("    ⚠️ **判据不成立**：没有哪个方向是「只有昨仓、没有今仓」。")
			r.Logf("       多今%.0f/多昨%.0f 空今%.0f/空昨%.0f",
				kq.MustNum(p, "volume_long_today"), kq.MustNum(p, "volume_long_his"),
				kq.MustNum(p, "volume_short_today"), kq.MustNum(p, "volume_short_his"))
			r.Logf("       今仓不为零时，平今被接受什么都不说明 —— 那时它本来就该被接受。")
			continue
		}
		r.Logf("    前提成立：%s 方向只有昨仓 %.0f 手、今仓 0 手", side, kq.MustNum(p, "volume_"+side+"_his"))

		// 平仓的下单方向与持仓方向相反。
		orderDir := kq.Buy
		if dir == kq.Buy {
			orderDir = kq.Sell
		}
		ex, inst := splitSymbol(sym)
		far := q.FarPrice(orderDir)

		for _, step := range []struct {
			offset kq.Offset
			what   string
			accept string
			reject string
		}{
			{kq.CloseToday, "平今",
				"⚠️ 柜台**接受**了平今 —— 要么它不区分今昨，要么它接受了一笔平不出来的单",
				"柜台**拒绝**平今 —— 它区分今昨（没有今仓可平）"},
			{kq.Close, "裸平",
				"柜台**接受**裸平 —— 由它自己决定消耗哪一边，那就落到「消耗顺序」问题上",
				"柜台**拒绝**裸平 —— 它要求显式指定今昨"},
		} {
			req := kq.OrderReq{
				Exchange: ex, Instrument: inst,
				Direction: orderDir, Offset: step.offset, Volume: 1,
				LimitPrice: far,
			}
			id, err := cli.InsertOrder(r.guard(), req)
			if err != nil {
				// ⚠️ 本地安全阀拦下**不是**柜台的答复，两者必须分开报：
				// 把它读成「柜台拒绝」，会得出一个关于柜台的错误结论。
				r.Logf("    %s：⚠️ **本地安全阀拦下，没发出去**（不是柜台的答复）：%v",
					step.what, err)
				continue
			}
			st, done := cli.WaitOrderFinished(id, 15*time.Second)
			switch {
			case !done:
				// 还挂着 = 被接受了。撤掉。
				r.Logf("    %s：%s", step.what, step.accept)
				r.Logf("       （委托 %s 挂着未成交，随即撤单）", id)
				if err := cli.CancelOrder(id); err != nil {
					r.Logf("       ⚠️ 撤单失败：%v —— **去看一眼那笔委托**", err)
				}
			case st.VolumeLeft == 0:
				// ⚠️ 用挂不上的价却成交了：那说明 FarPrice 不是挂不上的价，
				// 或者行情动得比预期快。这笔成交**吃掉了种子**，必须显眼地报。
				r.Logf("    %s：⚠️⚠️ **委托成交了**（限价 %.4f 本应挂不上）—— "+
					"种子被吃掉 1 手，后面的实验要重新核对前提", step.what, far)
			default:
				r.Logf("    %s：%s", step.what, step.reject)
				r.Logf("       （status=%q last_msg=%q）", st.Status, st.LastMsg)
			}
		}
		tested++
	}

	// ⚠️ 一个合约都没测到时**报错**：那时本实验什么都没说，
	// 而一份「跑完了」的日志会被当成有结论。
	if tested == 0 {
		return fmt.Errorf("⚠️ 一个合约都没能测 —— 要么行情没就绪，"+
			"要么没有「只有昨仓」的方向。**本实验此时什么结论都没有**（试了 %d 个合约）",
			len(r.Symbols))
	}
	return r.dump("position-date-type", fmt.Sprintf(
		"PositionDateType 判别：在只有昨仓的方向上发平今与裸平（挂不上的限价，随即撤单），"+
			"测了 %d 个合约。⚠️ 被拒的文案是柜台的答复，被本地安全阀拦下的不是",
		tested))
}

// historyOnlySide 找一个「只有昨仓、没有今仓」的方向。
//
// ⚠️ 两个条件都要查。只查「有昨仓」的话，一个今昨都有的方向也会被选中，
// 而在那种方向上平今被接受**什么都不说明**。
func historyOnlySide(p map[string]any) (kq.Direction, string, bool) {
	for _, c := range []struct {
		dir  kq.Direction
		side string
	}{{kq.Buy, "long"}, {kq.Sell, "short"}} {
		today, okT := kq.Num(p, "volume_"+c.side+"_today")
		his, okH := kq.Num(p, "volume_"+c.side+"_his")
		// ⚠️ 读不到就**不选**，不当成零：读不到与真的是零在这里后果不同 ——
		// 前者会让判据在一个未知的前提上跑。
		if !okT || !okH {
			continue
		}
		if today == 0 && his > 0 {
			return c.dir, c.side, true
		}
	}
	return kq.Buy, "", false
}
