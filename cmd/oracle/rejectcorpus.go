package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// rejectObservation 是一次拒单的**机器可读**记录，`ctperr` 的语料。
//
// # ⚠️ 它为什么只有码、没有原话
//
// `StatusMsg` 是柜台的**自由文本**，本项目的纪律是「可以进本地日志，
// **不进入库的夹具**」（同 `kq.Scrubbed` 那一条）。而 `ctperr` 要的从来不是原话，
// 是**码**：`RspInfo.ErrorID`，以及**交易所塞在 `StatusMsg` 前缀里的那个整数**。
//
//	⚠️ 两个码空间撞号：CTP 的 `50 平今仓位不足` 与上期所的 `50 价格跌破跌停板`
//	**意思完全不同** ⇒ 记录必须带「哪个空间」这一维，不能只有一个整数。
//
// ⇒ 于是每条记录带两个可空的码位：`error_id`（CTP 空间）与
// `exchange_code`（交易所空间，从前缀解析）。⚠️ **两个都可能缺**，
// 而「缺」与「零」必须分得开 —— 所以用指针，不用 0。
type rejectObservation struct {
	TradingDay string `json:"trading_day"`
	// At 是本地时钟（HH:MM:SS）；空表示那一轮没记这一栏（20260916 之前的记录）。
	//
	// ⚠️ 它补的是一个**离开时钟就读不懂**的码：柜台的 `26 当前状态禁止报单` 取决于时段 ——
	// 10:15–10:30 的盘中休息里，**每一个**交易所都给这个码。
	// 只有交易日的话，一条 10:28 拍到的 26 与「这个交易所报不进单」在语料里长得一模一样，
	// ⚠️ 而 20260916 我正是差一点据此写下「郑商所 / 广期所在 SimNow 上禁止报单」。
	At         string `json:"at,omitempty"`
	Exchange   string `json:"exchange"`
	Instrument string `json:"instrument"`
	// Case 是**我给这次输入起的名字**，不是柜台给的。
	// ⚠️ 20260911 栽过一次：`bc` 的 tick 是 10 而偏移写死成 ±1，
	// 于是「低于跌停」那条**同时**违反了步长，拿到的是步长的码 ——
	// **而输出上标着「低于跌停」**。⇒ 这一栏永远只是标签，
	// 判定要看 `violates`。
	Case string `json:"case"`
	// Violates 列出这次输入**确知违反**的项。一条只违反一项的输入，
	// 它拿到的码才能归给那一项。
	Violates []string `json:"violates"`
	Offset   string   `json:"offset"`
	// ErrorID 是 CTP 空间的码；nil 表示这次应答里没有（不是 0）。
	ErrorID *int `json:"error_id"`
	// ExchangeCode 是交易所空间的码，从 StatusMsg 的 `NN:` 前缀解析。
	// nil 表示前缀里没有整数 —— ⚠️ 那与「码是 0」是两回事。
	ExchangeCode *int `json:"exchange_code"`
	// Outcome：rejected / resting / traded / error。
	// ⚠️ `rejected` 之外的取值都意味着**这条用例没验到它要验的东西**，
	// 而它们必须进语料 —— 否则语料里只剩成功的那些，看起来覆盖得很齐。
	Outcome string `json:"outcome"`
	// Source 永远是 `"probe"`，由 `ctp-reject` 写入。
	//
	// ⚠️ 它存在的唯一理由是**把手填这条路堵死**：20260912 评审判
	// 「拒单码只活在提交正文里」为必修，而最快的「修法」是**照着提交正文
	// 手打一份语料** —— 那会造出一份带机器可读外形的转录，
	// **而它与真正拍下来的语料在文件里长得一模一样**。
	//
	//	⚠️ 这正是同一天 §13 #13 被打回的那个毛病的翻版：
	//	文档里的数没有任何落盘支撑，而它们看起来像证据。
	//
	// ⇒ 守卫 `TestRejectCorpusIsProbeWrittenOnly` 断言每一条都是 `"probe"`。
	// 谁要手填，得先把那条守卫改掉 —— **而那一改会出现在 diff 里**。
	Source string `json:"source"`
	// HasOrderSysID 报告这笔委托的回报里**有没有交易所委托号**；nil 表示那一轮没记这一栏（20260915 之前的 12 条）。
	//
	// ⚠️ 为 §13 #6 那个替代解释补的（评审 20260915）：`true` ⇒ 进过交易所，那个码出自交易所；
	// `false` ⇒ 没有交易所编号 —— 柜台前置拒，**或**交易所在分配编号之前拒，本栏分不开。
	// ⚠️ 只记有无，**不记编号本身**：判别只要有无，而多记一个标识字段没有收益。
	HasOrderSysID *bool `json:"has_order_sys_id,omitempty"`
	// PriceTick 是这一轮构造价格用的最小变动价位；nil 表示那一轮没记这一栏（20260916 之前的 12 条）。
	//
	// ⚠️ 它是**这批语料唯一出过错的那一环**的落盘：20260914 夜盘 `bc` 的 tick 是 10 而命令里填了 1，
	// 于是「低于跌停」那一笔同时违反了步长，拿到的是步长的码，**而输出上标着「低于跌停」**。
	// 那次错**事后**才被发现，靠的是人重读命令行 —— 语料本身一个字都没说。
	// ⇒ 把它记进每一条：谁用了哪个 tick，事后可核。
	PriceTick *float64 `json:"price_tick,omitempty"`
	// TickSource 是那个 tick 的出处：`counter`（柜台 ReqQryInstrument 报的）或 `flag`（人手 -tick 填的）。
	//
	// ⚠️ 两者的可信度不同，而**数值上完全一样** —— 不记出处的话，
	// 一个手填对了的 tick 与一个柜台报的 tick 在语料里分不开。
	TickSource string `json:"tick_source,omitempty"`
}

