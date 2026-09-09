package order

import (
	"fmt"
	"sort"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Frozen 是**挂着的委托**冻结掉的东西。
//
// # ⚠️ 两侧冻结是两回事，别合并
//
//	账户侧  Margin / Commission —— 金额，从 Available 扣（kq_facts 7）
//	持仓侧  VolumeToday / VolumeHistory —— 手数，冻的是可平量（kq_facts 31）
//
// 一笔**开仓**挂单冻账户侧，一笔**平仓**挂单冻持仓侧。
// 合并成一个「冻结」会让「开仓单冻了保证金」与「平仓单冻了手数」
// 在同一个数里分不开，而两者的释放时机、影响的字段都不同。
type Frozen struct {
	// Margin 是冻结保证金。
	//
	// ⚠️ 按**昨结算价**算，不按委托价（kq_facts 8 实测：
	// 跌停挂单的冻结额等于该合约每手持仓保证金）。
	// 按委托价算会在跌停挂单上少冻一大截，而那看起来只是「便宜」。
	Margin decimal.Decimal
	// Commission 是冻结手续费。
	Commission decimal.Decimal
	// VolumeToday / VolumeHistory 是持仓侧冻结的手数，**按今昨分开**。
	//
	// ⚠️ 分开是实测要求的：20260909 在今1/昨3 的同一截面上，
	// CLOSETODAY 冻 volume_long_frozen_today、CLOSE 冻 _his（kq_facts 31/32）。
	// 合并成一个数，对拍时那两个字段永远填不对。
	VolumeToday   int
	VolumeHistory int
}

// Add 把另一笔的冻结累加进来。
func (f Frozen) Add(o Frozen) Frozen {
	return Frozen{
		Margin:        f.Margin.Add(o.Margin),
		Commission:    f.Commission.Add(o.Commission),
		VolumeToday:   f.VolumeToday + o.VolumeToday,
		VolumeHistory: f.VolumeHistory + o.VolumeHistory,
	}
}

// IsZero 报告什么都没冻。
func (f Frozen) IsZero() bool {
	return f.Margin.IsZero() && f.Commission.IsZero() &&
		f.VolumeToday == 0 && f.VolumeHistory == 0
}

// FreezeInput 是算一笔委托冻结额要用到的外部事实。
//
// ⚠️ 与 Facts 同理：金额由调用方算好传入，本包不算 ——
// 保证金要费率、手续费要六个费率，那是 margin / fee 的职责。
// 在这里重算一遍就有了第二份实现，而两份实现会在
// 「同一笔单冻了多少」上分岔且谁都不报错。
type FreezeInput struct {
	// Margin 是这笔**开仓**单要冻的保证金；平仓单应当为零。
	//
	// ⚠️ 它的基准是**昨结算价**，不是报单价 —— 20260909 两次独立实测
	// （kq_facts 46）：SHFE.ag2702 昨结 16262 × 乘数 15 × 22% = 53664.6、
	// DCE.i2701 昨结 740 × 乘数 100 × 11% = 8140，与账户 frozen_margin
	// 分毫不差；而两笔的报单价（13009.9 / 673.95）都远低于昨结算价，
	// 按报单价会算出 42932.67 与 7413.45。
	//
	// ⚠️ 写在这里是因为**按报单价算是最自然的猜法**：调用方手上正好有报单价，
	// 而昨结算价要另外去取。猜错的方向是「挂在远离市价处的单冻得太少」——
	// 那不会报错，只会让可用资金显得比实际多。
	Margin decimal.Decimal
	// Commission 是这笔单要冻的手续费。⚠️ 开平都冻：
	// 平仓一样要收手续费，而它同样在成交前就从 Available 扣。
	Commission decimal.Decimal
}

// FreezeOf 算一笔委托冻结什么。
//
// ⚠️ **开仓冻金额、平仓冻手数**，而手续费两边都冻。
//
// ⚠️ 那处曾经**未实测**的边界，20260909 有实测了：
//
//	平仓挂单的账户 frozen_margin      **0**（三份样本）—— 平仓不冻保证金
//	平仓挂单的 frozen_commission      一笔的费额        —— 平仓**冻**手续费
//
// 与本函数原来**按 CTP 模型**的实现一致（持仓的保证金本来就占着）。
// ⚠️ 记这一笔是因为「按模型实现」与「有实测支撑」是两种不同的可信度，
// 而它们在代码上长得一模一样 —— 差别只在注释里。
// 证据：testdata/probes/position-frozen-held-20260909-{7,8,9}.json，
// 对拍见 TestFrozenAccountAgainstOracle。
func FreezeOf(req Request, in FreezeInput) (Frozen, error) {
	if req.Volume <= 0 {
		return Frozen{}, fmt.Errorf("委托手数必须为正，得到 %d", req.Volume)
	}
	if in.Margin.IsNegative() || in.Commission.IsNegative() {
		return Frozen{}, fmt.Errorf("冻结额不能为负（保证金 %s、手续费 %s）",
			in.Margin, in.Commission)
	}
	f := Frozen{Commission: in.Commission}
	switch req.Offset {
	case types.Open:
		if in.Margin.IsZero() {
			// ⚠️ 开仓单冻结保证金为零是**可疑的**，不是「这笔不占保证金」。
			// 让它静默通过，会让账户的 Available 多出一大截，
			// 而回测据此开出实际开不出的仓。
			return Frozen{}, fmt.Errorf("开仓单的冻结保证金是 0 —— "+
				"⚠️ 没有哪个合约开仓不占保证金；0 只可能是**没算**的伪装。"+
				"若确实要建模零保证金，请在调用方显式说明（%s %d 手）",
				req.Instrument.Canonical(), req.Volume)
		}
		f.Margin = in.Margin
	case types.CloseToday, types.CloseYesterday:
		// ⚠️ 平仓单传进来一个非零保证金是**调用方算错了**，要报错而不是忽略。
		//
		// 忽略它的代价不在这一笔上，在**测不出来**：忽略与「冻了但恰好是零」
		// 给出同一个结果，于是「平仓不冻保证金」这条性质永远没被验证过。
		// 破坏验证当场撞到这一点 —— 把 `f.Margin = in.Margin` 加进平仓分支，
		// 测试照样绿，因为用例传的本来就是零。
		if !in.Margin.IsZero() {
			return Frozen{}, fmt.Errorf("平仓单带着 %s 的冻结保证金 —— "+
				"⚠️ 持仓的保证金本来就占着，平仓不额外冻。"+
				"非零多半是调用方把开仓的算法用到了平仓上（%s %d 手）",
				in.Margin, req.Instrument.Canonical(), req.Volume)
		}
		if req.Offset == types.CloseToday {
			f.VolumeToday = req.Volume
		} else {
			f.VolumeHistory = req.Volume
		}
	case types.Close:
		// ⚠️ 裸 CLOSE 在本库是**被拒**的（见 checkClosable）。
		// 走到这里说明调用方绕过了校验 —— 报错而不是猜一边冻。
		return Frozen{}, fmt.Errorf("裸 CLOSE 的冻结算不出来：" +
			"不知道该冻今仓还是昨仓。⚠️ 本库对裸 CLOSE 报错" +
			"（simnow_pending#1 未裁决），走到这里说明校验被绕过了")
	default:
		return Frozen{}, fmt.Errorf("开平标志 %v 不认识", req.Offset)
	}
	return f, nil
}

// Book 是**挂着的**委托集合。
//
// ⚠️ 它只装还没走完的委托：成交或撤单之后要 Remove。
// 「留在 Book 里的已成交委托」会让冻结额一直挂着，
// 而那个错的方向是「看起来钱更少」—— 不会爆仓，只会少开仓，
// 于是回测结果偏保守而**没有任何报错**。
type Book struct {
	live map[string]entry
}

type entry struct {
	req    Request
	frozen Frozen
}

// NewBook 建一个空委托簿。
func NewBook() *Book { return &Book{live: map[string]entry{}} }

// Insert 记一笔挂着的委托。
//
// ⚠️ 同一个 id 插两次**报错**，不覆盖：覆盖会让前一笔的冻结凭空消失，
// 而账面上只表现为「可用资金多了一点」。
func (b *Book) Insert(id string, req Request, in FreezeInput) error {
	if id == "" {
		return fmt.Errorf("委托编号不能为空 —— 空 id 会让两笔单互相覆盖")
	}
	if _, dup := b.live[id]; dup {
		return fmt.Errorf("委托 %s 已经在簿上 —— "+
			"⚠️ 覆盖会让前一笔的冻结凭空消失，而账面上只表现为「可用资金多了一点」", id)
	}
	f, err := FreezeOf(req, in)
	if err != nil {
		return fmt.Errorf("委托 %s：%w", id, err)
	}
	b.live[id] = entry{req, f}
	return nil
}

// Remove 把一笔委托从簿上拿掉（成交或撤单），返回它释放的冻结。
//
// ⚠️ 不在簿上时**报错**，不静默当成零：那会掩盖「同一笔被释放了两次」，
// 而第二次释放会凭空多出一份可用资金。
func (b *Book) Remove(id string) (Frozen, error) {
	e, ok := b.live[id]
	if !ok {
		return Frozen{}, fmt.Errorf("委托 %s 不在簿上 —— "+
			"⚠️ 静默当成零会掩盖「同一笔被释放两次」，"+
			"而第二次释放会凭空多出一份可用资金", id)
	}
	delete(b.live, id)
	return e.frozen, nil
}

// Total 是簿上全部委托的冻结合计。
func (b *Book) Total() Frozen {
	var t Frozen
	t.Margin, t.Commission = decimal.Zero, decimal.Zero
	for _, e := range b.live {
		t = t.Add(e.frozen)
	}
	return t
}

// TotalOf 是**某个合约某个方向**的冻结合计。
//
// ⚠️ 持仓侧的 `volume_*_frozen_*` 是逐合约逐方向的字段，
// 拿全簿的合计去填它，在多合约账户上会全错。
//
// dir 是**被平持仓的方向**，不是下单方向：SELL/CLOSETODAY 平的是多头。
func (b *Book) TotalOf(inst types.InstrumentID, dir types.Direction) Frozen {
	t := Frozen{Margin: decimal.Zero, Commission: decimal.Zero}
	for _, e := range b.live {
		if e.req.Instrument != inst {
			continue
		}
		if e.req.Offset.IsClose() && closingSide(e.req.Direction) != dir {
			continue
		}
		if e.req.Offset == types.Open && e.req.Direction != dir {
			continue
		}
		t = t.Add(e.frozen)
	}
	return t
}

// closingSide 是一笔平仓委托**平的哪一边**。
func closingSide(orderDir types.Direction) types.Direction {
	if orderDir == types.Buy {
		return types.Sell
	}
	return types.Buy
}

// Live 是簿上的委托编号，排好序。
func (b *Book) Live() []string {
	ids := make([]string, 0, len(b.live))
	for id := range b.live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
