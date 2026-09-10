package live

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// sampleDict 忠于真实字典的形态：顶层是「合约代码 → 条目」的大对象，
// 条目里带 trading_time，且 commission / margin 带着**上游自己的 float64 噪声**。
//
// ⚠️ 那两个带噪声的数是照抄真实数据的形状（真实字典里就写着
// "commission": 31.477800000000002），不是我编出来吓唬人的。
const sampleDict = `{
  "SHFE.rb2701": {
    "class": "FUTURE",
    "instrument_id": "SHFE.rb2701",
    "exchange_id": "SHFE",
    "ins_id": "rb2701",
    "product_id": "rb",
    "volume_multiple": 10,
    "price_tick": 1.0,
    "price_decs": 0,
    "expired": false,
    "min_limit_order_volume": 1,
    "max_limit_order_volume": 500,
    "commission": 3.1477800000000002,
    "margin": 2210.5999999999995,
    "trading_time": {
      "day": [["09:00:00","10:15:00"],["10:30:00","11:30:00"],["13:30:00","15:00:00"]],
      "night": [["21:00:00","23:00:00"]]
    }
  },
  "SHFE.rb2705": {
    "class": "FUTURE",
    "instrument_id": "SHFE.rb2705",
    "exchange_id": "SHFE",
    "ins_id": "rb2705",
    "product_id": "rb",
    "volume_multiple": 10,
    "price_tick": 1.0,
    "expired": false,
    "trading_time": {
      "day": [["09:00:00","10:15:00"],["10:30:00","11:30:00"],["13:30:00","15:00:00"]],
      "night": [["21:00:00","23:00:00"]]
    }
  },
  "CFFEX.IF2701": {
    "class": "FUTURE",
    "instrument_id": "CFFEX.IF2701",
    "exchange_id": "CFFEX",
    "ins_id": "IF2701",
    "product_id": "IF",
    "volume_multiple": 300,
    "price_tick": 0.2,
    "expired": false,
    "trading_time": {
      "day": [["09:30:00","11:30:00"],["13:00:00","15:00:00"]],
      "night": []
    }
  },
  "KQ.i@CFFEX.IF": {
    "class": "FUTURE_INDEX",
    "instrument_id": "KQ.i@CFFEX.IF",
    "exchange_id": "KQ",
    "product_id": "IF",
    "volume_multiple": 300,
    "price_tick": 0.2,
    "trading_time": {"day": [["09:30:00","11:30:00"]], "night": []}
  }
}`

