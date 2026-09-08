// Package exchange 从**交易所**取每日结算结果。
//
// ⚠️ 它与 refdata/live 分开成两个包，不是为了整齐，是因为**证据等级不同**：
//
//	refdata/live      行情商的合约字典 —— 派生数据，供应商加工过
//	refdata/exchange  交易所的日行情   —— 权威源，结算价的出处本身
//
// 「结算价不是行情，是交易所的结算结果」（probes.md §2）：
// 它无法从 K 线推出来，而逐日盯市的每一分钱都挂在它上面。
// 把两个来源放进同一个包，会让「这个数是谁说的」在调用处消失。
//
// # 为什么必须有它
//
// 拿柜台自己的结算价去验柜台自己的逐日盯市，是同义反复。
// 本包提供那个**独立的第二来源** —— 而它当场就有用：
// 交易日 20260907 的上期所日行情给出 rb2610/2701/2705 今结算 3100/3158/3189，
// 与快期在交易日 20260908 报的昨结算**分毫不差**（probes.md §10.2 的外推正是靠它去掉循环的）。
//
// # 目前只有上期所
//
// ⚠️ 其余五家在 probes.md §2 里探过端点、**没有实现**，其中大商所返回 412 未打通。
// 少一家就是少一家，**不拿别家的数据顶替，也不插值**。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// SHFEDailyURL 是上期所日行情的地址模板，参数是八位交易日。
const SHFEDailyURL = "https://www.shfe.com.cn/data/tradedata/future/dailydata/kx%s.dat"

// Daily 是一个合约在一个交易日上的结算结果。
type Daily struct {
	Instrument types.InstrumentID
	// Settlement 是**当日**结算价。
	Settlement decimal.Decimal
	// PreSettlement 是上一交易日结算价；HasPre 报告它在不在。
	PreSettlement decimal.Decimal
	HasPre        bool
	// Close 是**收盘价**；HasClose 报告它在不在。
	//
	// ⚠️ 它与 Settlement 是两个数，混用会静默算错：20260908 的 rb2701
	// 收盘 3177、结算 3163，差 14 —— 而逐日盯市的每一分钱都挂在结算价上。
	// 留着它是为了能**独立核对**柜台的 position_price 用的是哪一个
	// （实测：柜台用收盘价，本库用结算价，那是一条已知口子差异）。
	Close    decimal.Decimal
	HasClose bool
}

// Report 记录一次解析里发生了什么，尤其是**丢了什么**。
//
// ⚠️ 丢弃必须逐类计数并可打印。一个静默丢行的解析器，
// 会在上游改格式时给出一份**看起来完全正常、只是少了几个合约**的结果，
// 而少掉的那几个恰恰可能是要用的那几个。
type Report struct {
	TradingDay types.TradingDay
	Rows       int
	Kept       int
	// Dropped 是丢弃原因 → 条数。
	Dropped map[string]int
	// Samples 是每类丢弃的头一个例子，供人判断丢得对不对。
	Samples map[string]string
}

func (r Report) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "交易日 %s：%d 行，留下 %d 个合约", r.TradingDay, r.Rows, r.Kept)
	keys := make([]string, 0, len(r.Dropped))
	for k := range r.Dropped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&sb, "\n  丢弃 %-24s %d 条（例：%s）", k, r.Dropped[k], r.Samples[k])
	}
	return sb.String()
}

func (r *Report) drop(reason, sample string) {
	if r.Dropped == nil {
		r.Dropped = map[string]int{}
		r.Samples = map[string]string{}
	}
	r.Dropped[reason]++
	if _, ok := r.Samples[reason]; !ok {
		r.Samples[reason] = sample
	}
}

// shfeRow 是日行情里的一行。
//
// ⚠️ 价格字段声明成 json.RawMessage 而不是 float64：
// 上期所在没有结算价时给的是**空字符串**而不是 null 或 0
// （小计行、TAS 合约都是这样）。声明成 float64 会让整份文件解析失败，
// 声明成 any 再断言则会静默丢行 —— 两种都不对，要的是「看得见地丢」。
type shfeRow struct {
	ProductID     string          `json:"PRODUCTID"`
	ProductClass  string          `json:"PRODUCTCLASS"`
	DeliveryMonth string          `json:"DELIVERYMONTH"`
	Settlement    json.RawMessage `json:"SETTLEMENTPRICE"`
	PreSettlement json.RawMessage `json:"PRESETTLEMENTPRICE"`
	// Close 是**收盘价**，与结算价是两个数。
	//
	// ⚠️ 它此前被丢掉了，而丢掉它的代价在 20260909 才显出来：
	// 柜台的 `position_price` 结算后给 3177 而不是结算价 3163，
	// 「3177 是收盘价」这个说法**只有柜台自己的数据支撑** ——
	// 拿柜台的数去解释柜台自己的基线是同义反复。
	// 上期所这份文件里一直有 CLOSEPRICE，只是解析器没要。
	Close json.RawMessage `json:"CLOSEPRICE"`
}

