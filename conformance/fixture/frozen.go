package fixture

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// FrozenOf 从夹具里**挂着的委托**算出某个合约的冻结手数。
//
// ⚠️ 它是冻结那一整块能不能对拍的桥：
// 柜台的 `volume_*_frozen_*` 是「挂着的委托」的函数，而在 20260909 之前
// 夹具里根本没有委托 —— 于是本库算出来的冻结拿什么去比都比不了。
// 破坏验证当场演示过这一点：把冻结合计改成漏掉昨仓那部分，
// **全套测试照样绿**，因为没有任何一份夹具能走到那一支。
//
// 第二个返回值报告**这份夹具记没记委托**：
//
//	false  20260909 之前的夹具 —— 冻结比不了，调用方应当把这些字段留作「未实现」
//	true   记了 —— 哪怕一笔挂单都没有，那也是一个**可以拿去对拍**的结论
//
// ⚠️ 这个区分不能省：两种情形下冻结量都是 0，判成一致什么都不说明。
func FrozenOf(f *Fixture, symbol string, naked NakedClosePolicy) (long, short order.Frozen, has bool, err error) {
	zero := order.Frozen{Margin: decimal.Zero, Commission: decimal.Zero}
	if !f.HasOrders {
		return zero, zero, false, nil
	}
	long, short = zero, zero
	for id, o := range f.Orders {
		// ⚠️ 只数**还挂着**的：已终结的委托不冻任何东西。
		// 把它们算进去，冻结量会一直挂着，而账面上只表现为
		// 「可用资金少了一点 / 可平量少了几手」—— 不报错，只是偏保守。
		alive, ok := aliveOf(o)
		if !ok {
			return zero, zero, false, fmt.Errorf(
				"委托 %s 没有 status 字段 —— ⚠️ 读不到不当成「挂着」也不当成「终结」："+
					"前者会凭空冻住手数，后者会漏掉真实的冻结", id)
		}
		if !alive {
			continue
		}
		if sym, _ := textOf(o, "exchange_id", "instrument_id"); sym != symbol {
			continue
		}
		left, ok := numberOf(o, "volume_left")
		if !ok || !left.IsPositive() {
			continue // 未成交量为零：挂着但没什么可冻的
		}
		dir, off, err := dirOffsetOf(o)
		if err != nil {
			return zero, zero, false, fmt.Errorf("委托 %s：%w", id, err)
		}
		if off == types.Open {
			continue // 开仓单冻的是账户侧的钱，不冻持仓手数
		}
		n := int(left.IntPart())
		// ⚠️ 平仓委托的 direction 是**下单方向**，被平的是反向持仓。
		// 搞反会把冻结记到另一边，而两边都有仓时不会报错。
		var into *order.Frozen
		if dir == types.Sell {
			into = &long
		} else {
			into = &short
		}
		switch off {
		case types.CloseToday:
			into.VolumeToday += n
		case types.CloseYesterday:
			into.VolumeHistory += n
		default:
			// 裸 CLOSE：冻今仓还是昨仓**取决于口子**。
			// ⚠️ 由调用方显式声明按哪种语义解释，零值是「拒绝」。
			switch naked {
			case NakedCloseIsYesterday:
				into.VolumeHistory += n
			default:
				return zero, zero, false, fmt.Errorf(
					"委托 %s 是裸 CLOSE，冻的是今仓还是昨仓**取决于口子**"+
						"（快期上等于平昨，kq_facts 32；simnow_pending#1 未裁决）—— "+
						"本函数不猜。要按某个口子的语义解释，"+
						"请在调用处显式传 NakedCloseIsYesterday", id)
			}
		}
	}
	return long, short, true, nil
}

// aliveOf 判断一笔委托还挂着没有。
func aliveOf(o map[string]Value) (alive, ok bool) {
	v, exists := o["status"]
	if !exists || !v.IsText {
		return false, false
	}
	return strings.EqualFold(v.Text, "ALIVE"), true
}

// textOf 把几个文本字段拼成合约键，形如 SHFE.rb2701。
func textOf(o map[string]Value, keys ...string) (string, bool) {
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, ok := o[k]
		if !ok || !v.IsText {
			return "", false
		}
		parts = append(parts, v.Text)
	}
	return strings.Join(parts, "."), true
}

// dirOffsetOf 读委托的方向与开平标志。
//
// ⚠️ 读不到就报错，不给默认值：一个默认成 OPEN 的开平标志
// 会让平仓委托的冻结整个消失，而账面上只表现为「可平量多了几手」。
func dirOffsetOf(o map[string]Value) (types.Direction, types.Offset, error) {
	dv, ok := o["direction"]
	if !ok || !dv.IsText {
		return 0, 0, fmt.Errorf("没有 direction 字段")
	}
	ov, ok := o["offset"]
	if !ok || !ov.IsText {
		return 0, 0, fmt.Errorf("没有 offset 字段")
	}
	var dir types.Direction
	switch strings.ToUpper(dv.Text) {
	case "BUY":
		dir = types.Buy
	case "SELL":
		dir = types.Sell
	default:
		return 0, 0, fmt.Errorf("买卖方向 %q 不认识", dv.Text)
	}
	var off types.Offset
	switch strings.ToUpper(ov.Text) {
	case "OPEN":
		off = types.Open
	case "CLOSE":
		off = types.Close
	case "CLOSETODAY":
		off = types.CloseToday
	case "CLOSEYESTERDAY":
		off = types.CloseYesterday
	default:
		return 0, 0, fmt.Errorf("开平标志 %q 不认识", ov.Text)
	}
	return dir, off, nil
}