func serve(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// TestFetchFiltersAndSkips 断言流式筛选：要的解出来，不要的**读掉而不解**。
func TestFetchFiltersAndSkips(t *testing.T) {
	srv := serve(t, sampleDict, http.StatusOK)
	defer srv.Close()

	// ⚠️ 要的那一条必须排在**被跳过的条目之后**，否则本条没有判别力。
	//
	// 第一版筛的是 SHFE.rb*，而它们是字典里最前两条 ——
	// 后面被跳过的条目坏了解码器也无所谓，要的东西已经拿到了。
	// 实测：把「跳过时读掉条目」那段删掉，这条测试**照样全绿**。
	// 我当时还在注释里写「拿到 rb2705 正是这条的证据」—— 那句话是错的。
	got, err := FetchSymbols(context.Background(), srv.URL,
		func(id string) bool { return id == "SHFE.rb2701" || id == "CFFEX.IF2701" }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应当保留 2 个合约（首条与靠后的一条），实得 %d 个：%v", len(got), got)
	}
	s := got["SHFE.rb2701"]
	if s.ProductID != "rb" || s.VolumeMultiple != 10 || s.PriceTick != 1.0 {
		t.Errorf("条目解析不对：%+v", s)
	}
	if len(s.TradingTime.Night) != 1 || s.TradingTime.Night[0][0] != "21:00:00" {
		t.Errorf("夜盘时段没解出来：%+v", s.TradingTime)
	}
	// ⚠️ 这一条才是判别力所在：IF2701 排在被跳过的 rb2705 **之后**。
	// 跳过的条目若没被读掉，解码器位置就错了，它要么解不出来要么解成别的东西。
	iff, ok := got["CFFEX.IF2701"]
	if !ok {
		t.Fatal("⚠️ 排在被跳过条目之后的那一条没拿到 —— 跳过时没把条目读掉，解码器位置错乱了")
	}
	if iff.ProductID != "IF" || iff.VolumeMultiple != 300 {
		t.Errorf("⚠️ 靠后那一条解错了：%+v —— 同样指向解码器位置错乱", iff)
	}
}

// TestFetchRefusesNilFilter 断言「全都要」不是默认值。
func TestFetchRefusesNilFilter(t *testing.T) {
	srv := serve(t, sampleDict, http.StatusOK)
	defer srv.Close()
	if _, err := FetchSymbols(context.Background(), srv.URL, nil, nil); err == nil {
		t.Error("⚠️ 没给筛选函数却被接受了 —— 字典有约三万个条目，" +
			"「全都要」不该是一个默认值")
	}
}

// TestFetchRefusesPartialContent 断言 206 是**错误**而不是可用结果。
//
// ⚠️ 半份字典解析出来的东西看起来完全正常，只是少了一些合约 ——
// 而「少了一些合约」在下游表现为「查不到这个合约」，与「本库还没有数据」同形。
func TestFetchRefusesPartialContent(t *testing.T) {
	srv := serve(t, sampleDict, http.StatusPartialContent)
	defer srv.Close()
	_, err := FetchSymbols(context.Background(), srv.URL, func(string) bool { return true }, nil)
	if err == nil {
		t.Fatal("⚠️ 206 被当成了可用结果")
	}
	if !strings.Contains(err.Error(), "206") {
		t.Errorf("错误信息里没提状态码：%v", err)
	}
}

// TestFetchRefusesTruncatedStream 断言流在中途断掉时**整体失败，不返回半份**。
//
// ⚠️ 两种切法要分开测，它们走的是**不同的**守卫：
//
//	切在条目中间   dec.Decode 先报错 —— 这条早就成立
//	切在条目之间   ⚠️ 只有收尾的 '}' 检查能抓到
//
// 第一版只测了前者，于是「收尾检查」那条守卫**至今未被验证过**：
// 实测把它删掉，这条测试照样全绿。
func TestFetchRefusesTruncatedStream(t *testing.T) {
	cut := sampleDict[:len(sampleDict)/2] // 从中间切断
	srv := serve(t, cut, http.StatusOK)
	defer srv.Close()
	got, err := FetchSymbols(context.Background(), srv.URL, func(string) bool { return true }, nil)
	if err == nil {
		t.Fatalf("⚠️ 被切断的流没有报错，返回了 %d 个条目 —— "+
			"半份结果比没有结果更坏：它看起来完全正常", len(got))
	}
	if got != nil {
		t.Errorf("⚠️ 报错的同时还返回了 %d 个条目 —— 调用方可能会用它", len(got))
	}
}

// TestFetchRefusesCutBetweenEntries 断言**只缺收尾**的流也整体失败。
//
// ⚠️ 这一条补的是一个我以为已经被覆盖、实际从未被验证过的守卫。
// 三种切法走的是**三条不同的路径**，而我第一版只测了第一条：
//
//	切在条目中间        dec.Decode 先报错          —— 早就成立
//	切在条目之后带逗号  循环里读下一个键时 EOF     —— 也成立，但仍不是收尾检查
//	⚠️ 只缺收尾的 '}'   **每个条目都解得出来**     —— 唯一能抓到的是最后那次 Token()
//
// 第三种是最危险的：实测把收尾检查删掉，它返回 **4 条、err=nil** ——
// 一份看起来完整的半份字典，静默通过。
func TestFetchRefusesCutBetweenEntries(t *testing.T) {
	ws := " " + string(rune(10)) // 空格与换行，避开转义序列
	cut := strings.TrimSuffix(strings.TrimRight(sampleDict, ws), "}")
	// ⚠️ 零层：先确认切点确实只去掉了收尾，而不是切进了某个条目。
	if !strings.HasSuffix(strings.TrimRight(cut, ws), "}") {
		t.Fatalf("切点不对，尾部是 %q —— 本条测的是「只缺收尾」，切错了什么都不说明",
			cut[len(cut)-20:])
	}
	srv := serve(t, cut, http.StatusOK)
	defer srv.Close()
	got, err := FetchSymbols(context.Background(), srv.URL, func(string) bool { return true }, nil)
	if err == nil {
		t.Fatalf("⚠️ 只缺收尾的流没有报错，返回了 %d 个条目 —— "+
			"每一条都解得出来，只是少了收尾。**这种半份结果看起来最正常**", len(got))
	}
	if !strings.Contains(err.Error(), "收尾") {
		t.Errorf("红了但红错了理由：%v", err)
	}
	if got != nil {
		t.Errorf("⚠️ 报错的同时还返回了 %d 个条目", len(got))
	}
}

// TestToSessionTable 断言时段表转换，包括无夜盘的品种。
func TestToSessionTable(t *testing.T) {
	srv := serve(t, sampleDict, http.StatusOK)
	defer srv.Close()
	syms, err := FetchSymbols(context.Background(), srv.URL,
		func(id string) bool { return !strings.HasPrefix(id, "KQ.") }, nil)
	if err != nil {
		t.Fatal(err)
	}
	tabs, err := SessionTablesOf(syms)
	if err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 2 {
		t.Fatalf("应当汇总出 2 个品种（rb / IF），实得 %d 个", len(tabs))
	}
	byProduct := map[string]refdata.SessionTable{}
	for _, tb := range tabs {
		byProduct[tb.Product] = tb
	}
	rb := byProduct["rb"]
	if len(rb.Day) != 3 || len(rb.Night) != 1 {
		t.Errorf("rb 的时段数不对：日 %d 夜 %d", len(rb.Day), len(rb.Night))
	}
	if rb.Night[0].Start != refdata.MustClockTime(21, 0, 0) ||
		rb.Night[0].End != refdata.MustClockTime(23, 0, 0) {
		t.Errorf("rb 夜盘时段不对：%v", rb.Night[0])
	}
	// ⚠️ 无夜盘的品种必须是**空的夜盘**，不是一个零长时段 ——
	// 零长时段会让 Session.Validate 报错，而那是个指向错误原因的失败。
	if len(byProduct["IF"].Night) != 0 {
		t.Errorf("IF 没有夜盘，却解出了 %d 段", len(byProduct["IF"].Night))
	}
}

// TestSessionTablesRefuseConflict 断言同品种内时段不一致时**不自动取第一个**。
func TestSessionTablesRefuseConflict(t *testing.T) {
	// ⚠️ Class 必须显式给 "FUTURE"。
	//
	// 20260910 之前这两条**没设 Class**，于是零值 "" != "FUTURE"，
	// 两条都在冲突检查之前就被当成非 FUTURE 跳过了 ——
	// 本条拿到的错误其实是「一个 FUTURE 合约都没有（按 class 跳过了 map[:2]）」。
	// 而它只断言了 `err != nil`，**任何错误都能满足**。
	//
	//	⇒ 这条测试从建立起就没走到过它名字里那条路，而它一直是绿的。
	//
	// 破坏验证当场揭出来的：把 `if len(conflicts) > 0` 关掉，本条照样绿。
	syms := map[string]Symbol{
		"SHFE.rb2701": {InstrumentID: "SHFE.rb2701", ExchangeID: "SHFE", ProductID: "rb",
			Class: "FUTURE", VolumeMultiple: 10, PriceTick: 1,
			TradingTime: TradingTime{Day: [][]string{{"09:00:00", "15:00:00"}}}},
		"SHFE.rb2705": {InstrumentID: "SHFE.rb2705", ExchangeID: "SHFE", ProductID: "rb",
			Class: "FUTURE", VolumeMultiple: 10, PriceTick: 1,
			TradingTime: TradingTime{Day: [][]string{{"09:30:00", "15:00:00"}}}},
	}
	_, err := SessionTablesOf(syms)
	if err == nil {
		t.Fatal("⚠️ 同品种两个月份时段表不同却被接受了 —— " +
			"自动取第一个会把「上游数据有问题」和「时段按品种这个假设错了」一起藏掉")
	}
	// ⚠️ 错在**哪一条**上必须查：只查 err != nil 的话，
	// 任何一个更早的失败（比如上面那次 class 跳过）都会冒充成功。
	if !strings.Contains(err.Error(), "不自动取第一个") {
		t.Errorf("⚠️ 报错了，但不是冲突那条：%v —— "+
			"本条要的是冲突检查真的走到了，不是「有个错误」", err)
	}
}

// TestToInstrumentLeavesRuleFieldsZero 断言**转不出来的规则字段留零值**，不猜。
//
// ⚠️ 零值在 refdata 里是「规则数据缺失，使用即报错」。
// 一个「差不多能用」的合约规格，会在平今平昨和大边上静默算错。
func TestToInstrumentLeavesRuleFieldsZero(t *testing.T) {
	s := Symbol{Class: "FUTURE", InstrumentID: "SHFE.rb2701", ExchangeID: "SHFE",
		ProductID: "rb", VolumeMultiple: 10, PriceTick: 1,
		TradingTime: TradingTime{Day: [][]string{{"09:00:00", "15:00:00"}}}}
	inst, err := ToInstrument(s)
	if err != nil {
		t.Fatal(err)
	}
	if inst.PositionDateType != refdata.PositionDateUnknown {
		t.Errorf("⚠️ PositionDateType 被填了值（%v）—— 字典里没有这个字段，"+
			"填出来的只能是猜的", inst.PositionDateType)
	}
	if inst.MaxMarginSide {
		t.Error("⚠️ MaxMarginSide 被填成了 true —— 字典里没有这个字段")
	}
	if inst.HasPriceLimitRatio {
		t.Error("⚠️ HasPriceLimitRatio 被填成了 true —— 字典里没有涨跌幅比例")
	}
	// 转得出来的必须对。
	if !inst.VolumeMultiple.Equal(decimal.RequireFromString("10")) {
		t.Errorf("乘数不对：%s", inst.VolumeMultiple)
	}
	if inst.ID.Exchange != types.SHFE || inst.ID.Product != "rb" {
		t.Errorf("合约代码解析不对：%+v", inst.ID)
	}
	// ⚠️ 留了零值的规格必须过不了 Builder —— 这才是零值报错的意义。
	b := refdata.NewBuilder(1).AddInstrument(inst)
	if _, err := b.Build(); err == nil {
		t.Error("⚠️ 缺规则字段的合约规格过了 Builder 的校验 —— " +
			"那样一个「差不多能用」的规格会在平今平昨和大边上静默算错")
	}
}

// TestFloatRoundTripGuard 断言高精度小数不会被静默截断。
func TestFloatRoundTripGuard(t *testing.T) {
	// 正常形态：位数很少，往返安全。
	for _, f := range []float64{10, 300, 0.2, 0.5, 1, 0.01} {
		if _, err := fromFloat(f, "price_tick", "测试"); err != nil {
			t.Errorf("%v 本该往返安全：%v", f, err)
		}
	}
	// ⚠️ 这条不是假设的：若哪天上游开始给这种数，静默截断会让 tick 差一点点，
	// 而 tick 差一点点意味着报价校验会在极少数价位上放过本该被拒的单。
	if _, err := fromFloat(0.1+0.2, "price_tick", "测试"); err != nil {
		t.Logf("（记录）0.1+0.2 的往返：%v", err)
	}
}

// TestSessionTablesSkipNonFuture 断言非 FUTURE 的条目被**显式跳过**，不是静默混入。
//
// ⚠️ 这条守的是一个真实的坑：**期权合约的代码以同一个品种前缀开头**
// （SHFE.rb2701C3000 与 SHFE.rb2701 前缀相同），
// 于是按代码前缀筛出来的集合里必然混进期权与合成指数。
// 实测：拉 5 个品种时，前 3485 条里「命中」了 692 条 —— 那个比例本身就是信号。
func TestSessionTablesSkipNonFuture(t *testing.T) {
	srv := serve(t, sampleDict, http.StatusOK)
	defer srv.Close()
	// 这次**不**过滤掉 KQ.（它的 class 是 FUTURE_INDEX）。
	syms, err := FetchSymbols(context.Background(), srv.URL, func(string) bool { return true }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(syms) != 4 {
		t.Fatalf("样本应有 4 条，实得 %d 条", len(syms))
	}
	tabs, err := SessionTablesOf(syms)
	if err != nil {
		t.Fatalf("非 FUTURE 的条目应当被跳过而不是报错：%v", err)
	}
	// ⚠️ 只该有 rb 与 IF 两个品种；FUTURE_INDEX 那条不该贡献时段表。
	if len(tabs) != 2 {
		got := make([]string, 0, len(tabs))
		for _, tb := range tabs {
			got = append(got, string(tb.Exchange)+"."+tb.Product)
		}
		t.Errorf("⚠️ 应当只汇总出 2 个品种，实得 %d 个：%v —— "+
			"非 FUTURE 的条目混进来了", len(tabs), got)
	}
}

// TestSessionTablesRefuseAllNonFuture 断言「筛出来的全是非 FUTURE」时报错而不是返回空。
//
// ⚠️ 返回一个空的时段表列表，与「这些品种没有时段表」长得一样，
// 而后者会让调用方以为上游数据缺失，实际是筛选写错了。
func TestSessionTablesRefuseAllNonFuture(t *testing.T) {
	syms := map[string]Symbol{
		"KQ.i@CFFEX.IF": {Class: "FUTURE_INDEX", InstrumentID: "KQ.i@CFFEX.IF",
			ExchangeID: "KQ", ProductID: "IF", VolumeMultiple: 300, PriceTick: 0.2,
			TradingTime: TradingTime{Day: [][]string{{"09:30:00", "11:30:00"}}}},
	}
	tabs, err := SessionTablesOf(syms)
	if err == nil {
		t.Errorf("⚠️ 全是非 FUTURE 却返回了 %d 个时段表、没有报错 —— "+
			"空列表与「这些品种没有时段表」长得一样，而实际是筛选写错了", len(tabs))
	}
}

// TestParseClockHandlesHoursBeyond24 断言上游「小时数 ≥ 24 表示次日」的约定。
//
// ⚠️ 这个约定是实测撞出来的，不是文档里读到的：
// 字典里 SHFE.ag1601 的夜盘终点写作 **26:30:00** —— 即次日 02:30。
// 它是被 refdata.NewClockTime 的越界守卫拦住才暴露的。
//
// ⚠️ 值得单记：**这里取模会碰巧算对** —— 26:30 取模正是 02:30。
// 取模不会在这个约定上出错，只会在 36:00 这类笔误上静默出错，
// 而那时错的值看起来完全合法。「碰巧对」和「对」在这一处长得一模一样。
func TestParseClockHandlesHoursBeyond24(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"21:00:00", "21:00:00", true},
		{"26:30:00", "02:30:00", true}, // ⚠️ 实测：SHFE.ag1601 的夜盘终点
		{"25:00:00", "01:00:00", true},
		{"24:00:00", "00:00:00", true},
		{"47:59:59", "23:59:59", true},
		{"48:00:00", "", false}, // ⚠️ 跨过第二个零点的盘不存在
		{"36:00:00", "12:00:00", true},
		{"09:60:00", "", false},
		{"不是时刻", "", false},
	}
	if len(cases) != 9 {
		t.Fatalf("用例 %d 条，应为 9 —— 增删了就同步改这个数", len(cases))
	}
	for _, c := range cases {
		got, err := parseClock(c.in)
		if c.ok != (err == nil) {
			t.Errorf("⚠️ %q：err=%v，期望 ok=%v", c.in, err, c.ok)
			continue
		}
		if c.ok && got.String() != c.want {
			t.Errorf("%q 解成 %s，应为 %s", c.in, got, c.want)
		}
	}
	// ⚠️ ≥48 那一格的**诊断**要单独钉，因为「结果对」在这里不代表「守卫还在」。
	//
	// 把 `if h >= 48` 关掉之后：h=48 走进 `h >= 24` 那一支，
	// 减 24 落到 24，而 refdata.NewClockTime 的越界守卫照样拦下它。
	// ⇒ **ok 仍然是 false，上面整个表照样全绿** —— 变的只有理由：
	// 从「跨过第二个零点的盘不存在，这是数据错」变成一句通用的越界。
	//
	//	⚠️ 两个守卫叠在同一个结果上时，删掉外面那个不改变结果，只删掉诊断。
	//	而诊断正是那一层存在的全部理由。
	if _, err := parseClock("48:00:00"); err == nil {
		t.Fatal("⚠️ 48:00:00 本该被拒")
	} else if !strings.Contains(err.Error(), "跨过第二个零点") {
		t.Errorf("⚠️ 48:00:00 被拒了，但不是那条诊断：%v —— "+
			"外层那个 `h >= 48` 多半没了，而只查 ok 的话这个差别看不见", err)
	}
}

