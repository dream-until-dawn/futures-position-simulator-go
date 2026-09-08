package probe

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// expPositionFrozen 测**持仓侧**的挂单冻结：挂一笔平仓单，看哪个 `volume_*_frozen_*` 动。
//
// ⚠️ 它与已有的 `frozen` 实验测的**不是一回事**：
//
//	frozen           账户侧 —— FrozenMargin / FrozenCommission 怎么进 Available
//	position-frozen  持仓侧 —— volume_*_frozen_today / _his 冻的是哪一边
//
// 前者早已实测；后者至今**几乎零观测**：54 个持仓字段里
// `volume_long_frozen_today` 与三个 `volume_short_frozen_*` 一次都没取到过非零值。
// 而一个从未非零过的字段，对拍时两边都是 0，判「一致」什么都不说明。
//
// # 判据
//
// 挂一笔**挂不上的**限价平仓单（FarPrice），看：
//
//	哪个字段变了      冻的是 _today 还是 _his？合计字段跟着动不动？
//	撤单之后释放没有  ⚠️ 不释放的话，那些手数就永远平不掉了
//
// ⚠️ 用挂不上的价：被接受的那一支不成交，不吃种子 —— 种子是别的实验的输入。
//
// # ⚠️ 为什么是「按顺序试」而不是「挑一个」
//
// 想打中 `_today` 就得平今仓，而**平今仓未必能用 CLOSETODAY**：
// NoUseHistory 的合约（DCE/CZCE）只认 CLOSE。第一版按今昨挑 Offset，
// 于是在唯一有今仓的那个合约（大商所）上必然拒单 ——
// 目标字段反而永远够不着。
//
// 但「大商所会拒 CLOSETODAY」这句话本身也只是**别处记的一条规则**。
// 与其照它挑，不如**照它排序、逐个试**：拒单是柜台的答复，
// 顺带就是一条 PositionDateType 的证据；试到被接受为止。
func (r *Runner) expPositionFrozen(ctx context.Context) error {
	if len(r.Symbols) != 1 {
		return fmt.Errorf("本实验需要 -symbols 指定**恰好一个**合约 —— "+
			"多个合约会让「哪个字段动了」与「是哪个合约动的」混在一起，得到 %d 个",
			len(r.Symbols))
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
		return fmt.Errorf("%s 行情未就绪，定不出挂不上的限价", sym)
	}

	r.Logf("")
	r.Logf("== 持仓侧挂单冻结：挂一笔平仓单，看哪个 volume_*_frozen_* 动 ==")
	r.Logf("  ⚠️ 与 frozen 实验不同：那个量账户侧的 FrozenMargin，这个量持仓侧的手数")

	p := cli.PositionOf(sym)
	if p == nil {
		return fmt.Errorf("读不到 %s 的持仓截面", sym)
	}
	cands, err := closableCandidates(p)
	if err != nil {
		return err
	}
	r.Logf("  %s 可平的组合按「优先补零观测字段」排好序，共 %d 个：", sym, len(cands))
	for i, c := range cands {
		r.Logf("    %d. %s %s（今 %.0f / 昨 %.0f）—— 想打中 %s",
			i+1, c.side, c.offset, c.today, c.his, c.target())
	}

	measured := false
	for i, pick := range cands {
		r.Logf("")
		r.Logf("  —— 第 %d 个：%s 方向发 %s 1 手 ——", i+1, pick.side, pick.offset)

		before := frozenSnapshot(cli, sym)
		r.Logf("    挂单前：%s", fmtFrozen(before))

		ex, inst := splitSymbol(sym)
		id, err := cli.InsertOrder(r.guard(), kq.OrderReq{
			Exchange: ex, Instrument: inst,
			Direction: pick.orderDir(), Offset: pick.offset, Volume: 1,
			LimitPrice: q.FarPrice(pick.orderDir()),
		})
		if err != nil {
			// ⚠️ 本地安全阀拦下**不是**柜台的答复：读成「柜台拒绝」会得出一个
			// 关于柜台的错误结论。这一条直接中止，不往下试。
			return fmt.Errorf("⚠️ **本地安全阀拦下，没发出去**（不是柜台的答复）：%w", err)
		}

		during, why := waitFrozen(cli, sym, id, before, 15*time.Second)
		r.Logf("    挂单后：%s", fmtFrozen(during))

		switch why {
		case orderFinished:
			st, _ := cli.OrderOf(id)
			if st.VolumeLeft == 0 {
				// ⚠️ 用挂不上的价却成交了：种子被吃掉，**立刻停**，
				// 别再往下试第二笔。
				return fmt.Errorf("⚠️⚠️ **委托成交了**（限价 %.4f 本应挂不上）—— "+
					"种子被吃掉 1 手，**就此停下**，后面的实验要重新核对前提",
					q.FarPrice(pick.orderDir()))
			}
			r.Logf("    柜台**拒单**：last_msg=%q", st.LastMsg)
			r.Logf("    ⚠️ 对「持仓冻结」什么都没测到 —— 拒单不冻结是显然的。")
			r.Logf("       但它是一条 PositionDateType 的证据：该合约不收 %s。", pick.offset)
			continue

		case timedOut:
			r.Logf("    ⚠️ 15 秒内委托还挂着，但**没有任何冻结字段变化**。")
			r.Logf("       那本身是一条结论：这个 Offset 下柜台不在持仓侧记冻结手数。")
			if err := cli.CancelOrder(id); err != nil {
				r.Logf("    ⚠️ 撤单失败：%v —— **去看一眼那笔委托**", err)
				return fmt.Errorf("撤单失败，就此停下，别把委托留在盘上：%w", err)
			}
			continue

		case frozenChanged:
			measured = true
			for _, k := range frozenFields {
				if before[k] != during[k] {
					r.Logf("    ✓ %s：%s → %s", k, before[k], during[k])
				}
			}
			if err := cli.CancelOrder(id); err != nil {
				r.Logf("    ⚠️ 撤单失败：%v —— **去看一眼那笔委托**，它可能还挂着", err)
				return fmt.Errorf("撤单失败，就此停下：%w", err)
			}
			after, why2 := waitFrozen(cli, sym, id, during, 15*time.Second)
			r.Logf("    撤单后：%s", fmtFrozen(after))
			switch {
			case why2 == timedOut:
				r.Logf("    ⚠️⚠️ 撤单之后冻结**没有释放**（15 秒内无变化）——")
				r.Logf("       那意味着那些手数从此平不掉。**这条要单独查**。")
			case !sameFrozen(after, before):
				r.Logf("    ⚠️ 撤单后与挂单前**不一致**：%s vs %s —— 释放得不完整",
					fmtFrozen(after), fmtFrozen(before))
			default:
				r.Logf("    ✓ 撤单完整释放，回到挂单前的状态")
			}
		}
	}

	// ⚠️ 一个都没测到时说清楚，别让一份「跑完了」的日志被当成有结论。
	if !measured {
		r.Logf("")
		r.Logf("  ⚠️ %d 个组合**全都没能测到冻结**（拒单或不冻结）——", len(cands))
		r.Logf("     本次对 volume_*_frozen_* 没有新增任何观测。")
	}
	return r.dump("position-frozen", fmt.Sprintf(
		"持仓侧挂单冻结：%s 上按顺序试了 %d 个「方向×开平」组合（挂不上的限价，随即撤单），"+
			"测到冻结：%t。⚠️ 与账户侧的 frozen 实验不是一回事：那个量 FrozenMargin，"+
			"这个量 volume_*_frozen_*", sym, len(cands), measured))
}