// 丢弃原因。⚠️ 用常量而不是字面量：判据里要按原因取数，
// 而两处各写一遍字符串时，改一处就会静默地永远取到 0。
const (
	dropNonFuture   = "非期货（PRODUCTCLASS≠1）"
	dropSummary     = "汇总行（月份非数字）"
	dropNoSuffix    = "PRODUCTID 没有 _f 后缀"
	dropBadID       = "合约代码解析失败"
	dropEmptyPrice  = "结算价为空"
	dropNotNumber   = "结算价不是数"
	dropNotPositive = "结算价非正"
)

// shfeFutures 是 PRODUCTCLASS 里表示「期货」的取值。
//
// ⚠️ 实测（kx20260907.dat，332 行）：只有 "1" 与 "6" 两种，
// 而 "6" 全是 `sc_tas`（原油 TAS），它们的结算价字段是空字符串。
// 按 "1" 过滤是**正面**判据；若改成「排除 6」，将来多出一个 "7" 就会被放进来。
const shfeFutures = "1"

// ParseSHFE 解析上期所日行情。
//
// day 是**期望的**交易日：文件里的 report_date 与它不符时报错，
// ⚠️ 因为拿错一天的结算价不会有任何动静 —— 数看起来完全正常，只是错了一天。
func ParseSHFE(r io.Reader, day types.TradingDay) (map[string]Daily, Report, error) {
	rep := Report{TradingDay: day}
	var raw struct {
		ReportDate  string    `json:"report_date"`
		Instruments []shfeRow `json:"o_curinstrument"`
	}
	// ⚠️ 保住精度的是 shfeRow 里那两个 json.RawMessage（原样取字节），
	// **不是** dec.UseNumber()。
	//
	// 第一版这里写了 `dec.UseNumber()`，破坏验证当场发现删掉它没有任何测试变红 ——
	// 因为 UseNumber 只影响解进 `any` 的数值，而本结构体里一个 `any` 都没有。
	// 一个删掉也没人发现的调用就是装饰，而装饰在这里更坏：
	// 它让人以为精度已经被照顾过了，于是不再去看真正起作用的那处。
	dec := json.NewDecoder(r)
	if err := dec.Decode(&raw); err != nil {
		return nil, rep, fmt.Errorf("解析上期所日行情失败：%w", err)
	}
	if raw.ReportDate == "" {
		return nil, rep, fmt.Errorf("日行情里没有 report_date —— " +
			"⚠️ 无法确认这是哪一天的数据，而拿错一天的结算价不会有任何动静")
	}
	got, err := types.ParseTradingDay(raw.ReportDate)
	if err != nil {
		return nil, rep, fmt.Errorf("report_date %q：%w", raw.ReportDate, err)
	}
	if got != day {
		return nil, rep, fmt.Errorf("⚠️ 要的是交易日 %s，文件里的 report_date 是 %s —— "+
			"拿错一天的结算价不会有任何动静：数看起来完全正常，只是错了一天", day, got)
	}
	rep.Rows = len(raw.Instruments)
	if rep.Rows == 0 {
		return nil, rep, fmt.Errorf("日行情里一行都没有 —— 是真的空，还是拿到了别的东西？")
	}

	out := map[string]Daily{}
	for _, row := range raw.Instruments {
		pid := strings.TrimSpace(row.ProductID)
		month := strings.TrimSpace(row.DeliveryMonth)
		label := pid + "/" + month
		if row.ProductClass != shfeFutures {
			rep.drop(dropNonFuture, label)
			continue
		}
		if !allDigits(month) {
			// 小计行：DELIVERYMONTH 是「小计」，价格字段为空。
			rep.drop(dropSummary, label)
			continue
		}
		product, ok := strings.CutSuffix(pid, "_f")
		if !ok {
			// ⚠️ 报出来而不是硬切：后缀规则变了要有人知道。
			rep.drop(dropNoSuffix, label)
			continue
		}
		inst, err := types.ParseNative(types.SHFE, product+month, day)
		if err != nil {
			rep.drop(dropBadID, label+"："+err.Error())
			continue
		}
		settle, ok := numText(row.Settlement)
		if !ok {
			rep.drop(dropEmptyPrice, label)
			continue
		}
		d, err := decimal.NewFromString(settle)
		if err != nil {
			rep.drop(dropNotNumber, label+"："+settle)
			continue
		}
		if !d.IsPositive() {
			// ⚠️ 结算价为零不是「便宜」，是缺数据。放进去会让逐日盯市把整个持仓算成归零。
			rep.drop(dropNotPositive, label+"："+settle)
			continue
		}
		rec := Daily{Instrument: inst, Settlement: d}
		if pre, ok := numText(row.PreSettlement); ok {
			if p, err := decimal.NewFromString(pre); err == nil && p.IsPositive() {
				rec.PreSettlement, rec.HasPre = p, true
			}
		}
		// ⚠️ 收盘价缺席**不丢行**：它是核对用的旁证，不是结算链条的输入。
		// 让它把一整行否掉，会在上游哪天不发这个字段时，
		// 把整份结算价一起弄没 —— 而结算价才是逐日盯市真正要的。
		if c, ok := numText(row.Close); ok {
			if p, err := decimal.NewFromString(c); err == nil && p.IsPositive() {
				rec.Close, rec.HasClose = p, true
			}
		}
		key := inst.Canonical()
		if old, dup := out[key]; dup {
			return nil, rep, fmt.Errorf("⚠️ 合约 %s 在同一份文件里出现两次（%s 与 %s）—— "+
				"后一条会覆盖前一条，而覆盖是静默的", key, old.Settlement, d)
		}
		out[key] = rec
		rep.Kept++
	}
	if rep.Kept == 0 {
		// ⚠️ 「文件在」不等于「结算发生了」。
		//
		// 实测（自然日 2026-09-08 14:4x，日盘尚未收盘）：
		// kx20260908.dat **已经存在且返回 200**，332 行俱全，
		// 而 301 行的 SETTLEMENTPRICE 全是空字符串 —— 交易所盘中就发布这个文件，
		// 结算之后才把价填进去。
		//
		// 把这种情形与「解析失败」合并成一条错误，会让人去查解析器，
		// 而真正的原因是「时候未到」。所以单独判、单独说。
		if rep.Dropped[dropEmptyPrice] == rep.Rows-rep.Dropped[dropSummary]-rep.Dropped[dropNonFuture] &&
			rep.Dropped[dropEmptyPrice] > 0 {
			return nil, rep, fmt.Errorf("⚠️ 交易日 %s 的日行情**已发布但结算价全为空**（%d 行）—— "+
				"交易所盘中就发布这个文件，结算之后才填价。"+
				"这不是解析失败，是**结算尚未发生**；"+
				"它同时是一条独立的结算判据（另两条：柜台的 quotes.settlement 由 \"-\" 变成数、"+
				"以及 pre_balance 推进）",
				day, rep.Dropped[dropEmptyPrice])
		}
		return nil, rep, fmt.Errorf("一个合约都没解析出来 —— %s", rep)
	}
	return out, rep, nil
}