// tickUsed 是这一轮用的最小变动价位与它的出处。
//
// ⚠️ 合成一个类型而不是两个散参数：出处与数值分开传，就会有人只传其中一个。
type tickUsed struct {
	Value  float64
	Source string
}

// tickFromCounter / tickFromFlag 是 TickSource 的两个合法取值。
const (
	tickFromCounter = "counter"
	tickFromFlag    = "flag"
)

// observe 把一次用例的委托回报折成一条语料。纯函数：离线可测。
func observe(day, at, ex, inst string, rc rejectCase, st ctp.OrderState, outcome string, tk tickUsed) rejectObservation {
	o := rejectObservation{
		TradingDay: day, At: at, Exchange: ex, Instrument: inst,
		Case: rc.Name, Violates: rc.Violates,
		Offset: string(rc.Off), Outcome: outcome, Source: "probe",
		TickSource: tk.Source,
	}
	if tk.Value > 0 {
		v := tk.Value
		o.PriceTick = &v
	}
	// ⚠️ 两个码位都用指针：**「缺」与「零」必须分得开**。
	// `ErrorID == 0` 恰恰是价格类拒单的常态（它们不经过 RspInfo）。
	if st.ErrorID != 0 {
		id := st.ErrorID
		o.ErrorID = &id
	}
	if code, ok := msgCode(st.StatusMsg); ok {
		o.ExchangeCode = &code
	}
	has := st.OrderSysID != ""
	o.HasOrderSysID = &has
	return o
}

// msgCode 从柜台自由文本里解析**开头那个整数码**，形如 `48:…`。
//
// ⚠️ 只认**开头**：文本里别处出现的数字（价格、手数）一律不算。
// 返回 false 表示没有前缀码 —— 那与「码是 0」必须分得开，
// 所以调用方拿到的是 `*int` 而不是 `int`。
func msgCode(s string) (int, bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(s[:i]))
	if err != nil {
		return 0, false
	}
	return n, true
}

// writeRejectCorpus 把这一轮的观测落成语料。
//
// ⚠️ **同一天同一个交易所可以跑很多轮**，所以它**读回已有的那份并合并**，
// 而不是覆盖 —— 一次覆盖会把上一个交易所的观测抹掉，
// 而抹掉之后的文件与「只测过这一个交易所」长得一模一样。
func writeRejectCorpus(dir string, obs []rejectObservation,
	logf func(string, ...any)) (string, error) {
	if len(obs) == 0 {
		return "", fmt.Errorf("⚠️ 一条观测都没有，不落盘 —— 一份空语料与一份没生成的语料在下游同形")
	}
	path := filepath.Join(dir, "ctp-reject-codes.json")
	var all []rejectObservation
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &all); err != nil {
			return "", fmt.Errorf("已有的语料读不动，**不覆盖**：%w", err)
		}
	}
	// ⚠️ 同一 (交易日, 交易所, 合约, 用例) 只留最后一次 —— 重跑要能覆盖自己，
	// 而不同交易所/不同日的必须都留着。
	key := func(o rejectObservation) string {
		return o.TradingDay + "|" + o.Exchange + "|" + o.Instrument + "|" + o.Case
	}
	idx := map[string]int{}
	for i, o := range all {
		idx[key(o)] = i
	}
	added, replaced := 0, 0
	for _, o := range obs {
		if i, ok := idx[key(o)]; ok {
			all[i] = o
			replaced++
			continue
		}
		idx[key(o)] = len(all)
		all = append(all, o)
		added++
	}
	sort.Slice(all, func(i, j int) bool { return key(all[i]) < key(all[j]) })
	b, err := json.MarshalIndent(all, "", "\t")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		return "", err
	}
	logf("[rej] 语料落盘 %s（新增 %d、覆盖 %d，累计 %d 条）", path, added, replaced, len(all))
	return path, nil
}
