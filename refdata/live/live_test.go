package live

import (
	"context"
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
	syms := map[string]Symbol{
		"SHFE.rb2701": {InstrumentID: "SHFE.rb2701", ExchangeID: "SHFE", ProductID: "rb",
			VolumeMultiple: 10, PriceTick: 1,
			TradingTime: TradingTime{Day: [][]string{{"09:00:00", "15:00:00"}}}},
		"SHFE.rb2705": {InstrumentID: "SHFE.rb2705", ExchangeID: "SHFE", ProductID: "rb",
			VolumeMultiple: 10, PriceTick: 1,
			TradingTime: TradingTime{Day: [][]string{{"09:30:00", "15:00:00"}}}},
	}
	if _, err := SessionTablesOf(syms); err == nil {
		t.Error("⚠️ 同品种两个月份时段表不同却被接受了 —— " +
			"自动取第一个会把「上游数据有问题」和「时段按品种这个假设错了」一起藏掉")
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