// closable 是一个「平哪一边、用哪个开平标志」的组合。
type closable struct {
	dir        kq.Direction // 持仓方向
	side       string       // "long" / "short"
	offset     kq.Offset
	today, his float64
}

// orderDir 是平仓委托的方向 —— 与持仓方向**相反**。
//
// ⚠️ 单独成一个方法而不是在调用处取反：方向搞反会在双向持仓上
// 开出反向仓位（或平掉另一边），且柜台不会报错。
func (c closable) orderDir() kq.Direction {
	if c.dir == kq.Buy {
		return kq.Sell
	}
	return kq.Buy
}

// target 是这个组合**想打中**的字段，用来给候选排序、也用来在日志里说明意图。
//
// ⚠️ CLOSE 打中哪个是**未知**的 —— 今昨都有时由柜台决定消耗顺序
// （那正是 close-order 实验的问题）。这里照实说「未知」，不猜。
func (c closable) target() string {
	base := "volume_" + c.side + "_frozen"
	switch {
	case c.offset == kq.CloseToday:
		return base + "_today"
	case c.today > 0 && c.his > 0:
		return base + "_today 或 _his（CLOSE 消耗哪边由柜台定）"
	case c.today > 0:
		return base + "_today"
	default:
		return base + "_his"
	}
}

// zeroObserved 是至今**一次都没取到过非零值**的那几个字段。
//
// ⚠️ 这张表是排序的依据，不是断言。它会过期 —— 一旦某个字段被观测到，
// 它就该从这里挪走，否则实验会一直去补一份已经有了的样本。
var zeroObserved = map[string]bool{
	"volume_long_frozen_today":  true,
	"volume_short_frozen":       true,
	"volume_short_frozen_today": true,
	"volume_short_frozen_his":   true,
}

