// Package fixture 读取实测夹具，并把它变成可以逐字段对拍的两侧输入。
//
// ⚠️ 它存在的理由是**夹具在成交进来之后才是自足的**：
//
//	trades      本库的输入 —— 逐笔开平，即 position.Position 的驱动
//	positions   柜台的输出 —— 逐字段与 view.Position 比
//
// 在成交进夹具之前，一份夹具里只有柜台的输出而没有本库的输入，
// 于是「对拍」只能靠人把当时下了什么单**记在别处**（probes.md 的正文里）。
// 一份要靠旁边的散文才能读懂的证据，不能被机械地重放。
//
// # "-" 不是 0
//
// 柜台在空仓方向上给的是字符串 `"-"`，实测 188/188 无反例（probes.md §9）。
// 本包把它解析成 `Absent`，**绝不回落到 0** —— 那是一个能让对拍双向全绿的洞：
// 读成 0 则空仓字段碰巧一致，整个跳过则它们全落进「未触发」，
// 两条路互相矛盾却都不报错。
package fixture

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Value 是夹具里一个字段的值。
type Value struct {
	// Absent 表示柜台在这里给的是 "-"，即**声明此处没有值**。
	Absent bool
	// Number 是数值；Absent 为真时它没有意义。
	Number decimal.Decimal
	// Text 是字符串值（合约代码这类）。
	Text string
	// IsText 区分「字符串字段」与「数值字段」。
	IsText bool
}

// Trade 是一笔成交。
type Trade struct {
	TradeID    string
	OrderID    string
	Instrument types.InstrumentID
	Direction  types.Direction
	Offset     types.Offset
	Hedge      types.HedgeFlag
	Price      decimal.Decimal
	Volume     int
	// At 是成交时刻（纳秒）。⚠️ 它决定重放顺序，不是装饰。
	At         int64
	Commission decimal.Decimal
}

// Notify 是柜台一条通知的结构化部分。见 Fixture.Notifies。
type Notify struct {
	Type  string
	Level string
	Code  int
}

// Fixture 是一份读进来的夹具。
type Fixture struct {
	Path       string
	TradingDay types.TradingDay
	CapturedAt string
	Note       string
	Account    map[string]Value
	Positions  map[string]map[string]Value
	Trades     []Trade

	// Quotes 是被观察合约的行情快照。
	//
	// ⚠️ 它让夹具自足：昨结算价同时是手续费基准、保证金基准与逐日盯市基线，
	// 而它此前不在夹具里的任何地方 —— 拿夹具重算这三样，都要先去别处找一个数补进来，
	// 而「别处」意味着那个数不属于这份证据，它可以被换掉而没人发现。
	Quotes map[string]map[string]Value

	// Orders 是委托截面（20260909 起）；HasOrders 区分
	// 「这份夹具里没有挂着的委托」与「这份夹具根本没记委托」。
	//
	// ⚠️ 这个区分是冻结那一块能不能对拍的**前提**：
	// 两种情形下冻结量都是 0，而前者可以拿去比，后者不能 ——
	// 判成一致什么都不说明。20260909 之前的全部夹具都是后者。
	Orders    map[string]map[string]Value
	HasOrders bool

	// Notifies 是柜台推来的通知的**结构化部分**（20260909 起）。
	//
	// ⚠️ 它存在的理由是一整类拒绝**不写进委托记录**：
	// 报单到一个不存在的合约上时，柜台一个字都不写进 orders，
	// 只从这条通道回一个码。只读委托记录会把它读成「柜台没反应」。
	//
	// ⚠️ 没有文案 —— 落盘时刻意不收自由文本。HasNotifies 区分
	// 「这份夹具没记通知」与「没有通知」。
	Notifies    []Notify
	HasNotifies bool
	// SkippedTrades 记录解析不了的成交，带原因。
	//
	// ⚠️ 它不是警告而是**证据缺口**：少一笔成交，重放出来的持仓就是错的，
	// 而错的那个持仓会与柜台比出一堆看起来像真差异的差异。
	SkippedTrades []string
}

