package refdata

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// Evidence 是一份快照里数据的证据等级。
//
// ⚠️ 它跟着**数据**走，不跟着产生数据的那次实测走。
// 一个取值离开它被量出来的那次实验之后，仍然要带着「它是从哪来的」——
// 否则「快期模拟的取值」会在几次传递之后被读成「规则如此」，
// 而快期已经量到至少一条**已知偏离真实 CTP** 的取值（按额手续费的基准价）。
type Evidence string

const (
	// EvidencePlaceholder 是占位数据：结构完整但内容不可用。
	//
	// ⚠️ 这一档存在的理由：一份空快照如果能加载成功，
	// 调用方拿到的会是「查不到这个合约」，而不是「本库还没有参考数据」。
	// 两者在代码里长得一样，在排查时差得很远。
	EvidencePlaceholder Evidence = "placeholder"
	// EvidenceDocumented 来自交易所公告 / CTP 接口定义，未在柜台上复核。
	EvidenceDocumented Evidence = "documented"
	// EvidenceMeasuredKQ 来自快期模拟实测。⚠️ 不等于「规则如此」。
	EvidenceMeasuredKQ Evidence = "measured-kq"
	// EvidenceMeasuredCTP 来自 CTP 柜台实测。
	EvidenceMeasuredCTP Evidence = "measured-ctp"
)

// Valid 报告证据等级是不是已知的那四档之一。
func (e Evidence) Valid() bool {
	switch e {
	case EvidencePlaceholder, EvidenceDocumented, EvidenceMeasuredKQ, EvidenceMeasuredCTP:
		return true
	}
	return false
}

// —— 线格式 ——
//
// ⚠️ 所有小数一律是**字符串**。
// 经一次 float64 往返之后 0.07 会变成 0.07000000000000001：它不报错，
// 只让每一笔保证金差一个极小的量，而那个量逐日累加进结存。
// 本项目已经量到过这个形状（夹具里的 commission: 126.95459999999999），
// 持久化是本库自己能控制的那一段，没有理由在这里再引入一次。

type wireMarginRates struct {
	LongByMoney   string `json:"long_by_money"`
	LongByVolume  string `json:"long_by_volume"`
	ShortByMoney  string `json:"short_by_money"`
	ShortByVolume string `json:"short_by_volume"`

	// ⚠️ CompanyAddOn 必须序列化。漏掉它，一份带公司加收的快照往返之后加收变 0，
	// 而那个方向正是**低估保证金占用** —— MarginRates 的注释里明写着
	// 「默认 0 意味着公司口径 = 交易所口径，这会低估」。
	// 一个静默的低估，在回测里表现为「比真实账户能开更多仓」。
	CompanyAddOn string `json:"company_add_on"`
}

type wireCommissionRates struct {
	OpenByMoney        string `json:"open_by_money"`
	OpenByVolume       string `json:"open_by_volume"`
	CloseByMoney       string `json:"close_by_money"`
	CloseByVolume      string `json:"close_by_volume"`
	CloseTodayByMoney  string `json:"close_today_by_money"`
	CloseTodayByVolume string `json:"close_today_by_volume"`
}

// ⚠️ 合约 ID 存**四个分量**，不存 "SHFE.rb2701" 那种拼好的串。
//
// 拼好的串反解时需要一个 `asOf` 来定世纪（2701 是 2027 还是 1927），
// 而**一个需要消歧参数才能往返的格式，就是一个会丢信息的格式**。
// 分量式没有这个问题，代价只是多三个字段。
type wireInstrument struct {
	Exchange         string `json:"exchange"`
	Product          string `json:"product"`
	Year             int    `json:"year"`  // 四位
	Month            int    `json:"month"` // 1..12
	VolumeMultiple   string `json:"volume_multiple"`
	PriceTick        string `json:"price_tick"`
	PositionDateType string `json:"position_date_type"`
	MaxMarginSide    bool   `json:"max_margin_side"`
	ExpireDate       int32  `json:"expire_date"`
	IsTrading        bool   `json:"is_trading"`
	MinLimitVolume   int    `json:"min_limit_order_volume"`
	MaxLimitVolume   int    `json:"max_limit_order_volume"`

	// ⚠️ PriceLimitRatio 与 HasPriceLimitRatio **必须成对**序列化。
	//
	// 漏掉那个布尔，「比例是零」与「不知道比例」就压成了同一个值 ——
	// 而前者意味着不许波动，后者意味着本库要跳过涨跌停校验并给出原因。
	// 这一对存在的全部理由就是把这两者分开，线格式上再合并回去等于白设。
	PriceLimitRatio    string `json:"price_limit_ratio"`
	HasPriceLimitRatio bool   `json:"has_price_limit_ratio"`
}

