// Package conformance 是**连着柜台跑**的逐字段对拍。
//
// # ⚠️ 它与主模块里那批离线对拍的分工
//
//	主模块的测试   拿**已入库的夹具**对拍 —— 可复现、进 CI、退化会红
//	本包           拿**此刻柜台上的真实截面**对拍 —— 不可复现，但它验的是「现在」
//
// 两者缺一不可，理由不对称：
//
//   - 只有离线：夹具是过去某一刻的快照。柜台改了口径，夹具不会变，
//     全套测试照样绿 —— 而那正是「永远绿的测试」的一个变种，只是绿得更隐蔽。
//   - 只有在线：结论不可复现，红了之后没人能重跑同一份输入。
//
// # ⚠️ 一份实现，两条路
//
// 本包**不自己写比对逻辑**。它把实时截面 marshal 成夹具的 JSON，
// 交给主模块的 `conformance/fixture.Load` 解析，再走同一套
// `ComparePosition` / `Classify`。
//
// 那是刻意的：比对逻辑写两份，两份就会漂移，而漂移的表现是
// **同一个柜台在两条路上给出不同的判定**，且谁都不报错。
// 走同一条解析还顺带一个好处：夹具格式漂移会**同时**打断两条路，
// 而不是只打断没人在跑的那一条。
package conformance

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	libconf "github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/conformance/fixture"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/dream-until-dawn/futures-position-simulator-go/view"
	"github.com/shopspring/decimal"
)

// Spec 是对拍一个合约所需的规则数据。
//
// ⚠️ 与 fixture.Spec 同理：**没有默认值**。一个「差不多能用」的乘数或费率
// 会让每一个金额都错，而错出来的数看起来完全正常。
type Spec struct {
	Multiplier   decimal.Decimal
	Margin       refdata.MarginRates
	PositionDate refdata.PositionDateType
}

// Result 是一次实时对拍的结果。
type Result struct {
	TradingDay string
	// Symbols 是**真的比过**的合约。
	Symbols []string
	// SkippedNoSpec / SkippedHistory 是跳过的两类，各自记数。
	//
	// ⚠️ 分开记：前者是「我们没准备好规则数据」，后者是「这份输入不自足」。
	// 合并成一个「跳过 N 个」，会让人以为只要补规则数据就能全比上。
	SkippedNoSpec  []string
	SkippedHistory []string
	// SkippedNoTrades 是**有持仓、而当日没有成交**的合约。
	//
	// ⚠️ 它与上面两类又不一样：不是我们没准备好，也不是输入不自足，
	// 是**柜台的成交表按交易日重置**——今天之前开的仓，实时截面里
	// 没有任何一笔成交能解释它。要比它得先有前一日的夹具走 Carry。
	SkippedNoTrades []string
	Report          *libconf.Report

	// KnownFailures 是**已登记**的口子差异，按类分组；
	// NovelFailures 是**第三类** —— 新出现、还没有解释的不一致。
	//
	// ⚠️ 分开是这个工具能不能被人看的关键：混在一个「失败 N 个」里，
	// 新东西会被淹在一堆已知里，而已知的那堆每次都在。
	// ⚠️ 命令的退出码只看 NovelFailures：拿已知差异让命令失败，
	// 会让这个工具**永远红**，而永远红与永远绿一样会被无视。
	KnownFailures map[string][]string
	NovelFailures []string
}