// Load 读一份夹具。
func Load(r io.Reader, path string) (*Fixture, error) {
	var raw struct {
		TradingDay string                    `json:"trading_day"`
		CapturedAt string                    `json:"captured_at"`
		Note       string                    `json:"note"`
		Account    map[string]any            `json:"account"`
		Positions  map[string]map[string]any `json:"positions"`
		Trades     map[string]map[string]any `json:"trades"`
		Quotes     map[string]map[string]any `json:"quotes"`
		// Orders 是委托截面（20260909 起）。
		//
		// ⚠️ 老夹具没有这个键，而它们**仍然是有效的证据** ——
		// 缺席解析成空 map，调用方靠 HasOrders 区分「没有委托」与「这份夹具没记委托」。
		Orders map[string]map[string]any `json:"orders"`
		// Notifies 是柜台通知的结构化部分（20260909 起）。
		//
		// ⚠️ 没有 content：那是服务器写的自由文本，落盘时刻意不收，
		// 理由见 cmd/oracle/kq 的 Fixture.Notifies 注释。
		// 于是这里能拿到的只有**码**，而拒因的文案在
		// docs/cn-futures-rules.md §9 的表里，由人工看过之后写下。
		Notifies []struct {
			Type  string `json:"type"`
			Level string `json:"level"`
			Code  int    `json:"code"`
		} `json:"notifies"`
		// Unclassified 是落盘时白名单与丢弃表都没见过的键。
		//
		// ⚠️ 它非空意味着**这份夹具丢过字段**：那些键被记了名字，值没有留下。
		// 拿这样一份夹具去对拍，缺的字段会以「本库多出字段」的形式报出来，
		// 而那指向一个错的原因。所以非空即拒读，见下。
		Unclassified []string `json:"unclassified"`
	}
	dec := json.NewDecoder(r)
	// ⚠️ 对**自己的**格式开 DisallowUnknownFields：多出来的字段是漂移信号。
	// （对上游的格式则相反，见 refdata/live 的注释。）
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("读 %s 失败：%w", path, err)
	}
	if len(raw.Unclassified) > 0 {
		return nil, fmt.Errorf("⚠️ %s 的 unclassified 非空：%v —— "+
			"这份夹具落盘时有字段既不在白名单也不在丢弃表里，值没有留下。"+
			"拿它对拍会以「本库多出字段」的形式失败，而那指向一个错的原因。"+
			"先把白名单补齐再重采，不要将就着用", path, raw.Unclassified)
	}
	day, err := types.ParseTradingDay(raw.TradingDay)
	if err != nil {
		return nil, fmt.Errorf("%s 的 trading_day：%w", path, err)
	}
	f := &Fixture{
		Path: path, TradingDay: day, CapturedAt: raw.CapturedAt, Note: raw.Note,
		Account:   map[string]Value{},
		Positions: map[string]map[string]Value{},
		Quotes:    map[string]map[string]Value{},
		Orders:    map[string]map[string]Value{},
	}
	for k, v := range raw.Account {
		val, err := toValue(v)
		if err != nil {
			return nil, fmt.Errorf("%s 的 account.%s：%w", path, k, err)
		}
		f.Account[k] = val
	}
	for sym, p := range raw.Positions {
		out := map[string]Value{}
		for k, v := range p {
			val, err := toValue(v)
			if err != nil {
				return nil, fmt.Errorf("%s 的 positions[%s].%s：%w", path, sym, k, err)
			}
			out[k] = val
		}
		f.Positions[sym] = out
	}
	for sym, q := range raw.Quotes {
		out := map[string]Value{}
		for k, v := range q {
			val, err := toValue(v)
			if err != nil {
				return nil, fmt.Errorf("%s 的 quotes[%s].%s：%w", path, sym, k, err)
			}
			out[k] = val
		}
		f.Quotes[sym] = out
	}
	// ⚠️ HasOrders 用 `raw.Orders != nil` 判，不用 `len() > 0`：
	// 一份记了委托但此刻没有挂单的夹具，与一份根本没记委托的夹具，
	// 在长度上都是 0 —— 而前者可以拿去对拍冻结（结论是「都是 0」），
	// 后者不能。这正是本字段存在的全部理由。
	f.HasOrders = raw.Orders != nil
	// ⚠️ 与 HasOrders 同理：nil 判「这份夹具没记通知」，不是「没有通知」。
	f.HasNotifies = raw.Notifies != nil
	for _, n := range raw.Notifies {
		f.Notifies = append(f.Notifies, Notify{Type: n.Type, Level: n.Level, Code: n.Code})
	}
	for id, o := range raw.Orders {
		out := map[string]Value{}
		for k, v := range o {
			val, err := toValue(v)
			if err != nil {
				return nil, fmt.Errorf("%s 的 orders[%s].%s：%w", path, id, k, err)
			}
			out[k] = val
		}
		f.Orders[id] = out
	}
	for id, t := range raw.Trades {
		tr, err := toTrade(id, t, day)
		if err != nil {
			f.SkippedTrades = append(f.SkippedTrades, fmt.Sprintf("%s：%v", id, err))
			continue
		}
		f.Trades = append(f.Trades, tr)
	}
	// ⚠️ 按成交时刻排序，同刻按 trade_id —— 重放顺序必须是确定的。
	// map 的迭代顺序是随机的，靠它重放会得到一个**每次都不同**的持仓，
	// 而其中大多数次看起来完全正常。
	sort.Slice(f.Trades, func(i, j int) bool {
		if f.Trades[i].At != f.Trades[j].At {
			return f.Trades[i].At < f.Trades[j].At
		}
		return f.Trades[i].TradeID < f.Trades[j].TradeID
	})
	sort.Strings(f.SkippedTrades)
	return f, nil
}