// closableCandidates 列出全部「方向×开平」组合，**按判别力排序**。
//
// 排序只有一条规则：先试**能打中零观测字段**的那些。
// 一个从未非零过的字段，对拍时两边都是 0，判「一致」什么都不说明 ——
// 补上它才是这次实验的价值所在。
//
// ⚠️ 今仓给出**两个**候选（CLOSETODAY 与 CLOSE）而不是一个：
// NoUseHistory 的合约只认 CLOSE，按规则表挑会在那些合约上必然拒单，
// 目标字段反而永远够不着。排序照规则，能不能用交给柜台答。
func closableCandidates(p map[string]any) ([]closable, error) {
	var cands []closable
	for _, c := range []struct {
		dir  kq.Direction
		side string
	}{{kq.Buy, "long"}, {kq.Sell, "short"}} {
		today, okT := kq.Num(p, "volume_"+c.side+"_today")
		his, okH := kq.Num(p, "volume_"+c.side+"_his")
		if !okT || !okH {
			// ⚠️ 读不到不当成零：那会让判据在一个未知的前提上跑。
			continue
		}
		if today > 0 {
			cands = append(cands,
				closable{c.dir, c.side, kq.CloseToday, today, his},
				closable{c.dir, c.side, kq.Close, today, his})
		} else if his > 0 {
			// ⚠️ 只有昨仓时才单独给 CLOSE：今仓存在时上面那对已经含了它，
			// 重复一次会让同一笔单被发两遍。
			cands = append(cands, closable{c.dir, c.side, kq.Close, today, his})
		}
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf(
			"⚠️ 没有任何方向有可平的量 —— 本实验要一笔**平仓**单才冻结持仓，" +
				"空仓上挂开仓单冻的是保证金（那是 frozen 实验的事）")
	}
	// ⚠️ 稳定排序：只把「能打中零观测字段」的提前，其余保持原序。
	// 用 sort.Slice 会让同优先级的次序不确定，而次序决定先发哪一笔单。
	var first, rest []closable
	for _, c := range cands {
		if hitsZeroObserved(c) {
			first = append(first, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(first, rest...), nil
}

// hitsZeroObserved 判断这个组合**可能**打中一个零观测字段。
//
// ⚠️ 是「可能」不是「一定」：CLOSE 在今昨都有时消耗哪边由柜台定。
// 排序上宁可把它算成可能命中 —— 排错顺序只是多发一笔挂不上的单，
// 漏掉一次补样的机会才是真损失。
func hitsZeroObserved(c closable) bool {
	base := "volume_" + c.side + "_frozen"
	if c.offset == kq.CloseToday {
		return zeroObserved[base+"_today"]
	}
	if c.today > 0 && zeroObserved[base+"_today"] {
		return true
	}
	return c.his > 0 && zeroObserved[base+"_his"]
}

// frozenFields 是持仓侧的六个冻结字段。
var frozenFields = []string{
	"volume_long_frozen", "volume_long_frozen_today", "volume_long_frozen_his",
	"volume_short_frozen", "volume_short_frozen_today", "volume_short_frozen_his",
}

func frozenSnapshot(cli *kq.Client, sym string) map[string]string {
	p := cli.PositionOf(sym)
	out := map[string]string{}
	for _, k := range frozenFields {
		out[k] = numOrDash(p, k)
	}
	return out
}

func sameFrozen(a, b map[string]string) bool {
	for _, k := range frozenFields {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}

// fmtFrozen 只打非零的那几个 —— 六个字段里五个是零时，
// 把零一起打出来会把真正动了的那个淹掉。
func fmtFrozen(m map[string]string) string {
	var b strings.Builder
	for _, k := range frozenFields { // 固定顺序，两次输出可直接肉眼对比
		v := m[k]
		if v == "0.0000" {
			continue
		}
		fmt.Fprintf(&b, "%s=%s ", k, v)
	}
	if b.Len() == 0 {
		return "（六个字段全为零）"
	}
	return b.String()
}

// waitReason 说明 waitFrozen 为什么停下来。
//
// ⚠️ 三态不能合并成布尔：「委托被拒所以没冻结」与「委托挂着但柜台不冻结」
// 是完全不同的两条结论，而后者才是本实验想要的。
type waitReason int

const (
	timedOut waitReason = iota
	frozenChanged
	orderFinished
)

// waitFrozen 等冻结字段**相对 base 发生变化**，或委托走到终态，或超时。
//
// ⚠️ 等变化而不是睡固定秒数：睡够了就往下走，
// 会在慢的那一次读到「还没冻」并把它记成「不冻结」——
// 而「不冻结」是个会被当真的结论。
func waitFrozen(cli *kq.Client, sym, orderID string, base map[string]string,
	timeout time.Duration) (map[string]string, waitReason) {

	deadline := time.Now().Add(timeout)
	cur := base
	for time.Now().Before(deadline) {
		cur = frozenSnapshot(cli, sym)
		if !sameFrozen(cur, base) {
			return cur, frozenChanged
		}
		if st, found := cli.OrderOf(orderID); found &&
			strings.EqualFold(st.Status, "FINISHED") {
			return cur, orderFinished
		}
		cli.WaitTrade(500 * time.Millisecond)
	}
	return cur, timedOut
}
