package fixture

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// halfTickGapLo / halfTickGapHi 圈出**盘中样本至今没有碰过**的那段零头。
//
// ⚠️ 这两个数不是「可以接受的空档」，是「现在就这么空着」。
const (
	halfTickGapLo = 0.34
	halfTickGapHi = 0.69
)

// TestTickScanInSessionCoverage 核 roadmap 那句「盘中只有一个点」。
//
// # 它当场推翻了那句话
//
// 原文写的是「盘中只有一个点与该模型相符」。按时段表逐笔判下来**不是**：
// `SHFE.ag2702` 在它自己的夜盘时段内（21:00–02:30，实测落在 02:09）
// 有两个**不同零头**的点，而且它们分别落在半个 tick 的两侧 ——
//
//	零头 1/3  →  柜台报「不是价格单位的整倍数」
//	零头 0.7  →  柜台报「价格超出涨停」
//
// ⚠️ 差别是实质性的：一个点只能说「盘中没有反例」，
// **两个跨过边界的点**说的是「盘中这条路径的走向与盘后一致」。
//
// # 但边界的**位置**仍然只有盘后样本
//
// 1/3 与 0.7 之间是空的。半个 tick 这个具体位置，
// 在 (0.34, 0.69) 这一整段上盘中一个观测都没有 ——
// 任何落在这段里的边界都同样解释得了现有的盘中样本。
//
// > 所以 09:00 那次重跑要问的不是「盘中是不是也这样」，
// > 是**把零头扫到 0.5 附近**：0.4 / 0.45 / 0.5 / 0.55 / 0.6。
//
// ⚠️ 这条会在那次重跑成功的当天变红，那是它该做的：
// 红了说明空档被填上了，去更新 kq_facts 45 并把这两个常量收窄或删掉。
func TestTickScanInSessionCoverage(t *testing.T) {
	tables := loadSessionsForTest(t)
	specs := loadSpecsForTest(t)
	cn := refdata.CNZone()

	type point struct {
		frac float64
		msg  string
		sym  string
		at   string
	}
	var in []point
	seen := map[string]bool{}
	for _, f := range loadAll(t) {
		if !strings.Contains(f.Path, "reject-tick-vs-limit") {
			continue
		}
		for _, o := range f.Orders {
			sym, ok := textOf(o, "exchange_id", "instrument_id")
			if !ok {
				continue
			}
			ns, ok := numberOf(o, "insert_date_time")
			if !ok || !ns.IsPositive() {
				continue
			}
			px, ok := numberOf(o, "limit_price")
			if !ok {
				continue
			}
			spec, ok := specs[sym]
			if !ok || spec.PriceTick.IsZero() {
				continue
			}
			inst, err := types.ParseSymbol(sym, f.TradingDay)
			if err != nil {
				continue
			}
			product, _ := splitProduct(inst.Product)
			st, ok := tables[string(inst.Exchange)+"."+product]
			if !ok {
				continue
			}
			at := time.Unix(0, ns.IntPart()).In(cn)
			clock, err := refdata.NewClockTime(at.Hour(), at.Minute(), at.Second())
			if err != nil {
				continue
			}
			live := false
			for _, ss := range append(append([]refdata.Session{}, st.Day...), st.Night...) {
				if ss.Contains(clock) {
					live = true
					break
				}
			}
			if !live {
				continue
			}
			q := px.Div(spec.PriceTick)
			frac, _ := q.Sub(q.Floor()).Float64()
			if frac == 0 {
				continue // 整数倍，与这条边界无关
			}
			msg := ""
			if v, ok := o["last_msg"]; ok && v.IsText {
				msg = v.Text
			}
			key := sym + px.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			in = append(in, point{frac, msg, sym, at.Format("15:04")})
		}
	}

	if len(in) == 0 {
		t.Fatal("⚠️ 一个**盘中的、带零头的**样本都没有 —— " +
			"那么「盘中这条路径与盘后一致」目前是零观测，不是「只有一个点」")
	}
	sort.Slice(in, func(i, j int) bool { return in[i].frac < in[j].frac })
	for _, p := range in {
		t.Logf("  盘中 %s %s 零头 %.3f  柜台：%s", p.sym, p.at, p.frac, p.msg)
	}

	var gap []string
	for _, p := range in {
		if p.frac > halfTickGapLo && p.frac < halfTickGapHi {
			gap = append(gap, p.sym+" "+p.at)
		}
	}
	if len(gap) > 0 {
		t.Fatalf("⚠️ (%.2f, %.2f) 这段零头**现在有盘中样本了**：%v —— "+
			"这条红了不是坏消息，是 09:00 那次重跑问到了东西。"+
			"该做的是把观测写进 kq_facts 45，然后收窄或删掉 halfTickGapLo/Hi",
			halfTickGapLo, halfTickGapHi, gap)
	}
	t.Logf("盘中带零头的样本 %d 个，(%.2f, %.2f) 这段仍然是空的 —— "+
		"半个 tick 这个**位置**至今只有盘后样本支持",
		len(in), halfTickGapLo, halfTickGapHi)
}