type wireRateEntry struct {
	Exchange string `json:"exchange"`
	Product  string `json:"product"`
	Year     int    `json:"year"`
	Month    int    `json:"month"`
	// Hedge 用稳定的 ASCII 令牌，不用 String() 的中文显示串。
	// ⚠️ 显示文案会变，而且 "未知" 往返回来是什么谁也说不准。
	Hedge      string               `json:"hedge_flag"`
	Margin     *wireMarginRates     `json:"margin,omitempty"`
	Commission *wireCommissionRates `json:"commission,omitempty"`
}

type wireSession struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type wireSessionTable struct {
	Exchange string        `json:"exchange"`
	Product  string        `json:"product"`
	Day      []wireSession `json:"day"`
	Night    []wireSession `json:"night,omitempty"`
}

type wireCalendar struct {
	Days           []int32            `json:"trading_days"`
	Tables         []wireSessionTable `json:"session_tables"`
	NightSuspended []int32            `json:"night_suspended,omitempty"`
}

type wireSnapshot struct {
	Version     int64            `json:"version"`
	Evidence    Evidence         `json:"evidence"`
	Source      string           `json:"source"`
	GeneratedAt string           `json:"generated_at"`
	Instruments []wireInstrument `json:"instruments"`
	Rates       []wireRateEntry  `json:"rates"`
	Calendar    *wireCalendar    `json:"calendar,omitempty"`
}