// dashToken 是柜台表示「此处无值」的那个字符串。
const dashToken = "-"

func toValue(v any) (Value, error) {
	switch x := v.(type) {
	case string:
		if x == dashToken {
			return Value{Absent: true}, nil
		}
		return Value{Text: x, IsText: true}, nil
	case float64:
		// ⚠️ 经 float64 之后再转 decimal 是**有损的**，但源头就是 JSON 数值，
		// 损失已经发生在夹具里了。这里不假装能补回来，只是不再引入新的损失：
		// 用 NewFromFloat 而不是 FromString(fmt.Sprint(...))。
		return Value{Number: decimal.NewFromFloat(x)}, nil
	case bool:
		return Value{}, fmt.Errorf("布尔值不该出现在业务截面里：%v", x)
	case nil:
		// ⚠️ null 与 "-" 不是一回事：前者是「键在但值是空」，
		// 本仓库至今没见过，见到了要先查清楚而不是归进 Absent。
		return Value{}, fmt.Errorf("值是 null —— 与 %q 不是一回事，先查清楚再归类", dashToken)
	}
	return Value{}, fmt.Errorf("不认识的值类型 %T（%v）", v, v)
}

// toTrade 解析一笔成交。
//
// ⚠️ asOf 不能省：两位年月的合约代码要靠它定四位年份，
// 而少了它就得猜一个「当前年」—— 那会在跨年的历史夹具上静默错十年。
func toTrade(id string, m map[string]any, asOf types.TradingDay) (Trade, error) {
	t := Trade{TradeID: id}
	inst, _ := m["instrument_id"].(string)
	ex, _ := m["exchange_id"].(string)
	if inst == "" || ex == "" {
		return t, fmt.Errorf("缺 instrument_id 或 exchange_id")
	}
	pid, err := types.ParseSymbol(ex+"."+inst, asOf)
	if err != nil {
		return t, err
	}
	t.Instrument = pid
	t.OrderID, _ = m["order_id"].(string)

	ds, _ := m["direction"].(string)
	if t.Direction, err = types.DirectionFromDIFF(ds); err != nil {
		return t, err
	}
	os_, _ := m["offset"].(string)
	if t.Offset, err = types.OffsetFromDIFF(os_); err != nil {
		return t, err
	}
	hs, _ := m["hedge_flag"].(string)
	if t.Hedge, err = types.HedgeFromDIFF(hs); err != nil {
		return t, err
	}
	p, ok := m["price"].(float64)
	if !ok {
		return t, fmt.Errorf("缺 price")
	}
	t.Price = decimal.NewFromFloat(p)
	v, ok := m["volume"].(float64)
	if !ok || v <= 0 {
		return t, fmt.Errorf("volume 缺失或非正：%v", m["volume"])
	}
	t.Volume = int(v)
	at, ok := m["trade_date_time"].(float64)
	if !ok {
		// ⚠️ 没有成交时刻就没有确定的重放顺序，而顺序错了持仓就错了。
		return t, fmt.Errorf("缺 trade_date_time —— 没有它就没有确定的重放顺序")
	}
	t.At = int64(at)
	if c, ok := m["commission"].(float64); ok {
		t.Commission = decimal.NewFromFloat(c)
	}
	return t, nil
}

