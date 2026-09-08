package live

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// fromFloat 把上游的 JSON 数字转成 decimal。
//
// ⚠️ 用 NewFromFloat 而不是 NewFromFloat32 或字符串拼接：
// shopspring 的 NewFromFloat 取「能可靠往返的最少有效位」，
// 于是 0.2 得到 "0.2" 而不是 0.200000000000000011…
//
// ⚠️ 但这掩盖不了一件事：**上游给的就是 float64**。
// 字典里写着 `"commission": 31.477800000000002`、`"margin": 164231.99999999997`——
// 那两个字段本包不取（见 Symbol 的注释）。这里取的是乘数与最小变动价位，
// 它们在实际数据里都是位数很少的数（10 / 300 / 0.2 / 0.5），往返是安全的。
// **安全的理由是数据的形态，不是转换函数的承诺**，所以下面还有一道校验。
func fromFloat(f float64, field, who string) (decimal.Decimal, error) {
	d := decimal.NewFromFloat(f)
	// ⚠️ 往返校验：转回 float64 必须逐位相同。
	// 这一条在正常数据上恒真，它防的是「哪天上游开始给高精度小数」——
	// 那时静默截断会让最小变动价位差一点点，而 tick 差一点点意味着
	// 报价校验会在极少数价位上放过本该被拒的单。
	if back, _ := d.Float64(); back != f {
		return decimal.Zero, fmt.Errorf("%s 的 %s = %v 转 decimal 后往返不一致（得到 %s）—— "+
			"上游可能开始给高精度小数了，此处不许静默截断", who, field, f, d)
	}
	return d, nil
}

// ToInstrument 把字典条目转成本库的合约规格。
//
// ⚠️ 三个字段**转不出来**，本函数不猜：
//
//	PositionDateType        字典里没有；它决定报单必须不必声明平今平昨
//	MaxMarginSide           字典里没有；它决定单向大边
//	PriceLimitRatio         字典里没有涨跌幅比例
//
// 这三个都是**规则数据**，来源是 CTP 的合约表，不是行情商的字典。
// 所以本函数返回的 Instrument 是**不完整的**，调用方必须补齐后才能进 Builder——
// 而 Builder 的零值报错会在没补齐时拦住它。⚠️ 这是刻意的：
// 一个「差不多能用」的合约规格，会在平今平昨和大边上静默算错。
func ToInstrument(s Symbol) (refdata.Instrument, error) {
	if err := s.Validate(); err != nil {
		return refdata.Instrument{}, err
	}
	if s.Class != "" && s.Class != "FUTURE" {
		return refdata.Instrument{}, fmt.Errorf("合约 %s 的 class 是 %q，本库目前只处理 FUTURE",
			s.InstrumentID, s.Class)
	}
	id, err := types.ParseSymbol(s.InstrumentID, 0)
	if err != nil {
		return refdata.Instrument{}, fmt.Errorf("合约代码 %q 解析失败：%w", s.InstrumentID, err)
	}
	mult, err := fromFloat(s.VolumeMultiple, "volume_multiple", s.InstrumentID)
	if err != nil {
		return refdata.Instrument{}, err
	}
	tick, err := fromFloat(s.PriceTick, "price_tick", s.InstrumentID)
	if err != nil {
		return refdata.Instrument{}, err
	}
	return refdata.Instrument{
		ID:                  id,
		VolumeMultiple:      mult,
		PriceTick:           tick,
		IsTrading:           !s.Expired,
		MinLimitOrderVolume: s.MinLimitVolume,
		MaxLimitOrderVolume: s.MaxLimitVolume,
		// PositionDateType / MaxMarginSide / PriceLimitRatio 刻意留零值。
		// ⚠️ 零值在 refdata 里是「规则数据缺失，使用即报错」，正是这里要的。
	}, nil
}