// Save 把快照写成 JSON。
//
// ⚠️ 输出是**确定性**的：所有列表按键排序。
// 否则同一份数据两次生成会产生不同的字节，而内置快照是要进 git 的——
// 一个每次生成都变的文件，会让「这次快照到底改了什么」无从查起。
// ⚠️ generatedAt 由调用方传入，函数内部**不调用 time.Now()**。
//
// 藏一个 time.Now() 在序列化里，会让「同一份数据两次生成是否产生同样的字节」
// 这个问题**无法被测试回答**——而那正是内置快照能进 git 的前提。
// 把时钟移到参数上，确定性就成了一条可以断言的性质。
func (s *Snapshot) Save(w io.Writer, ev Evidence, source string, generatedAt time.Time, cal *Calendar) error {
	if !ev.Valid() {
		return fmt.Errorf("证据等级 %q 不是已知的四档之一 —— 不许自造", ev)
	}
	out := wireSnapshot{
		Version:     s.Version(),
		Evidence:    ev,
		Source:      source,
		GeneratedAt: generatedAt.In(cnZone).Format(time.RFC3339),
	}
	ids := make([]types.InstrumentID, 0, len(s.instruments))
	for _, inst := range s.instruments {
		ids = append(ids, inst.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].Canonical() < ids[j].Canonical() })

	for _, id := range ids {
		inst, err := s.Instrument(id)
		if err != nil {
			return err
		}
		out.Instruments = append(out.Instruments, wireInstrument{
			Exchange: string(inst.ID.Exchange), Product: inst.ID.Product,
			Year: inst.ID.Year, Month: inst.ID.Month,
			VolumeMultiple: inst.VolumeMultiple.String(), PriceTick: inst.PriceTick.String(),
			PositionDateType: posDateToken(inst.PositionDateType), MaxMarginSide: inst.MaxMarginSide,
			ExpireDate: int32(inst.ExpireDate), IsTrading: inst.IsTrading,
			MinLimitVolume: inst.MinLimitOrderVolume, MaxLimitVolume: inst.MaxLimitOrderVolume,
			PriceLimitRatio: inst.PriceLimitRatio.String(), HasPriceLimitRatio: inst.HasPriceLimitRatio,
		})
		for _, hedge := range []types.HedgeFlag{types.Speculation, types.Hedge, types.Arbitrage} {
			e := wireRateEntry{Exchange: string(inst.ID.Exchange), Product: inst.ID.Product,
				Year: inst.ID.Year, Month: inst.ID.Month, Hedge: hedgeToken(hedge)}
			if m, err := s.MarginRates(id, hedge); err == nil {
				e.Margin = &wireMarginRates{m.LongByMoney.String(), m.LongByVolume.String(),
					m.ShortByMoney.String(), m.ShortByVolume.String(), m.CompanyAddOn.String()}
			}
			if c, err := s.CommissionRates(id, hedge); err == nil {
				e.Commission = &wireCommissionRates{c.OpenByMoney.String(), c.OpenByVolume.String(),
					c.CloseByMoney.String(), c.CloseByVolume.String(),
					c.CloseTodayByMoney.String(), c.CloseTodayByVolume.String()}
			}
			if e.Margin != nil || e.Commission != nil {
				out.Rates = append(out.Rates, e)
			}
		}
	}
	if cal != nil {
		wc := &wireCalendar{}
		for _, d := range cal.days {
			wc.Days = append(wc.Days, int32(d))
		}
		keys := make([]string, 0, len(cal.tables))
		for k := range cal.tables {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t := cal.tables[k]
			wt := wireSessionTable{Exchange: string(t.Exchange), Product: t.Product}
			for _, ss := range t.Day {
				wt.Day = append(wt.Day, wireSession{ss.Start.String(), ss.End.String()})
			}
			for _, ss := range t.Night {
				wt.Night = append(wt.Night, wireSession{ss.Start.String(), ss.End.String()})
			}
			wc.Tables = append(wc.Tables, wt)
		}
		for d := range cal.noNight {
			wc.NightSuspended = append(wc.NightSuspended, d)
		}
		sort.Slice(wc.NightSuspended, func(i, j int) bool { return wc.NightSuspended[i] < wc.NightSuspended[j] })
		out.Calendar = wc
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// —— 枚举的线格式：稳定 ASCII 令牌，未知一律拒绝 ——
//
// ⚠️ 不用 String()：那是**中文显示串**，会随文案调整而变，
// 而且它对零值返回「未知」——把「未知」写进文件再读回来，
// 得到的是一个能加载成功、使用时才报错的快照。
// 拒绝发生在**加载期**，那里还知道是哪一份文件、哪一条记录。

var (
	posDateTokens = map[PositionDateType]string{
		UseHistory: "use_history", NoUseHistory: "no_use_history",
	}
	hedgeTokens = map[types.HedgeFlag]string{
		types.Speculation: "speculation", types.Arbitrage: "arbitrage", types.Hedge: "hedge",
	}
)

func posDateToken(p PositionDateType) string { return posDateTokens[p] }
func hedgeToken(h types.HedgeFlag) string    { return hedgeTokens[h] }

func parsePosDate(tok string) (PositionDateType, error) {
	for k, v := range posDateTokens {
		if v == tok {
			return k, nil
		}
	}
	return 0, fmt.Errorf("今昨仓类型令牌 %q 不认识（认 use_history / no_use_history）—— "+
		"⚠️ 空串或未知令牌不许落成零值：零值是「规则数据缺失」，"+
		"那会变成一份能加载成功、使用时才报错的快照", tok)
}

func parseHedge(tok string) (types.HedgeFlag, error) {
	for k, v := range hedgeTokens {
		if v == tok {
			return k, nil
		}
	}
	return 0, fmt.Errorf("投机套保标志令牌 %q 不认识（认 speculation / arbitrage / hedge）", tok)
}

// LoadOptions 控制加载时的宽严。
type LoadOptions struct {
	// AllowPlaceholder 允许加载 evidence 为 placeholder 的快照。
	//
	// ⚠️ 默认为 false 是刻意的：一份占位快照如果能默默加载成功，
	// 调用方拿到的会是「查不到这个合约」，而不是「本库还没有参考数据」。
	// 要用占位数据（比如跑一个不关心费率的冒烟测试），必须**显式说出来**。
	AllowPlaceholder bool
}

// Load 从 JSON 读回快照与日历。
//
// ⚠️ 反序列化之后**重新走一遍 Builder**，不走「反正文件是我们自己写的」的快路径。
//
// 快照文件是可以手改的。若直接把字段塞进 Snapshot，Builder 在构造期的全部校验——
// 键的规范形式、费率非负、孤儿费率、缺失字段——**在加载路径上就全部失效**。
// 这与「两个实现一起退化」是同一个形状，只是两条路径一条是构造、一条是加载。
func Load(r io.Reader, opt LoadOptions) (*Snapshot, *Calendar, Evidence, error) {
	var in wireSnapshot
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields() // ⚠️ 多出来的字段是格式漂移，不是可以忽略的噪声
	if err := dec.Decode(&in); err != nil {
		return nil, nil, "", fmt.Errorf("快照解析失败：%w", err)
	}
	if !in.Evidence.Valid() {
		return nil, nil, "", fmt.Errorf("快照的证据等级 %q 不是已知的四档之一", in.Evidence)
	}
	if in.Evidence == EvidencePlaceholder && !opt.AllowPlaceholder {
		return nil, nil, "", fmt.Errorf("这是一份**占位**快照（evidence=placeholder），" +
			"默认不许加载 —— 否则调用方拿到的会是「查不到这个合约」，" +
			"而不是「本库还没有参考数据」。确实要用请设 LoadOptions.AllowPlaceholder")
	}
	if in.Version == 0 {
		return nil, nil, "", fmt.Errorf("快照没有版本号 —— 版本是对拍时定位「用的哪一份数据」的唯一锚点")
	}

	b := NewBuilder(in.Version)
	idOf := func(ex, product string, year, month int) (types.InstrumentID, error) {
		if product == "" || year < 1000 || month < 1 || month > 12 {
			return types.InstrumentID{}, fmt.Errorf("合约分量非法：%s.%s %d-%d", ex, product, year, month)
		}
		return types.InstrumentID{Exchange: types.Exchange(ex), Product: product,
			Year: year, Month: month}, nil
	}
	num := func(field, v string) (decimal.Decimal, error) {
		d, err := decimal.NewFromString(v)
		if err != nil {
			return decimal.Zero, fmt.Errorf("字段 %s 的值 %q 不是数：%w", field, v, err)
		}
		return d, nil
	}

	for i, w := range in.Instruments {
		id, err := idOf(w.Exchange, w.Product, w.Year, w.Month)
		if err != nil {
			return nil, nil, "", fmt.Errorf("第 %d 个合约：%w", i+1, err)
		}
		pdt, err := parsePosDate(w.PositionDateType)
		if err != nil {
			return nil, nil, "", fmt.Errorf("合约 %s：%w", id.Canonical(), err)
		}
		mult, err := num("volume_multiple", w.VolumeMultiple)
		if err != nil {
			return nil, nil, "", fmt.Errorf("合约 %s：%w", id.Canonical(), err)
		}
		tick, err := num("price_tick", w.PriceTick)
		if err != nil {
			return nil, nil, "", fmt.Errorf("合约 %s：%w", id.Canonical(), err)
		}
		ratio, err := num("price_limit_ratio", w.PriceLimitRatio)
		if err != nil {
			return nil, nil, "", fmt.Errorf("合约 %s：%w", id.Canonical(), err)
		}
		b.AddInstrument(Instrument{
			ID: id, VolumeMultiple: mult, PriceTick: tick,
			PositionDateType: pdt, MaxMarginSide: w.MaxMarginSide,
			ExpireDate: types.TradingDay(w.ExpireDate), IsTrading: w.IsTrading,
			MinLimitOrderVolume: w.MinLimitVolume, MaxLimitOrderVolume: w.MaxLimitVolume,
			PriceLimitRatio: ratio, HasPriceLimitRatio: w.HasPriceLimitRatio,
		})
	}

	for i, w := range in.Rates {
		id, err := idOf(w.Exchange, w.Product, w.Year, w.Month)
		if err != nil {
			return nil, nil, "", fmt.Errorf("第 %d 条费率：%w", i+1, err)
		}
		hedge, err := parseHedge(w.Hedge)
		if err != nil {
			return nil, nil, "", fmt.Errorf("第 %d 条费率（%s）：%w", i+1, id.Canonical(), err)
		}
		if m := w.Margin; m != nil {
			var vals [5]decimal.Decimal
			for j, pair := range [][2]string{{"long_by_money", m.LongByMoney},
				{"long_by_volume", m.LongByVolume}, {"short_by_money", m.ShortByMoney},
				{"short_by_volume", m.ShortByVolume}, {"company_add_on", m.CompanyAddOn}} {
				if vals[j], err = num(pair[0], pair[1]); err != nil {
					return nil, nil, "", fmt.Errorf("%s 保证金率：%w", id.Canonical(), err)
				}
			}
			b.AddMarginRates(id, hedge, MarginRates{vals[0], vals[1], vals[2], vals[3], vals[4]})
		}
		if c := w.Commission; c != nil {
			var vals [6]decimal.Decimal
			for j, pair := range [][2]string{{"open_by_money", c.OpenByMoney},
				{"open_by_volume", c.OpenByVolume}, {"close_by_money", c.CloseByMoney},
				{"close_by_volume", c.CloseByVolume}, {"close_today_by_money", c.CloseTodayByMoney},
				{"close_today_by_volume", c.CloseTodayByVolume}} {
				if vals[j], err = num(pair[0], pair[1]); err != nil {
					return nil, nil, "", fmt.Errorf("%s 手续费率：%w", id.Canonical(), err)
				}
			}
			b.AddCommissionRates(id, hedge, CommissionRates{vals[0], vals[1], vals[2],
				vals[3], vals[4], vals[5]})
		}
	}

	snap, err := b.Build()
	if err != nil {
		return nil, nil, "", fmt.Errorf("快照过不了构造期校验（加载路径与构造路径走同一套）：%w", err)
	}

	var cal *Calendar
	if in.Calendar != nil {
		days := make([]types.TradingDay, 0, len(in.Calendar.Days))
		for _, d := range in.Calendar.Days {
			days = append(days, types.TradingDay(d))
		}
		tables := make([]SessionTable, 0, len(in.Calendar.Tables))
		for _, wt := range in.Calendar.Tables {
			t := SessionTable{Exchange: types.Exchange(wt.Exchange), Product: wt.Product}
			for _, group := range []struct {
				src []wireSession
				dst *[]Session
			}{{wt.Day, &t.Day}, {wt.Night, &t.Night}} {
				for _, ws := range group.src {
					st, err := parseClock(ws.Start)
					if err != nil {
						return nil, nil, "", fmt.Errorf("%s.%s 时段起点：%w", wt.Exchange, wt.Product, err)
					}
					en, err := parseClock(ws.End)
					if err != nil {
						return nil, nil, "", fmt.Errorf("%s.%s 时段终点：%w", wt.Exchange, wt.Product, err)
					}
					*group.dst = append(*group.dst, Session{st, en})
				}
			}
			tables = append(tables, t)
		}
		if cal, err = NewCalendar(days, tables, in.Calendar.NightSuspended); err != nil {
			return nil, nil, "", fmt.Errorf("日历过不了构造期校验：%w", err)
		}
	}
	return snap, cal, in.Evidence, nil
}

// parseClock 读 "21:00:00" 这样的时刻。越界与格式错都报错，不补零也不取模。
func parseClock(s string) (ClockTime, error) {
	var h, m, sec int
	n, err := fmt.Sscanf(s, "%d:%d:%d", &h, &m, &sec)
	if err != nil || n != 3 {
		return 0, fmt.Errorf("时刻 %q 不是 hh:mm:ss", s)
	}
	return NewClockTime(h, m, sec)
}