// Symbols 返回夹具里出现过的合约，升序。
func (f *Fixture) Symbols() []string {
	out := make([]string, 0, len(f.Positions))
	for k := range f.Positions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TradesOf 返回某个合约的成交，保持时间序。
func (f *Fixture) TradesOf(symbol string) []Trade {
	var out []Trade
	for _, t := range f.Trades {
		if t.Instrument.Canonical() == symbol {
			out = append(out, t)
		}
	}
	return out
}

// PreSettlement 取某个合约的昨结算价。
//
// ⚠️ 第二个返回值不是可有可无的：昨结算价缺失时，
// 手续费、保证金、逐日盯市基线三处都算不出来，
// 而回落到 0 会让保证金变成 0、让手续费变成 0 —— 两个都看起来「便宜」，
// 而「便宜」在数上完全合理。
func (f *Fixture) PreSettlement(symbol string) (decimal.Decimal, bool) {
	q, ok := f.Quotes[symbol]
	if !ok {
		return decimal.Zero, false
	}
	v, ok := q["pre_settlement"]
	if !ok || v.Absent || v.IsText || !v.Number.IsPositive() {
		return decimal.Zero, false
	}
	return v.Number, true
}

// Multiplier 取某个合约的乘数。
//
// ⚠️ 同样带 ok：乘数为零会让所有金额变成 0，而 0 看起来完全合理。
func (f *Fixture) Multiplier(symbol string) (decimal.Decimal, bool) {
	q, ok := f.Quotes[symbol]
	if !ok {
		return decimal.Zero, false
	}
	v, ok := q["volume_multiple"]
	if !ok || v.Absent || v.IsText || !v.Number.IsPositive() {
		return decimal.Zero, false
	}
	return v.Number, true
}

// HasHistoryPosition 报告某个合约的持仓截面里有没有**昨仓**。
//
// ⚠️ 它是「只重放当日成交」这条路径的**适用性判据**：
//
//	柜台的成交截面按交易日重置。有昨仓时，那几手是昨天开的，
//	今天的成交里**没有任何一笔能解释它**。
//
// 于是 Replay(nil, 今日成交) 会漏掉全部昨仓，
// 而漏掉之后重放出来的持仓**看起来完全正常**，只是手数少了几手 ——
// 它会与柜台比出一堆看起来像真差异的差异。
//
// 有昨仓时正确的做法是走 Carry + ReplayFrom（上一日夹具 → 结算 → 昨仓），
// 而不是把结果将就着拿去对拍。
func (f *Fixture) HasHistoryPosition(symbol string) bool {
	p, ok := f.Positions[symbol]
	if !ok {
		return false
	}
	for _, k := range []string{"volume_long_his", "volume_short_his"} {
		v, ok := p[k]
		if ok && !v.Absent && !v.IsText && v.Number.IsPositive() {
			return true
		}
	}
	return false
}