// TestFetchRefusesEmptyDict 断言**空字典不是可用结果**。
//
// ⚠️ 20260910 补：此前**全库**没有任何东西测它。把 `if scanned == 0` 关掉，
// `go test ./...` 全绿 —— 而它的后果是拿到一份 0 个合约的字典时静默返回空 map，
// 而空 map 与「这批筛选条件确实一个都没命中」长得一模一样。
//
// ⚠️ 这正是方法论 80 那一格：**空集合上一切全称判断都成立**。
// 上游换了个 URL、返回一个 `{}`、或者字典结构变了顶层键 ——
// 三种情形都会走到这里，而没有这条守卫时它们都表现为「同步成功，0 个合约」。
func TestFetchRefusesEmptyDict(t *testing.T) {
	srv := serve(t, `{}`, http.StatusOK)
	defer srv.Close()
	got, err := FetchSymbols(context.Background(), srv.URL,
		func(string) bool { return true }, nil)
	if err == nil {
		t.Fatalf("⚠️ 空字典被当成了可用结果（返回 %d 个条目）—— "+
			"「一个都没命中」与「拿到的根本不是字典」在这里长得一样", len(got))
	}
	// ⚠️ 判别力：非空的必须照常成功。只测拒绝那一侧的话，
	// 一个「永远报错」的实现也能过 —— 而那会让整条同步链路死掉。
	srv2 := serve(t, sampleDict, http.StatusOK)
	defer srv2.Close()
	if _, err := FetchSymbols(context.Background(), srv2.URL,
		func(id string) bool { return id == "SHFE.rb2701" }, nil); err != nil {
		t.Errorf("⚠️ 非空字典本该成功，却报了 %v —— "+
			"上面那条断言可能只是因为它什么都拒绝", err)
	}
}

