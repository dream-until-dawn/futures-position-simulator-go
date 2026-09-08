package fixture

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestReconstructCoversCarriedSides 把**带昨仓的方向**接回对拍。
//
// 此前那些方向在重放对拍与保证金对拍里是**跳过**的（各 7 处），
// 理由是重放只回放当日成交、凑不出昨仓。跳过诚实，但它让出去的
// 恰好是 20260909 夜盘新出现、也最值得对的那批截面：**今昨并存**。
//
// 本条用 `Reconstruct`（前一交易日结转 + 当日成交重放）把它们接回来，
// 比四个字段：**总手数**、**今昨划分**（两个）与**开仓均价**。
//
// # ⚠️ 为什么只比这四个
//
//	总手数      结转与重放合起来对不对，这个字段最直接
//	今昨划分    ⚠️ 它才是「结转真的发生了」的证据 —— 总手数对得上而划分错了，
//	            在纯今仓或纯昨仓的样本上完全看不出来
//	开仓均价    ⚠️ 它是**逐笔对冲**的基线，穿过结算**不变**（kq_facts 26 实测）——
//	            所以本库与柜台在这个字段上**应当相等**，不受「本库用结算价、
//	            柜台用收盘价」那条已知差异影响
//
// 而 `position_price` / `margin` / 盈亏都挂在**逐日盯市**基线上，
// 那条基线两边本来就不同（类 A，kq_facts 37）。把它们拉进来，
// 红的会是一条已经登记过的差异，而本条想验的是结转本身。
func TestReconstructCoversCarriedSides(t *testing.T) {
	all := loadAll(t)
	byPath := map[string]*Fixture{}
	for _, f := range all {
		byPath[f.Path] = f
	}
	prev, ok := byPath["status-20260908-8.json"]
	if !ok {
		t.Skip("找不到 20260908 的收盘前夹具")
	}
	const sym = "SHFE.rb2701"
	settle, ok := settlementOf(t, sym)
	if !ok {
		t.Fatalf("⚠️ 上期所日行情里没有 %s 的结算价 —— "+
			"本条的整条链子挂在它上面；**不拿柜台的顶替**", sym)
	}

	var fields []conformance.Field
	reconstructed, withToday := 0, 0
	for _, f := range all {
		if f.TradingDay.String() != "20260909" {
			continue
		}
		oracle, ok := f.Positions[sym]
		if !ok {
			continue
		}
		his := numOr(oracle, "volume_long_his")
		if !his.IsPositive() {
			continue // 没有昨仓的方向，原来那两条对拍已经在比了
		}
		p, err := Reconstruct(prev, f, sym, types.Speculation,
			positionDateOf(t, sym), settle)
		if err != nil {
			t.Errorf("⚠️ %s 重建失败：%v", f.Path, err)
			continue
		}
		reconstructed++
		if numOr(oracle, "volume_long_today").IsPositive() {
			withToday++
		}

		s, err := p.Side(types.Buy)
		if err != nil {
			t.Fatal(err)
		}
		key := f.Path + " " + sym + ".long"
		fields = append(fields, conformance.Field{
			Name:      key + ".volume",
			Library:   decimal.NewFromInt(int64(s.Volume())),
			Oracle:    numOr(oracle, "volume_long_today").Add(his),
			Triggered: s.Volume() > 0,
		})
		avg, has := s.AvgOpenPrice()
		o := oracle["open_price_long"]
		opField := conformance.Field{
			Name:          key + ".open_price",
			Library:       avg,
			LibraryAbsent: !has,
			Oracle:        o.Number,
			OracleAbsent:  o.Absent,
			Triggered:     s.Volume() > 0,
		}
		// ⚠️ 这一侧**平过仓**时，open_price 两边必然分岔 —— 而那是**设计差异**，
		// 不是算错。声明成已知口子差异，三样齐全，否则 Validate 会判失败。
		//
		//	柜台   open_cost 按**持仓均价**冲减，于是 open_price 平仓后不动
		//	       实测：今1/昨3 @3159.75 → 平昨 1 手 → 今1/昨2 仍 @3159.75，
		//	       open_cost = 3159.75 × 3 × 10 = 94792.5
		//	本库   保留**逐笔明细**，消耗掉的是具体的那一笔（这里是 3151），
		//	       于是均价变成 (3151+3171+3166)/3 = 3162.6667
		//
		// ⚠️ 这正是 position 包开头那段警告的实证：**均价是有损压缩**。
		// 柜台把信息丢了，所以它的 float_profit 在部分平仓之后
		// 不再是真正的逐笔对冲口径 —— 而两边都不会报错。
		// 本库不改：存明细是 design.md 决策 10，事后补明细等于把历史丢了。
		if closedOnSide(f, sym, types.Buy) {
			opField.Deviation = &conformance.Deviation{
				Fixture: f.Path + "（对照 position-frozen-20260909-5.json，同一持仓平昨前后）",
				Arbiter: "simnow_pending#10：真实 CTP 的 OpenCost 在部分平仓时按均价冲减" +
					"还是按被消耗的明细冲减，未裁决",
				Chose: "本库选**逐笔明细**：逐笔对冲口径要求知道「这一手是哪一笔开的」，" +
					"而均价把它压没了（(2手@100,1手@130) 与 (3手@110) 均价相同，" +
					"平掉1手@120 时逐笔对冲 +20、按均价 +10，两个结果都不报错）。" +
					"design.md 决策 10，事后补明细等于把历史丢了",
			}
		}
		fields = append(fields, opField)
		// 今昨划分也要对上 —— 它才是「结转真的发生了」的证据。
		fields = append(fields, conformance.Field{
			Name:      key + ".volume_his",
			Library:   decimal.NewFromInt(int64(s.VolumeHistory())),
			Oracle:    his,
			Triggered: true,
		})
		fields = append(fields, conformance.Field{
			Name:      key + ".volume_today",
			Library:   decimal.NewFromInt(int64(s.VolumeToday())),
			Oracle:    numOr(oracle, "volume_long_today"),
			Triggered: true,
		})
	}

	if reconstructed == 0 {
		t.Fatal("⚠️ 一个截面都没重建 —— 本条在空转。" +
			"要 20260909 的、rb2701 多头有昨仓的夹具")
	}
	// ⚠️ 判别力：必须有**今昨并存**的样本。
	// 纯昨仓时「结转对了」与「今昨划分对了」分不开 ——
	// 今仓恒为 0，两边都是 0。
	if withToday == 0 {
		t.Fatal("⚠️ 没有一个**今昨并存**的样本 —— " +
			"纯昨仓时今仓两边都是 0，「结转 + 重放叠加」这件事一点没被验到")
	}

	r := conformance.Classify("结转+重放·带昨仓的方向", fields,
		decimal.RequireFromString("0.0000001"))
	t.Logf("重建 %d 个截面（其中今昨并存 %d 个），比了 %d 个字段",
		reconstructed, withToday, len(fields))
	for _, v := range []conformance.Verdict{
		conformance.Matched, conformance.Untriggered,
		conformance.KnownDeviation, conformance.Failed,
	} {
		t.Logf("  %-16s %d", v.String(), r.Counts[v])
	}
	if n := r.Counts[conformance.Failed]; n != 0 {
		t.Errorf("⚠️ 结转+重放之后有 %d 个字段对不上：\n%s\n"+
			"⚠️ 这四个字段（手数、今昨划分、开仓均价）**不该**受"+
			"「本库用结算价、柜台用收盘价」那条已知差异影响 —— "+
			"红在这里说明结转或叠加本身有问题", n, r.Summary())
	}
	// ⚠️ 那处「已知口子差异」的声明必须**真的被用到**。
	// 一处从未被触发的豁免声明，与一段死代码没有区别 —— 而它看起来像做过功课。
	if n := r.Counts[conformance.KnownDeviation]; n == 0 {
		t.Error("⚠️ 一处「已知口子差异」都没判出来 —— " +
			"那处关于 open_price 的声明没被触发过。要么样本里没有平过仓的方向" +
			"（那就该把声明删掉），要么判据坏了")
	}
	if n := r.Counts[conformance.Matched]; n < 8 {
		t.Errorf("⚠️ 只有 %d 个字段「对得上且被触发过」—— 太少", n)
	}
}

// closedOnSide 报告这份夹具里某个方向发生过平仓没有。
func closedOnSide(f *Fixture, sym string, side types.Direction) bool {
	for _, tr := range f.TradesOf(sym) {
		if tr.Offset == types.Open {
			continue
		}
		// 平仓成交的 direction 是**下单方向**，被平的是反向持仓。
		if opposite(tr.Direction) == side {
			return true
		}
	}
	return false
}