// Compare 把一份实时截面与本库逐字段比。
//
// raw 是 `kq.Sanitize` 产出的夹具结构（调用方 marshal 成 JSON 传进来），
// 走的是**主模块的解析器**，与离线对拍完全同一条路。
//
// ⚠️ 带昨仓的方向一律跳过并记数，理由与离线那条一样：
// 实时截面里只有**当日**成交，昨仓那几手的开仓单在前一交易日。
// 要比它们得先有前一日的夹具走 Carry —— 那是 `-carry` 的事，本函数不猜。
func Compare(raw []byte, specs map[string]Spec, carry *Carry) (Result, error) {
	var res Result
	f, err := fixture.Load(strings.NewReader(string(raw)), "（实时截面）")
	if err != nil {
		return res, fmt.Errorf("实时截面解析失败：%w —— "+
			"⚠️ 它走的是主模块的夹具解析器，所以这里失败意味着"+
			"**离线那批夹具也读不进来**，先去查格式漂移", err)
	}
	res.TradingDay = f.TradingDay.String()

	var fields []libconf.Field
	for _, sym := range f.Symbols() {
		spec, ok := specs[sym]
		if !ok {
			res.SkippedNoSpec = append(res.SkippedNoSpec, sym)
			continue
		}
		// —— 需要前一日夹具的两种情形，能结转就结转 ——
		//
		//	有昨仓            昨仓那几手的开仓单在前一交易日
		//	有持仓但当日无成交  柜台成交表按交易日重置
		//
		// ⚠️ 结转要一个**交易所**给的结算价。拿柜台自己的顶替是同义反复，
		// 所以 carry 里没有这个合约的结算价时**不猜**，照样记进跳过。
		needsCarry := f.HasHistoryPosition(sym) || len(f.TradesOf(sym)) == 0
		if needsCarry {
			p, err := carry.reconstruct(f, sym, spec)
			if err != nil {
				if f.HasHistoryPosition(sym) {
					res.SkippedHistory = append(res.SkippedHistory, sym+"："+err.Error())
				} else if hasAnyVolume(f.Positions[sym]) {
					res.SkippedNoTrades = append(res.SkippedNoTrades, sym+"："+err.Error())
				}
				continue
			}
			fs, err := compareOne(f, sym, spec, p)
			if err != nil {
				return res, err
			}
			fields = append(fields, fs...)
			res.Symbols = append(res.Symbols, sym+"（结转）")
			continue
		}
		trades := f.TradesOf(sym)
		p, err := fixture.Replay(trades[0].Instrument, types.Speculation,
			spec.PositionDate, f.TradingDay, trades)
		if err != nil {
			return res, fmt.Errorf("重放 %s：%w", sym, err)
		}
		fs, err := compareOne(f, sym, spec, p)
		if err != nil {
			return res, err
		}
		fields = append(fields, fs...)
		res.Symbols = append(res.Symbols, sym)
	}
	sort.Strings(res.Symbols)
	sort.Strings(res.SkippedNoSpec)
	sort.Strings(res.SkippedHistory)
	sort.Strings(res.SkippedNoTrades)

	// ⚠️ 一个合约都没比到时**报错**，不返回一份「全绿」的空报告。
	// 一次什么都没比的对拍，与一次全对的对拍，在汇总行里长得一模一样。
	if len(res.Symbols) == 0 {
		return res, fmt.Errorf("⚠️ 一个合约都没比到 —— "+
			"没登记规则数据 %d 个、有昨仓 %d 个、有持仓但当日无成交 %d 个。"+
			"本次对拍**什么都没验**，不要把它读成通过。"+
			"⚠️ 后两类都要前一日的夹具走 Carry 才比得了（见 -carry）",
			len(res.SkippedNoSpec), len(res.SkippedHistory), len(res.SkippedNoTrades))
	}
	res.Report = libconf.Classify("实时截面",
		fields, decimal.RequireFromString("0.0000001"))

	// ⚠️ 把失败分成「已登记的口子差异」与「**第三类**：新出现的」。
	//
	// 不分的话这个工具**永远红** —— 已知差异每次都在，而一个永远红的
	// 对拍工具，和一个永远绿的一样会被无视，只是被无视的理由听起来更正当。
	// 第三类才是要人立刻去查的。
	reg := libconf.Live()
	if err := reg.Validate(); err != nil {
		return res, fmt.Errorf("已知差异登记表本身不合格，**不出结论**：%w", err)
	}
	var failed []string
	for name, v := range res.Report.Verdicts {
		if v == libconf.Failed {
			failed = append(failed, name)
		}
	}
	res.KnownFailures, res.NovelFailures = reg.Split(failed)
	return res, nil
}

// String 渲染成可读的报告。
func (r Result) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "交易日 %s，比了 %d 个合约：%s\n",
		r.TradingDay, len(r.Symbols), strings.Join(r.Symbols, " "))
	if len(r.SkippedNoSpec) > 0 {
		fmt.Fprintf(&b, "  ⓘ 跳过（没登记规则数据）%d 个：%s\n",
			len(r.SkippedNoSpec), strings.Join(r.SkippedNoSpec, " "))
	}
	if len(r.SkippedHistory) > 0 {
		fmt.Fprintf(&b, "  ⓘ 跳过（有昨仓，实时截面里只有当日成交）%d 个：%s\n",
			len(r.SkippedHistory), strings.Join(r.SkippedHistory, " "))
	}
	if len(r.SkippedNoTrades) > 0 {
		fmt.Fprintf(&b, "  ⚠️ 跳过（**有持仓**而当日无成交，柜台成交表按交易日重置）%d 个：%s\n",
			len(r.SkippedNoTrades), strings.Join(r.SkippedNoTrades, " "))
	}
	b.WriteString(r.Report.Summary())
	if len(r.KnownFailures) > 0 {
		b.WriteString("\n已登记的口子差异（**不算新问题**）：\n")
		classes := make([]string, 0, len(r.KnownFailures))
		for c := range r.KnownFailures {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		for _, c := range classes {
			fmt.Fprintf(&b, "  %-10s %d 个：%s\n",
				c, len(r.KnownFailures[c]), strings.Join(r.KnownFailures[c], " "))
		}
	}
	if len(r.NovelFailures) > 0 {
		fmt.Fprintf(&b, "\n⚠️⚠️ **第三类**（新出现、没有解释）%d 个：%s\n"+
			"   这几个要**立刻去查**：它们不在已知差异登记表里。\n",
			len(r.NovelFailures), strings.Join(r.NovelFailures, " "))
	}
	return b.String()
}