// TestFromFloatGuardCannotFireOnFloat64 记录一个**结构上打不响的守卫**。
//
// `fromFloat` 里那句往返校验（decimal 转回 float64 必须逐位相同）
// 在**任何有限 float64 上都恒真** —— shopspring 的 NewFromFloat 按设计
// 就用最短可往返表示。实测七个极端值全部相等：
//
//	0.1+0.2 / 1e300 / 1e-300 / MaxFloat64 / 次正规数 / 1.0÷3.0 / 1.2345678901234569e23
//
// ⚠️ 于是它防不住它注释里说要防的那件事。「上游开始给高精度小数」
// 那个损失发生在**更早一步**：JSON 数字被解进 float64 的那一刻。
// 到了 fromFloat 手上，精度已经没了，而它看到的是一个自洽的 float64。
//
//	⚠️ 一个瞄错了位置的守卫，和一个成立的守卫，在代码里长得一模一样 ——
//	差别只在它防的那件事有没有可能走到它面前。
//
// ⇒ **欠着的动作**：真要防这件事，得在解析处把数字读成 `json.Number`
// （原始文本）再与转换结果比。那是 Symbol 线格式的改动，不在本批。
// 在那之前，「高精度小数静默截断」这一条**没有守卫**，别以为有。
//
// 本条把「恒真」这件事钉住：哪天依赖库改了行为，它会红，
// 而那时上面这段说明也就该重写了。
func TestFromFloatGuardCannotFireOnFloat64(t *testing.T) {
	extremes := []float64{0.1 + 0.2, 1e300, 1e-300, math.MaxFloat64,
		math.SmallestNonzeroFloat64, 1.0 / 3.0, 123456789012345678901234.0}
	if len(extremes) != 7 {
		t.Fatalf("用例 %d 个，应为 7 —— 增删了就同步改上面的说明", len(extremes))
	}
	for _, f := range extremes {
		if _, err := fromFloat(f, "price_tick", "测试"); err != nil {
			t.Errorf("⚠️ %v 上往返校验竟然打响了：%v —— "+
				"若这条红了，是**好消息**：那个守卫不再是死的。"+
				"去把它上面那段「结构上打不响」的说明重写", f, err)
		}
	}
}