// numText 把 RawMessage 取成数字原文；空字符串与非数值返回 false。
func numText(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	if strings.HasPrefix(s, `"`) {
		// 上期所在没有值时给的是空字符串 ""，而不是 null 或 0。
		var str string
		if err := json.Unmarshal(raw, &str); err != nil {
			return "", false
		}
		str = strings.TrimSpace(str)
		if str == "" {
			return "", false
		}
		return str, true
	}
	return s, true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// FetchSHFE 拉取并解析上期所某个交易日的日行情。
func FetchSHFE(ctx context.Context, day types.TradingDay) (map[string]Daily, Report, error) {
	url := fmt.Sprintf(SHFEDailyURL, day.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, Report{TradingDay: day}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, Report{TradingDay: day}, fmt.Errorf("拉取 %s 失败：%w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// ⚠️ 非交易日与「接口挂了」在这里长得一样（都是 404），
		// 所以把状态码原样带出去，让调用方自己判，不在这里替它下结论。
		return nil, Report{TradingDay: day}, fmt.Errorf(
			"拉取 %s 得到状态码 %d —— ⚠️ 非交易日与接口异常在这里长得一样，"+
				"本函数不替调用方判断是哪一种", url, resp.StatusCode)
	}
	return ParseSHFE(resp.Body, day)
}

// DayStatus 是探一个自然日的结果。
type DayStatus uint8

const (
	// DayUnknown 是零值：没探。使用即出错。
	DayUnknown DayStatus = iota
	// DayTrading 那天是交易日：日行情有，且结算价填好了。
	DayTrading
	// DayNotPublished 日行情取不到（404）。
	//
	// ⚠️ 它对**过去的**日期意味着「非交易日」，对**当天或将来**什么都不意味着。
	// 把两者合并成「非交易日」，会在每次「今天的还没发」时多删掉一个交易日 ——
	// 而少一个交易日会让此后每一次今昨仓滚动错位，且不报错。
	DayNotPublished
	// DaySettling 日行情已发布但结算价全为空 —— 那天是交易日，只是还没结算。
	DaySettling
)

func (d DayStatus) String() string {
	switch d {
	case DayTrading:
		return "交易日（已结算）"
	case DayNotPublished:
		return "日行情取不到"
	case DaySettling:
		return "交易日（未结算）"
	}
	return "未探"
}

// ProbeDay 探一个自然日在上期所是不是交易日。
//
// ⚠️ 三态而不是两态。「取不到」与「非交易日」是两件事：
// 前者对过去的日期才等价于后者，对今天或将来什么都不说明。
func ProbeDay(ctx context.Context, day types.TradingDay) (DayStatus, error) {
	_, _, err := FetchSHFE(ctx, day)
	switch {
	case err == nil:
		return DayTrading, nil
	case strings.Contains(err.Error(), "结算尚未发生"):
		return DaySettling, nil
	case strings.Contains(err.Error(), "状态码 404"):
		return DayNotPublished, nil
	}
	return DayUnknown, err
}