// NakedClosePolicy 说明裸 `CLOSE` 的委托按哪种语义解释。
//
// ⚠️ 它必须是**调用点的显式选择**，不能由本包定。理由是这个语义
// **取决于口子**：快期模拟上裸 CLOSE 等于平昨（kq_facts 32，两条独立证据），
// 而真实 CTP 未裁决（simnow_pending#1）。
//
// 猜错的后果是把冻结记到另一边 —— 而那**不会以失败的形式出现**：
// 两边都是「冻了 1 手」，只是冻在了不同的桶里。
type NakedClosePolicy uint8

const (
	// NakedCloseRefuse 是零值：遇到裸 CLOSE 就报错。
	//
	// ⚠️ 零值选「拒绝」而不是某种语义，是因为默认值会被沿用而不被注意到。
	NakedCloseRefuse NakedClosePolicy = iota
	// NakedCloseIsYesterday 按「裸 CLOSE = 平昨」解释。
	//
	// ⚠️ 这是**快期模拟**上的实测语义（kq_facts 32），
	// 用它意味着你在对拍**那个口子**。换口子时要重新量。
	NakedCloseIsYesterday
)

// FrozenAccountOf 算一份夹具里**全部挂着的委托**冻结的金额合计。
//
// ⚠️ 它把每一笔委托喂进 `order.Book`，返回 `book.Total()` ——
// **不自己加**。这是 20260909 收掉的一处重复：账户侧对拍原先自己建一个
// Book 逐笔 Insert，而 Rebuild 那条路走本函数自己累加，
// 于是同一件事有了两份实现。⚠️ 两份实现之间**从来没有任何东西比过**，
// 它们漂移时的表现是「两条路对同一份夹具给出不同的冻结额」，且谁都不报错。
//
// 走 Book 还顺带把 order 包的那几条拒绝（开仓单零保证金、平仓单非零保证金、
// 裸 CLOSE、重复委托号）接进了这条路 —— 它们此前只在单测里被走到。
//
// ⚠️ 手续费由本库自己算（fee.Compute，基准是昨结算价），
// **不抄**柜台委托记录里的 frozen_commission —— 抄了就是同义反复：
// 两侧变成同一个数，而一次同义反复的对拍与一次真的对拍，
// 在汇总行里长得一模一样。
//
// ⚠️ 平仓单不冻保证金（20260909 实测：账户 frozen_margin 在只挂平仓单时
// 恒为 0，且委托记录上这个字段**整个缺席**——404/404，kq_facts 49）。
// 开仓单要冻，基准是**昨结算价**不是报单价，见 openFrozenMargin。
//
// ⚠️ 这段注释原先写着「本函数**遇到开仓挂单就报错** …… 本批样本里一笔
// 开仓挂单都没有，没见过的情形不猜」。同日凌晨挂上开仓单之后那一支实现了，
// 而**这句话在代码改完之后还在这里留了几个小时** ——
// 一条描述已经不成立的注释，与一条正确的注释在代码上长得一模一样。
func FrozenAccountOf(f *Fixture, specs map[string]Spec) (order.Frozen, error) {
	zero := order.Frozen{Margin: decimal.Zero, Commission: decimal.Zero}
	live, err := liveOrders(f)
	if err != nil {
		return zero, err
	}
	book := order.NewBook()
	for id, o := range live {
		left, _ := numberOf(o, "volume_left")
		sym, ok := textOf(o, "exchange_id", "instrument_id")
		if !ok {
			return zero, fmt.Errorf("委托 %s 读不出合约", id)
		}
		dir, off, err := dirOffsetOf(o)
		if err != nil {
			return zero, fmt.Errorf("委托 %s：%w", id, err)
		}
		spec, ok := specs[sym]
		if !ok {
			return zero, fmt.Errorf("委托 %s 的合约 %s 没有规格", id, sym)
		}
		inst, err := types.ParseSymbol(sym, f.TradingDay)
		if err != nil {
			return zero, fmt.Errorf("委托 %s 的合约 %s：%w", id, sym, err)
		}
		pre, ok := f.PreSettlement(sym)
		if !ok {
			return zero, fmt.Errorf("委托 %s 的合约 %s 没有昨结算价 —— 手续费基准缺失", id, sym)
		}
		m := decimal.Zero
		feeOffset := off
		if off == types.Open {
			// ⚠️ 开仓挂单**要冻保证金**，基准是**昨结算价**，不是报单价。
			// 20260909 两次独立实测（见 openFrozenMargin 的注释）：
			// 两笔的报单价都远低于昨结算价，所以这两者被分得干干净净。
			m, err = openFrozenMargin(sym, inst, dir, spec, pre, int(left.IntPart()))
			if err != nil {
				return zero, fmt.Errorf("委托 %s：%w", id, err)
			}
		} else if off == types.Close {
			// 裸 CLOSE 在金额上与平昨等价（手续费一样收、保证金一样不冻），
			// 所以这里当平昨算是安全的 —— 而**手数**那一侧不是，见 FrozenOf。
			feeOffset = types.CloseYesterday
		}
		c, err := fee.Compute(spec.Commission, feeOffset, pre, spec.Multiplier,
			int(left.IntPart()), decimalx.NoRounding)
		if err != nil {
			return zero, fmt.Errorf("委托 %s 算手续费：%w", id, err)
		}
		req := order.Request{Instrument: inst, Direction: dir, Offset: feeOffset,
			Hedge: types.Speculation, Price: decimal.Zero, Volume: int(left.IntPart())}
		if err := book.Insert(id, req, order.FreezeInput{Margin: m, Commission: c}); err != nil {
			return zero, fmt.Errorf("委托 %s 进簿：%w", id, err)
		}
	}
	return book.Total(), nil
}

