package fixture

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
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
	frozenSamples, frozenNonZero := 0, 0
	skippedStalePre, skippedInconsistent := 0, 0
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

		// —— 冻结：从这份夹具里**挂着的委托**算，与柜台报的比 ——
		//
		// ⚠️ 这是 view 那一侧冻结渲染的**唯一**证据来源。
		// 不接的话，volume_*_frozen_* 三个字段永远落在「未实现」，
		// 而它们的实现写成什么样都不会红。
		if fl, fs, has, ferr := FrozenOf(f, sym, NakedCloseIsYesterday); ferr != nil {
			t.Errorf("⚠️ %s 算冻结失败：%v", f.Path, ferr)
		} else if has {
			frozenSamples++
			for _, fc := range []struct {
				key  string
				want int
			}{
				{"volume_long_frozen_today", fl.VolumeToday},
				{"volume_long_frozen_his", fl.VolumeHistory},
				{"volume_short_frozen_today", fs.VolumeToday},
				{"volume_short_frozen_his", fs.VolumeHistory},
			} {
				o := oracle[fc.key]
				fields = append(fields, conformance.Field{
					Name:      f.Path + " " + sym + "." + fc.key,
					Library:   decimal.NewFromInt(int64(fc.want)),
					Oracle:    o.Number,
					Triggered: fc.want > 0 || o.Number.IsPositive(),
				})
				if fc.want > 0 {
					frozenNonZero++
				}
			}
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
		// —— 保证金：两个方向一起 ——
		//
		// ⚠️ 它**不**受「本库用结算价、柜台用收盘价」那条差异影响：
		// 保证金的基准是**昨结算价**（kq_facts 1），而 D+1 日的昨结算价
		// 就是 D 日的结算价 3163 —— 两边取的是同一个数。
		// position_price 那条差异挂在逐日盯市基线上，与这里无关。
		//
		// ⚠️ 空头一起比：空仓侧至今没被这条链子碰过，而它是昨晚才有的
		// （开了一手空今仓）。只比多头的话，「结转对空头做了什么」一点没验。
		// ⚠️ 只在**柜台自己的昨结算价与交易所的结算价一致**时才比保证金。
		//
		// 不一致时比的是「行情侧滚没滚到新交易日」，不是保证金算得对不对 ——
		// 那是已登记的类 C（kq_facts 29）：账户侧已滚到 20260909，
		// 而行情侧仍停在昨天收盘，于是柜台拿**旧的**昨结算价 3158 算，
		// 得 3×3158×10×0.07 = 6631.8，本库用 3163 得 6642.3。
		//
		// ⚠️ 判据落在**夹具自己报的昨结算价**上，不落在拍摄时间上：
		// 时间要人去查「那时候滚了没有」，而 pre_settlement 是当场可比的。
		// 20 个失败一开始全是这个原因 —— 拿时间去挑会挑错，拿这个数不会。
		curPre, hasPre := f.PreSettlement(sym)
		if !hasPre || !curPre.Equal(settle) {
			skippedStalePre++
			continue
		}
		// ⚠️ 再查一层：**这份截面自己内部自不自洽**。
		//
		// 实测抓到过换挡的那一瞬：18:39:50.268 那份的 quotes.pre_settlement
		// 已经是 3163，而 margin_long 仍是 6631.8（= 3×**3158**×10×0.07）；
		// 1.3 秒后（18:39:51.5）两者都到位。柜台把「行情的昨结算价」与
		// 「持仓的保证金」分成两条消息推，中间有个不到两秒的窗口。
		//
		// ⚠️ 一份自己就不自洽的截面，拿它去比本库**没有意义**：
		// 比出来的差异指向的是采样时刻，不是两边的口径。
		// 这与「重建出来的账户先自查内部不变式再拿去比」是同一条纪律。
		//
		// ⚠️ 判据不引用本库的任何计算：反解柜台自己的基线，与它自己报的昨结算价比。
		if implied, ok := impliedMarginBasis(oracle, "long",
			marginRatesOf(t, sym), multiplierOf(t, sym)); ok &&
			!implied.Sub(curPre).Abs().LessThan(decimal.RequireFromString("0.5")) {
			skippedInconsistent++
			t.Logf("ⓘ %s：柜台**自己内部不自洽** —— margin_long 反解出的基线是 %s，"+
				"而它自己报的 pre_settlement 是 %s（quotes.datetime=%s）。"+
				"换挡瞬间的截面，跳过", f.Path, implied, curPre, oracleDatetime(f, sym))
			continue
		}
		ml, ms, merr := MarginOf(p, marginRatesOf(t, sym), multiplierOf(t, sym),
			settle, false, margin.PreSettleAll, margin.ByInstrument)
		if merr != nil && !IsNoPosition(merr) {
			t.Errorf("%s 算保证金失败：%v", f.Path, merr)
		} else if merr == nil {
			for _, mc := range []struct {
				name string
				lib  decimal.Decimal
			}{{"margin_long", ml}, {"margin_short", ms}} {
				oc := oracle[mc.name]
				fields = append(fields, conformance.Field{
					Name:          key + "." + mc.name,
					Library:       mc.lib,
					LibraryAbsent: mc.lib.IsZero(),
					Oracle:        oc.Number,
					OracleAbsent:  oc.Absent,
					Triggered:     !mc.lib.IsZero(),
				})
			}
		}

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

	// ⚠️ 跳掉的要报出来，而且要求**至少有一个没跳的**：
	// 全跳了的话「保证金对得上」这句话一次都没被验，而本条照样绿。
	t.Logf("ⓘ 保证金比对跳过：行情侧未滚（类 C）%d 个、柜台自己内部不自洽（换挡瞬间）%d 个",
		skippedStalePre, skippedInconsistent)
	if skippedStalePre >= reconstructed && reconstructed > 0 {
		t.Error("⚠️ **每一个**截面的保证金都被跳过了 —— " +
			"那一段等于没跑。要一份行情已滚（pre_settlement 与交易所结算价一致）的夹具")
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

	// ⚠️ 冻结那几个字段必须有**非零**的比对，否则「本库算出 0、柜台报 0」
	// 什么都不说明 —— 把整块冻结逻辑删掉，它照样一致。
	t.Logf("ⓘ 接上冻结的截面 %d 个，其中本库算出非零的 %d 处", frozenSamples, frozenNonZero)
	if frozenSamples > 0 && frozenNonZero == 0 {
		t.Error("⚠️ 接上了冻结，但本库一处非零都没算出来 —— " +
			"全零的一致什么都不说明。要一份**挂着单**时拍的夹具")
	}
	// ⚠️ 「压根没接」也要拦，而这一条是破坏验证逼出来的：
	// 第 137 条把接冻结那段整个关掉，测试**仍然绿** —— 不比就没有失败。
	// 上面那条只管「接了但全零」，管不到「一个都没接」。
	//
	// 判据：只要**语料里有记了委托的夹具**，就必须至少接上一个。
	// 一个都接不上，要么是接线断了，要么是那些夹具全被前面的条件筛掉了 ——
	// 两种都要人去看，而不是让这条测试继续绿着。
	withOrders := 0
	for _, ff := range all {
		if ff.HasOrders {
			withOrders++
		}
	}
	if withOrders > 0 && frozenSamples == 0 {
		t.Errorf("⚠️ 语料里有 %d 份夹具记了委托，而本条**一个都没接上冻结** —— "+
			"要么接线断了，要么那些夹具全被前面的条件筛掉了。"+
			"⚠️ 不接就没有失败，于是 volume_*_frozen_* 的实现"+
			"写成什么样都不会红", withOrders)
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

// marginRates / multiplierOf 取某合约的规则数据，**没登记就报错**。
//
// ⚠️ 两个都不给默认值：一个「差不多能用」的保证金率会静默算错，
// 而算出来的数看起来完全正常。
func marginRatesOf(t *testing.T, sym string) refdata.MarginRates {
	t.Helper()
	inst, err := types.ParseSymbol(sym, types.NewTradingDay(2026, 9, 9))
	if err != nil {
		t.Fatal(err)
	}
	product, _ := splitProduct(inst.Product)
	r, ok := ratesFor(product)
	if !ok {
		t.Fatalf("品种 %s 没有登记保证金率", product)
	}
	return r
}

func multiplierOf(t *testing.T, sym string) decimal.Decimal {
	t.Helper()
	m, ok := multipliers[sym]
	if !ok {
		t.Fatalf("%s 没有登记乘数", sym)
	}
	return decimal.RequireFromString(m)
}

// impliedMarginBasis 从柜台**自己的** margin 反解它用的基准价。
//
//	基准价 = margin ÷ (手数 × 乘数 × 保证金率)
//
// ⚠️ 它一处都不引用本库的计算，所以拿它去判「柜台自不自洽」不是循环论证。
// 第二个返回值报告解不解得出（空仓、缺字段、率为零时解不出）。
func impliedMarginBasis(oracle map[string]Value, side string,
	rates refdata.MarginRates, mult decimal.Decimal) (decimal.Decimal, bool) {

	m, ok := numberOf(oracle, "margin_"+side)
	if !ok || !m.IsPositive() {
		return decimal.Zero, false
	}
	vol := numOr(oracle, "volume_"+side+"_today").Add(numOr(oracle, "volume_"+side+"_his"))
	if !vol.IsPositive() {
		return decimal.Zero, false
	}
	rate := rates.LongByMoney
	if side == "short" {
		rate = rates.ShortByMoney
	}
	den := vol.Mul(mult).Mul(rate)
	if !den.IsPositive() {
		return decimal.Zero, false
	}
	return m.Div(den), true
}

// oracleDatetime 取行情截面的时间戳，纯粹为了让日志能定位到那一瞬。
func oracleDatetime(f *Fixture, sym string) string {
	v, ok := f.Quotes[sym]["datetime"]
	if !ok || !v.IsText {
		return "(无)"
	}
	return v.Text
}