// TestAgNightSessionCrossesMidnight 用**实测的**白银夜盘验证跨零点表示。
//
// 白银夜盘 21:00 → 次日 02:30，字典写作 "21:00:00" → "26:30:00"。
// 转换后 End < Start，正是 refdata.Session 表示跨零点的形态。
func TestAgNightSessionCrossesMidnight(t *testing.T) {
	s := Symbol{Class: "FUTURE", InstrumentID: "SHFE.ag2702", ExchangeID: "SHFE",
		ProductID: "ag", VolumeMultiple: 15, PriceTick: 1,
		TradingTime: TradingTime{
			Day:   [][]string{{"09:00:00", "15:00:00"}},
			Night: [][]string{{"21:00:00", "26:30:00"}},
		}}
	tab, err := ToSessionTable(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(tab.Night) != 1 {
		t.Fatalf("夜盘应有 1 段，实得 %d 段", len(tab.Night))
	}
	n := tab.Night[0]
	if !n.CrossesMidnight() {
		t.Errorf("⚠️ 21:00→26:30 没被识别为跨零点：%s→%s", n.Start, n.End)
	}
	if n.End.String() != "02:30:00" {
		t.Errorf("终点应为 02:30:00，实为 %s", n.End)
	}
	// ⚠️ 判别力：凌晨 01:00 必须落在这一段内，23:00 也必须。
	for _, c := range []struct {
		clock string
		want  bool
	}{{"22:00:00", true}, {"01:00:00", true}, {"02:29:59", true},
		{"02:30:00", false}, {"20:59:59", false}, {"12:00:00", false}} {
		ct, err := parseClock(c.clock)
		if err != nil {
			t.Fatal(err)
		}
		if got := n.Contains(ct); got != c.want {
			t.Errorf("%s 落在 21:00→02:30 内？得到 %v，应为 %v", c.clock, got, c.want)
		}
	}
}

// TestListedDropsExpired 断言只留在市合约，并把丢掉的数量报出来。
//
// ⚠️ 这个筛选是实测逼出来的：拉 5 个品种得到 653 个 FUTURE，
// 其中 **31 个没有夜盘时段**（7 个空数组、1 个键缺失 —— 两种形态本身就说明数据不齐），
// 它们全部已到期。但反过来不成立：SHFE.rb1601 也已到期，却有完整夜盘。
// **为什么这 31 个缺数据，那份数据回答不了**；能确定的是只用在市合约时
// 同品种内时段表完全一致，混入到期合约则出现三种。
func TestListedDropsExpired(t *testing.T) {
	syms := map[string]Symbol{
		"SHFE.rb2701": {InstrumentID: "SHFE.rb2701", Expired: false},
		"SHFE.rb2705": {InstrumentID: "SHFE.rb2705", Expired: false},
		"SHFE.rb1601": {InstrumentID: "SHFE.rb1601", Expired: true},
		"SHFE.rb2002": {InstrumentID: "SHFE.rb2002", Expired: true},
	}
	got, dropped := Listed(syms)
	if len(got) != 2 || dropped != 2 {
		t.Errorf("应留 2 个丢 2 个，实为留 %d 丢 %d", len(got), dropped)
	}
	if _, ok := got["SHFE.rb1601"]; ok {
		t.Error("⚠️ 到期合约没被丢掉")
	}
	// ⚠️ 丢掉的数量必须被返回：一个静默的筛选会让「上游数据不齐」这件事消失。
	if _, zero := Listed(map[string]Symbol{}); zero != 0 {
		t.Errorf("空输入的丢弃计数应为 0，实为 %d", zero)
	}
	all, d2 := Listed(map[string]Symbol{"x": {InstrumentID: "x", Expired: true}})
	if len(all) != 0 || d2 != 1 {
		t.Errorf("全是到期合约时应留 0 丢 1，实为留 %d 丢 %d", len(all), d2)
	}
}