// MarshalFixture 把 kq.Sanitize 的产物 marshal 成夹具 JSON。
//
// ⚠️ 单独一个函数是为了让「实时那条路与落盘那条路用的是同一份字节」
// 在代码上是显然的：`probe` 落盘写的也是 `json.Marshal(f)`。
func MarshalFixture(f any) ([]byte, error) { return json.Marshal(f) }

// hasAnyVolume 报告这个持仓截面上四个手数字段里有没有非零的。
//
// ⚠️ 读不到不当成零：读不到意味着字段集漂移，那与「空仓」是两回事，
// 而把前者当成后者会让一个有持仓的合约被悄悄丢掉。
func hasAnyVolume(pos map[string]fixture.Value) bool {
	for _, k := range []string{
		"volume_long_today", "volume_long_his",
		"volume_short_today", "volume_short_his",
	} {
		v, ok := pos[k]
		if !ok || v.Absent || v.IsText {
			continue
		}
		if v.Number.IsPositive() {
			return true
		}
	}
	return false
}

// compareOne 把一个合约的本库持仓与柜台截面逐字段比。
//
// ⚠️ 抽出来是因为**有两条路要用它**：当日重放的、与从前一日结转的。
// 写两份比对准备（取最新价、算保证金、渲染视图）会让两条路在
// 「用不用昨结算价算保证金」这类细节上悄悄分岔。
func compareOne(f *fixture.Fixture, sym string, spec Spec,
	p *position.Position) ([]libconf.Field, error) {

	oracle := f.Positions[sym]
	last := oracle["last_price"]
	in := view.PositionInput{
		Multiplier: spec.Multiplier,
		LastPrice:  last.Number, HasLast: !last.Absent && !last.IsText,
	}
	if pre, ok := f.PreSettlement(sym); ok {
		l, s, merr := fixture.MarginOf(p, spec.Margin, spec.Multiplier, pre,
			false, margin.PreSettleAll, margin.ByInstrument)
		if merr == nil {
			in.MarginLong, in.MarginShort, in.HasMargin = l, s, true
		} else if !fixture.IsNoPosition(merr) {
			return nil, fmt.Errorf("%s 算保证金：%w", sym, merr)
		}
	}
	lib, err := view.PositionOf(p, in)
	if err != nil {
		return nil, fmt.Errorf("%s 渲染视图：%w", sym, err)
	}
	fs, errs := fixture.ComparePosition(lib, oracle, fixture.TriggeredByVolume(oracle))
	if len(errs) > 0 {
		return nil, fmt.Errorf("%s 逐字段比对报错：%v", sym, errs)
	}
	return fs, nil
}

// Carry 是「从前一交易日结转」所需的两样东西。
//
// ⚠️ 两样**都必须由调用方给**，本包不去取、不去猜：
//
//	Prev        前一交易日的夹具 —— 昨仓那几手的开仓成交在里面
//	Settlement  **交易所**给的当日结算价，按合约
//
// ⚠️ 结算价刻意要求来自交易所而不是柜台：拿柜台自己的结算价去验
// 柜台自己的逐日盯市，是同义反复。缺哪个合约的结算价就跳过哪个 ——
// 少一个就是少一个，不拿别处的顶替。
type Carry struct {
	Prev       *fixture.Fixture
	Settlement map[string]decimal.Decimal
}

// reconstruct 结转一个合约。nil 接收者表示**调用方没给 -carry**。
func (c *Carry) reconstruct(cur *fixture.Fixture, sym string, spec Spec) (*position.Position, error) {
	if c == nil || c.Prev == nil {
		return nil, fmt.Errorf("没给 -carry（前一交易日的夹具）")
	}
	settle, ok := c.Settlement[sym]
	if !ok {
		return nil, fmt.Errorf("没有该合约的**交易所**结算价（不拿柜台的顶替）")
	}
	return fixture.Reconstruct(c.Prev, cur, sym, types.Speculation,
		spec.PositionDate, settle)
}
