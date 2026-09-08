package view

import (
	"fmt"
	"sort"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Position 是持仓截面视图，键用柜台（DIFF）的字段名。
type Position map[string]Value

// PositionInput 是渲染持仓视图需要的、`position.Position` 里**没有**的东西。
//
// ⚠️ 这些不是「顺手补一下」的参数，它们各自代表一层依赖：
// 乘数来自规则数据，最新价来自行情，保证金来自 margin 包。
// 把它们做成显式入参而不是从别处偷，是为了让**每一个字段的来源在类型上就是可见的**。
type PositionInput struct {
	Multiplier decimal.Decimal

	LastPrice decimal.Decimal
	HasLast   bool

	// MarginLong / MarginShort 是该方向的持仓保证金合计，来自 margin 包。
	MarginLong, MarginShort decimal.Decimal
	HasMargin               bool
}

// sideStats 是一个方向上从逐笔明细算出来的量。
type sideStats struct {
	volToday, volHis int
	// openCost 是 Σ(开仓价 × 手数)，尚未乘乘数。
	openCost, posCost decimal.Decimal
	openCostToday     decimal.Decimal
	openCostHis       decimal.Decimal
	posCostToday      decimal.Decimal
	posCostHis        decimal.Decimal
}

func statsOf(s *position.Side) sideStats {
	var st sideStats
	for _, l := range s.Lots() {
		v := decimal.NewFromInt(int64(l.Volume))
		oc := l.OpenPrice.Mul(v)
		pc := l.Basis.Mul(v)
		st.openCost = st.openCost.Add(oc)
		st.posCost = st.posCost.Add(pc)
		if l.Settled {
			st.volHis += l.Volume
			st.openCostHis = st.openCostHis.Add(oc)
			st.posCostHis = st.posCostHis.Add(pc)
		} else {
			st.volToday += l.Volume
			st.openCostToday = st.openCostToday.Add(oc)
			st.posCostToday = st.posCostToday.Add(pc)
		}
	}
	return st
}

func (st sideStats) volume() int { return st.volToday + st.volHis }

// avg 返回加权均价；手数为零时**没有均价**，不是零。
//
// ⚠️ 实测支持这条，而且是干净的全称命题：188 份持仓截面里，
// open_price_long/short 与 position_price_long/short **空仓 ⟺ "-"，
// 188/188 无反例**（probes.md §9）。柜台自己就把「没有」与「零」分开了。
//
// ⚠️ 但**不要**把这条推广到成本字段：open_cost_long 在 144 个空仓样本里
// 有 139 个是 "-"、5 个是 0，那 5 个全是「当天曾持过仓、后来平掉」的合约。
// 也就是说成本字段的 "-" 与 0 是**路径依赖**的，而本库没有
// 「这个字段今天被设过没有」这个状态 —— 那一条本库复现不了，也不该假装能。
func (st sideStats) avg(cost decimal.Decimal) (decimal.Decimal, bool) {
	if st.volume() == 0 {
		return decimal.Zero, false
	}
	return cost.Div(decimal.NewFromInt(int64(st.volume()))), true
}

// total 把两个方向的同名字段合起来。
//
// ⚠️ 三种输入，三种结果，混掉任何一对都会静默出错：
//
//	两边都有值    相加
//	两边都无值    仍然无值 —— **不是 0**
//	一边有一边无  取有值的那边（另一边空仓，它对合计的贡献确实是「没有」）
//
// 而只要任一边是「还没实现」，合计就也是「还没实现」：
// 一个把未实现项按 0 计入的合计，会给出一个看起来正常的错数。
func total(a, b Value, whyNone string) Value {
	if a.Presence == NotImplemented || b.Presence == NotImplemented {
		return Todo("合计的某一边还没实现 —— 按 0 计入会给出一个看起来正常的错数")
	}
	if a.Presence == NotModeled || b.Presence == NotModeled {
		return Todo("合计的某一边声明为不建模，合计因此也算不出")
	}
	switch {
	case a.Presence == Absent && b.Presence == Absent:
		return None(whyNone)
	case a.Presence == Absent:
		return Num(b.Number)
	case b.Presence == Absent:
		return Num(a.Number)
	default:
		return Num(a.Number.Add(b.Number))
	}
}

// PositionOf 渲染持仓视图。
func PositionOf(p *position.Position, in PositionInput) (Position, error) {
	long, err := p.Side(types.Buy)
	if err != nil {
		return nil, err
	}
	short, err := p.Side(types.Sell)
	if err != nil {
		return nil, err
	}
	if !in.Multiplier.IsPositive() {
		// ⚠️ 乘数为零会让所有金额变成 0，而 0 看起来完全合理。
		return nil, fmt.Errorf("合约乘数必须为正，得到 %s —— "+
			"乘数漏乘会得到一个量级正确到肉眼看不出的错值", in.Multiplier)
	}
	m := in.Multiplier
	ls, ss := statsOf(long), statsOf(short)

	v := Position{}
	num := func(k string, d decimal.Decimal) { v[k] = Num(d) }
	i64 := func(k string, n int) { v[k] = Num(decimal.NewFromInt(int64(n))) }

	for _, side := range []struct {
		name string
		st   sideStats
	}{{"long", ls}, {"short", ss}} {
		st := side.st
		n := side.name
		i64("volume_"+n, st.volume())
		i64("volume_"+n+"_today", st.volToday)
		i64("volume_"+n+"_his", st.volHis)
		// ⚠️ pos_*_today / pos_*_his 与 volume_*_today / _his 在实测样本里同值，
		// 但「同值」不等于「同义」—— 本次样本从未把它们分开过。
		// 渲染成同一个数是本库的**建模选择**，而它在对拍时会落进
		// 「值对得上但未触发」那一档，那正是它该待的地方。
		i64("pos_"+n+"_today", st.volToday)
		i64("pos_"+n+"_his", st.volHis)

		num("open_cost_"+n, st.openCost.Mul(m))
		num("position_cost_"+n, st.posCost.Mul(m))
		// ⚠️ 今昨拆分的四个成本字段：本库算得出，而**实测柜台一律给 0**。
		//
		// 188 份持仓截面里，open_cost_*_today / _his 与
		// position_cost_*_today / _his 全是 0 —— 从不是 "-"，也从不是数值；
		// 其中 **47 个方向是有今仓的**（volume_*_today > 0），
		// 也就是说「因为今天没有今仓所以是 0」这个解释被否掉了。
		//
		// 这里仍然渲染本库算出来的真值，而不是跟着填 0。理由：
		// 跟着填 0 就是在**没有裁决者**的情况下选边，而按 design.md §5，
		// 判「已知口子差异」需要出处、裁决者、选边理由三样齐全，现在只有出处。
		// 于是这四个字段今晚会对拍成红 —— **红是正确的结果**，
		// 它说的是「两边不一样且还没人裁决」，不是「本库算错了」。
		num("open_cost_"+n+"_today", st.openCostToday.Mul(m))
		num("open_cost_"+n+"_his", st.openCostHis.Mul(m))
		num("position_cost_"+n+"_today", st.posCostToday.Mul(m))
		num("position_cost_"+n+"_his", st.posCostHis.Mul(m))

		if avg, ok := st.avg(st.openCost); ok {
			num("open_price_"+n, avg)
		} else {
			v["open_price_"+n] = None("该方向空仓，没有开仓均价 —— " +
				"⚠️ 这是**结论**不是欠债；柜台在这种情形下返回 \"-\"（实测 188/188）")
		}
		if avg, ok := st.avg(st.posCost); ok {
			num("position_price_"+n, avg)
		} else {
			v["position_price_"+n] = None("该方向空仓，没有持仓均价，同 open_price（实测 188/188）")
		}

		// —— 浮动盈亏与持仓盈亏：两条基线的区别就在这里 ——
		//
		// ⚠️ float_profit 用**开仓价**基线（逐笔对冲），
		// position_profit 用**持仓均价**基线（逐日盯市，昨仓已被结算价重置）。
		// 今仓上两者相等，所以**今仓样本对这条区分没有判别力** —— 实测确认过。
		if st.volume() == 0 {
			// ⚠️ 空仓方向**没有**盈亏，不是盈亏为零 —— 同 open_price 的理由：
			// 「一个不存在的持仓的浮动盈亏」不是 0，是没有。
			//
			// ⚠️ 而柜台这边在空仓时给什么是**路径依赖**的：
			// 同一个合约 SHFE.rb2610、同样两边空仓，
			// 一份截面里 float_profit 是 "-"、另一份里是 0（见 probes.md §9）。
			// 本库没有「这个字段今天被设过没有」这个状态，**复现不了，也不假装能**；
			// 后果是这些字段在空仓样本上**不可判**，那要在对拍侧写明，不能糊过去。
			v["float_profit_"+n] = None("该方向空仓，没有浮动盈亏")
			v["position_profit_"+n] = None("该方向空仓，没有持仓盈亏")
		} else if in.HasLast {
			last := in.LastPrice
			vol := decimal.NewFromInt(int64(st.volume()))
			sign := decimal.NewFromInt(1)
			if n == "short" {
				sign = decimal.NewFromInt(-1)
			}
			num("float_profit_"+n, last.Mul(vol).Sub(st.openCost).Mul(m).Mul(sign))
			num("position_profit_"+n, last.Mul(vol).Sub(st.posCost).Mul(m).Mul(sign))
		} else {
			v["float_profit_"+n] = Todo("没有最新价就算不出浮动盈亏 —— 这是「没有」不是「零」")
			v["position_profit_"+n] = Todo("没有最新价就算不出持仓盈亏")
		}

		// —— 保证金 ——
		mg := in.MarginLong
		if n == "short" {
			mg = in.MarginShort
		}
		switch {
		case st.volume() == 0:
			// ⚠️ 空仓方向**没有**保证金，不是保证金为零。
			// 实测 margin_long / margin_short 同样是「空仓 ⟺ "-"」，188/188。
			v["margin_"+n] = None("该方向空仓，没有持仓保证金（实测 188/188 为 \"-\"）")
		case in.HasMargin:
			num("margin_"+n, mg)
		default:
			v["margin_"+n] = Todo("保证金由 margin 包算，本视图不重算 —— " +
				"重算会产生第二个实现，而两个实现一起退化时测试全绿")
		}
		// ⚠️ 今昨仓的保证金拆分：本库**确实还算不出**（margin 包按 leg 聚合，
		// 不给逐笔），所以这两个是真欠债，与上面那四个成本字段不同 ——
		// 那四个是「本库算得出但两边不一致」，这两个是「本库没有」。
		//
		// 实测柜台这两个字段也恒为 0（188/188，含 47 个有今仓的方向），
		// 但那不改变本库这边的状态：欠债就是欠债，不能因为对方也没填就算平。
		v["margin_"+n+"_today"] = Todo("需要逐笔保证金，而 margin 包按 leg 聚合 —— " +
			"⚠️ 实测柜台此字段亦恒为 0（188/188，含 47 个有今仓的方向），" +
			"但「对方也没填」不能把本库的欠债抵消掉")
		v["margin_"+n+"_his"] = Todo("同 margin_*_today")

		// —— 报单冻结：v0.4.0 ——
		v["volume_"+n+"_frozen"] = Todo("报单冻结在 v0.4.0（order 包），本库尚无")
		v["volume_"+n+"_frozen_today"] = Todo("同上")
		v["volume_"+n+"_frozen_his"] = Todo("同上")

		// —— ⚠️ volume_*_yd：与 _his 的差别是待实测第 7 条 ——
		v["volume_"+n+"_yd"] = Todo("⚠️ 它与 volume_" + n + "_his 的差别**尚未实测**" +
			"（cn-futures-rules.md §13 第 7 条）。猜一个映射就是把待实测项当成已知")

		// —— 期权 ——
		v["market_value_"+n] = Skip("v1.0.0", "期权市值；v1.0 不含期权")
	}

	// —— 合计项 ——
	//
	// ⚠️ 合计不是「把两边加起来」那么简单：一边有值一边无值时，
	// 把无值当成 0 加进去，就把 Absent 悄悄降格成了 Present。
	// total 只在**两边都可加**时才有值。
	v["float_profit"] = total(v["float_profit_long"], v["float_profit_short"],
		"两个方向都空仓，没有浮动盈亏")
	v["position_profit"] = total(v["position_profit_long"], v["position_profit_short"],
		"两个方向都空仓，没有持仓盈亏")
	v["margin"] = total(v["margin_long"], v["margin_short"],
		"两个方向都空仓，没有持仓保证金")
	if in.HasLast {
		v["last_price"] = Num(in.LastPrice)
	} else {
		v["last_price"] = Todo("行情由调用方提供，本视图不持有行情")
	}
	v["market_value"] = Skip("v1.0.0", "期权市值合计；v1.0 不含期权")

	// —— 标识与状态 ——
	v["instrument_id"] = Todo("合约代码是字符串，本视图只承载数值字段")
	v["exchange_id"] = Todo("交易所代码是字符串，同上")
	v["market_status"] = Todo("盘口状态来自行情，本库不建模")

	for name, val := range v {
		if err := val.Validate(name); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// Fields 返回视图声明的全部字段名，升序。
func (p Position) Fields() []string {
	out := make([]string, 0, len(p))
	for k := range p {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// CoverExactly 断言字段集与给定集合完全一致，两个方向都查。
func (p Position) CoverExactly(want []string) error {
	return Account(p).CoverExactly(want)
}