// openFrozenMargin 算一笔**开仓挂单**冻结的保证金。
//
// # 实测（20260909，快期模拟）
//
//	SHFE.ag2702  昨结 16262 × 乘数 15  × 22% = 53664.6   账户 frozen_margin 53664.6
//	DCE.i2701    昨结 740   × 乘数 100 × 11% = 8140      账户 frozen_margin 8140
//
// ⚠️ 两笔的**报单价**分别是 13009.9 与 673.95，都远低于昨结算价 ——
// 于是「按昨结算价」与「按报单价」被分得干干净净：后者会算出
// 42932.67 与 7413.45，与账户对不上。**这不是一次同值的巧合。**
//
// ⚠️ 它与持仓保证金同基准（kq_facts 1），但那是两件事：
// 一个是挂单占用、一个是持仓占用，同基准是**量出来的**，不是推出来的。
//
// ⚠️ 走 margin.Compute 而不是就地写「名义额 × 费率」：那会是第二份实现，
// 而两份实现里只有一份会被对拍走到。
func openFrozenMargin(sym string, inst types.InstrumentID, dir types.Direction,
	spec Spec, pre decimal.Decimal, volume int) (decimal.Decimal, error) {

	leg := margin.Leg{
		Instrument: inst, Direction: dir, Volume: volume,
		Multiplier: spec.Multiplier, Rates: spec.Margin,
		// ⚠️ 挂单还没成交，没有「今昨」可言 —— 记作今仓那一档。
		// 昨仓档只在两档费率不同的合约上才有差别，而本口子上四档同值
		// （probes.md §7.2），所以这一步在本口子上**没有判别力**，
		// 换口子时要重新想。
		// ⚠️ MaxMarginSide 写死为 false、scope 取 NoNetting：单腿算不出大边，
		// 而**假装能算**会让这里在多空并存时悄悄少冻一边。真要建模挂单与持仓
		// 之间的大边合并，得把持仓一起传进来 —— 那是另一件事，本函数不做。
		IsHistory: false, MaxMarginSide: false,
		PreSettlement: pre, HasPreSettlement: true,
	}
	res, err := margin.Compute([]margin.Leg{leg}, margin.PreSettleAll, margin.NoNetting)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s 算开仓冻结保证金：%w", sym, err)
	}
	return res.Company, nil
}

// liveOrders 挑出**还挂着且还有未成交量**的委托。
//
// ⚠️ 单独一个函数是为了让「哪些委托要算冻结」只有一份定义。
// 调用方（对拍要先凑齐这些合约的规格）与 FrozenAccountOf 各写一遍的话，
// 两份筛法一旦分岔，表现是**对拍悄悄少比几份夹具** —— 不报错，只是覆盖变小。
func liveOrders(f *Fixture) (map[string]map[string]Value, error) {
	out := map[string]map[string]Value{}
	for id, o := range f.Orders {
		alive, ok := aliveOf(o)
		if !ok {
			return nil, fmt.Errorf("委托 %s 没有 status 字段 —— "+
				"读不到不当成「挂着」也不当成「终结」", id)
		}
		if !alive {
			continue
		}
		left, ok := numberOf(o, "volume_left")
		if !ok || !left.IsPositive() {
			continue
		}
		out[id] = o
	}
	return out, nil
}

// LiveOrderSymbols 列出 FrozenAccountOf 会用到规格的那些合约，升序。
func LiveOrderSymbols(f *Fixture) ([]string, error) {
	live, err := liveOrders(f)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for id, o := range live {
		sym, ok := textOf(o, "exchange_id", "instrument_id")
		if !ok {
			return nil, fmt.Errorf("委托 %s 读不出合约", id)
		}
		if !seen[sym] {
			seen[sym] = true
			out = append(out, sym)
		}
	}
	sort.Strings(out)
	return out, nil
}
