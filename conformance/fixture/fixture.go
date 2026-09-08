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

// Fixture 是一份读进来的夹具。
type Fixture struct {
	Path       string
	TradingDay types.TradingDay
	CapturedAt string
	Note       string
	Account    map[string]Value
	Positions  map[string]map[string]Value
	Trades     []Trade
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