// ToSessionTable 把字典条目的 trading_time 转成本库的时段表。
//
// ⚠️ 这是本包真正的价值：`Calendar` 的时段表此前是照文档填的**猜测**，
// 而这份字典给的是行情商实际在用的时段。
func ToSessionTable(s Symbol) (refdata.SessionTable, error) {
	if err := s.Validate(); err != nil {
		return refdata.SessionTable{}, err
	}
	// ⚠️ class 必须查，而且这不是形式：**期权合约的代码以同一个品种前缀开头**
	// （`SHFE.rb2701C3000` 与 `SHFE.rb2701` 前缀相同），
	// 于是按代码前缀筛出来的集合里会混进期权。
	// 期权带着自己的时段表进汇总时，「同品种内一致」那道检查未必抓得住 ——
	// 它们的 product_id 可能相同，也可能不同，两种都会出问题：
	// 相同则被当成同品种的冲突（错误的原因），不同则悄悄多出一个品种。
	if s.Class != "FUTURE" {
		return refdata.SessionTable{}, fmt.Errorf("合约 %s 的 class 是 %q，本函数只处理 FUTURE —— "+
			"按代码前缀筛选会把期权一起带进来（%s 与期货前缀相同）", s.InstrumentID, s.Class, s.InstrumentID)
	}
	ex, _, ok := strings.Cut(s.InstrumentID, ".")
	if !ok {
		return refdata.SessionTable{}, fmt.Errorf("合约代码 %q 里没有交易所前缀", s.InstrumentID)
	}
	t := refdata.SessionTable{Exchange: types.Exchange(ex), Product: s.ProductID}
	for _, g := range []struct {
		name string
		src  [][]string
		dst  *[]refdata.Session
	}{{"day", s.TradingTime.Day, &t.Day}, {"night", s.TradingTime.Night, &t.Night}} {
		for i, pair := range g.src {
			if len(pair) != 2 {
				return refdata.SessionTable{}, fmt.Errorf("%s 的 %s 第 %d 段不是 [起, 止] 两项，是 %d 项",
					s.InstrumentID, g.name, i+1, len(pair))
			}
			start, err := parseClock(pair[0])
			if err != nil {
				return refdata.SessionTable{}, fmt.Errorf("%s 的 %s 第 %d 段起点：%w",
					s.InstrumentID, g.name, i+1, err)
			}
			end, err := parseClock(pair[1])
			if err != nil {
				return refdata.SessionTable{}, fmt.Errorf("%s 的 %s 第 %d 段终点：%w",
					s.InstrumentID, g.name, i+1, err)
			}
			*g.dst = append(*g.dst, refdata.Session{Start: start, End: end})
		}
	}
	// ⚠️ 走本库自己的校验，不因为「上游给的」就跳过。
	// 上游的时段表同样可能重叠或越界，而重叠时「一个时刻落在哪一段」有两个答案。
	if err := t.Validate(); err != nil {
		return refdata.SessionTable{}, fmt.Errorf("上游给的时段表过不了本库校验：%w", err)
	}
	return t, nil
}

func parseClock(s string) (refdata.ClockTime, error) {
	var h, m, sec int
	n, err := fmt.Sscanf(s, "%d:%d:%d", &h, &m, &sec)
	if err != nil || n != 3 {
		return 0, fmt.Errorf("时刻 %q 不是 hh:mm:ss", s)
	}
	return refdata.NewClockTime(h, m, sec)
}

// SessionTablesOf 从一批条目里按**品种**汇总时段表，并检查同品种内是否一致。
//
// ⚠️ 同一品种的不同月份合约，时段表应当相同。若不同，那要么是上游数据有问题，
// 要么是本库「时段按品种」这个假设错了 —— 两种都必须被人看见，不能自动取第一个。
func SessionTablesOf(syms map[string]Symbol) ([]refdata.SessionTable, error) {
	byProduct := map[string]refdata.SessionTable{}
	source := map[string]string{}
	var conflicts []string
	keys := make([]string, 0, len(syms))
	for k := range syms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	skipped := map[string]int{}
	for _, k := range keys {
		// ⚠️ 非 FUTURE 的**显式跳过并计数**，不是静默丢掉。
		// 计数会打给调用方，因为「筛出来的东西里混了多少期权」本身是个信号：
		// 它说明代码前缀这个筛法有多粗。
		if c := syms[k].Class; c != "FUTURE" {
			skipped[c]++
			continue
		}
		t, err := ToSessionTable(syms[k])
		if err != nil {
			return nil, err
		}
		pk := string(t.Exchange) + "." + t.Product
		prev, seen := byProduct[pk]
		if !seen {
			byProduct[pk] = t
			source[pk] = k
			continue
		}
		if !sameTable(prev, t) {
			conflicts = append(conflicts, fmt.Sprintf("%s：%s 与 %s 的时段表不同", pk, source[pk], k))
		}
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("同品种内时段表不一致，**不自动取第一个**：%s —— "+
			"要么上游数据有问题，要么「时段按品种」这个假设错了，两种都得人看",
			strings.Join(conflicts, "；"))
	}
	if len(byProduct) == 0 {
		return nil, fmt.Errorf("一个 FUTURE 合约都没有（按 class 跳过了 %v）—— "+
			"筛出来的可能全是期权或合成指数", skipped)
	}
	out := make([]refdata.SessionTable, 0, len(byProduct))
	pks := make([]string, 0, len(byProduct))
	for pk := range byProduct {
		pks = append(pks, pk)
	}
	sort.Strings(pks)
	for _, pk := range pks {
		out = append(out, byProduct[pk])
	}
	return out, nil
}

func sameTable(a, b refdata.SessionTable) bool {
	same := func(x, y []refdata.Session) bool {
		if len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
		return true
	}
	return a.Exchange == b.Exchange && a.Product == b.Product &&
		same(a.Day, b.Day) && same(a.Night, b.Night)
}
