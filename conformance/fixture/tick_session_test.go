package fixture

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// tickMsg / limitMsg 是柜台那两句原话。
//
// ⚠️ 归因**不认关键字**，认整句相等 —— 与 kq_facts 44 同一条规矩：
// 「包含『整倍数』三个字」这种判据，会把一句新出现的、含同样字眼的
// 别的拒因悄悄归到这一档里。
const (
	tickMsg  = "下单价格不是价格单位的整倍数"
	limitMsg = "已撤单报单被拒绝价格超出涨停板"
)

// TestCounterHalfTickBoundaryHoldsInSession 把半个 tick 那条边界
// **在交易时段内**逐笔钉住，并且**双向**钉。
//
// # 它取代了什么
//
// 上一版是 `TestTickScanInSessionCoverage`：钉住「(0.34, 0.69) 这段零头
// 盘中一个观测都没有」，是一条会在采样成功那天变红的绊线。
// ⚠️ 2026-09-09 09:02 它红了 —— **那次采样成功了**，于是这里换成
// 一条强得多的断言：不再是「那段空着」，是「边界就在 0.5，而且盘中如此」。
//
//	盘中实扫（SHFE.ag2702，tick=1，越涨停 +10 tick，只改零头）：
//
//	    0.400  不是整数倍        0.500  涨停板
//	    0.450  不是整数倍        0.550  涨停板
//	                             0.600  涨停板
//
//	⇒ 边界**恰好**在 0.5，且 0.5 本身落在**接受**侧（报涨停，说明
//	  整数倍这一项没被触发）—— 与盘后扫出来的完全一致。
//
// # 断言的形状：与 offTick **双向**一致
//
// 柜台先查整数倍、后查涨跌停（kq_facts 44）。于是对任何一笔拿到这两句
// 原话之一的委托：
//
//	回「不是整数倍」  ⟺  offTick(价, tick) 为真
//	回「涨停板」      ⟺  offTick(价, tick) 为假（整数倍那一项没拦住它）
//
// ⚠️ **双向**是关键。只钉一个方向的话，一个「永远说不是整数倍」的
// offTick 也能通过其中一半 —— 而 20260909 那次把 order 的两项优先级
// 对调的翻案，根子就是拿本库的判据去猜柜台会怎么判。
// 这条断言让那两份判据**在盘中样本上逐笔对齐**。
func TestCounterHalfTickBoundaryHoldsInSession(t *testing.T) {
	tables := loadSessionsForTest(t)
	specs := loadSpecsForTest(t)
	cn := refdata.CNZone()

	checked, nearBelow, nearAbove := 0, 0, 0
	// outOfSession 是**被时段过滤挡掉**的笔数。
	//
	// ⚠️ 它不是统计，是这条断言里「盘中」两个字的**唯一**守卫：
	// 柜台的行为盘中盘后一样（kq_facts 48：它根本不查时段），
	// 所以过滤失效时盘后那批照样与 offTick 一致，断言**照样全绿** ——
	// 而这条测试声称量的是**盘中**。破坏 192 演示过这一点。
	outOfSession := 0
	for _, f := range loadAll(t) {
		if !strings.Contains(f.Path, "reject-tick-vs-limit") {
			continue
		}
		for _, o := range f.Orders {
			msg := ""
			if v, ok := o["last_msg"]; ok && v.IsText {
				msg = v.Text
			}
			if msg != tickMsg && msg != limitMsg {
				continue // 被接受的、或别的拒因 —— 这条断言不管
			}
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
				outOfSession++
				continue
			}

			pxF, _ := px.Float64()
			tickF, _ := spec.PriceTick.Float64()
			q := px.Div(spec.PriceTick)
			frac, _ := q.Sub(q.Floor()).Float64()

			off := offTickAgrees(pxF, tickF)
			checked++
			switch {
			case msg == tickMsg && !off:
				t.Errorf("⚠️ %s %s @%s（零头 %.4f，%s）柜台回「不是整数倍」，"+
					"而本库的 offTick 说它**是**整数倍 —— 两份判据分岔了。"+
					"⚠️ 别急着改 offTick：先确认这一笔的 tick 取对了",
					f.Path, sym, px, frac, at.Format("15:04"))
			case msg == limitMsg && off:
				t.Errorf("⚠️ %s %s @%s（零头 %.4f，%s）柜台回「涨停板」——"+
					"说明整数倍那一项**没拦住它**（kq_facts 44：先查整数倍），"+
					"而本库的 offTick 说它偏离整数倍。两份判据分岔了",
					f.Path, sym, px, frac, at.Format("15:04"))
			}
			if frac >= 0.44 && frac < 0.5 {
				nearBelow++
			}
			if frac >= 0.5 && frac <= 0.56 {
				nearAbove++
			}
		}
	}

	if checked < 10 {
		t.Fatalf("⚠️ 只逐笔核了 %d 笔盘中委托 —— 本条在空转", checked)
	}
	if outOfSession == 0 {
		t.Fatalf("⚠️ 一笔都没有被时段过滤挡掉（核了 %d 笔）—— "+
			"语料里明明有大量**盘后**的委托，说明时段判定失效了。"+
			"⚠️ 而它失效时这条断言仍然全绿：柜台不查时段（kq_facts 48），"+
			"盘后那批与 offTick 同样一致 —— 于是「盘中」两个字会**悄悄变成一句空话**", checked)
	}
	// ⚠️ 两侧**贴着边界**的样本缺一不可：只有 0.1 与 0.9 的话，
	// 「边界在 0.5」与「边界在 (0.1, 0.9) 里任何一处」给出同一个答案。
	if nearBelow == 0 || nearAbove == 0 {
		t.Fatalf("⚠️ 贴着 0.5 的盘中样本：下侧 %d 笔、上侧 %d 笔 —— "+
			"缺一侧就定不住**边界的位置**，只能说明「零头这一维起作用」",
			nearBelow, nearAbove)
	}
	t.Logf("盘中逐笔核了 %d 笔（另有 %d 笔盘后的被挡掉），与 offTick 双向一致；"+
		"贴着 0.5 的样本下侧 %d 笔、上侧 %d 笔",
		checked, outOfSession, nearBelow, nearAbove)
}

// offTickAgrees 是 cmd/oracle 那边 offTick 的**同一份判据**。
//
// ⚠️ 它必须与 cmd/oracle/probe 的 offTick 保持同一条规则，而两处**是两份代码**
// —— 主模块与嵌套模块之间不能互相 import（嵌套模块依赖主模块，反过来不行）。
// 这是一处**已知的重复**，写出来而不是假装没有：
// 破坏 213 演示两边分岔时这里不会有任何动静。
func offTickAgrees(price, tick float64) bool {
	if tick <= 0 {
		return false
	}
	frac := price/tick - math.Floor(price/tick)
	return frac > 1e-3 && frac < 0.5
}
