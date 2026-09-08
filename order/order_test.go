package order

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func rb2701(t *testing.T) types.InstrumentID {
	t.Helper()
	id, err := types.ParseSymbol("SHFE.rb2701", types.NewTradingDay(2026, 9, 9))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// fullFacts 是**八项全都查得了**的一组输入。
//
// ⚠️ 它存在的理由是让「没能查」成为一个**可观测的差别**：
// 每条用例从这里出发只拿掉一样东西，于是「拿掉什么 → 哪一项查不了」
// 是一一对应的。全是零值的话，八项一起查不了，测不出对应关系。
func fullFacts(t *testing.T) Facts {
	t.Helper()
	inst := refdata.Instrument{
		ID:                  rb2701(t),
		VolumeMultiple:      d("10"),
		PriceTick:           d("1"),
		PositionDateType:    refdata.UseHistory,
		IsTrading:           true,
		MinLimitOrderVolume: 1,
		MaxLimitOrderVolume: 500,
		PriceLimitRatio:     d("0.05"),
		HasPriceLimitRatio:  true,
	}
	p, err := position.New(inst.ID, types.Speculation,
		types.NewTradingDay(2026, 9, 9), refdata.UseHistory)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Open(types.Buy, types.NewTradingDay(2026, 9, 9), d("3163"), 3); err != nil {
		t.Fatal(err)
	}
	return Facts{
		Instrument: inst, HasInstrument: true,
		PreSettlement: d("3163"), HasPreSettlement: true,
		Rounding:  refdata.TickFloor,
		Position:  p,
		Available: d("100000"), HasAvailable: true,
		Need: d("2214.1"), HasNeed: true,
		PositionLimit: 100, HasPositionLimit: true,
		InSession: true, HasSession: true,
	}
}

// mkReq 造一笔报单。⚠️ 用具名构造而不是位置字面量：
// Request 加一个字段时，位置字面量会在**每一处**编译失败，
// 而那是好事；但它同时让每条用例都要写一遍 Instrument，
// 于是有人会图省事去掉那个字段 —— 具名构造把这个诱惑去掉。
func mkReq(dir types.Direction, off types.Offset, price decimal.Decimal, vol int) Request {
	return Request{Direction: dir, Offset: off, Hedge: types.Speculation,
		Price: price, Volume: vol}
}

func openReq() Request {
	return Request{Direction: types.Buy, Offset: types.Open,
		Hedge: types.Speculation, Price: d("3163"), Volume: 1}
}

// TestFullFactsPassesEverything 断言那组「全都查得了」的输入**真的全都查了**。
//
// ⚠️ 它是下面每一条用例的地基：地基上就有一项查不了的话，
// 「拿掉 X → X 那项查不了」这个对应关系立不住。
func TestFullFactsPassesEverything(t *testing.T) {
	res := Validate(openReq(), fullFacts(t))
	if !res.FullyChecked() {
		t.Fatalf("⚠️ 地基上就有查不了的：%v —— 下面每条用例的对应关系都立不住",
			res.Unchecked)
	}
	if res.Rejected != nil {
		t.Fatalf("一笔正常的开仓单被拒了：%v", res.Rejected)
	}
	if !res.OK() {
		t.Fatal("全查过且没拒绝，OK() 却是 false")
	}
}

// TestMissingInputMeansUncheckedNotPassed 是本包最要紧的一条。
//
// ⚠️ 每拿掉一样输入，对应那几项必须落进 **Unchecked**，
// 而**不是**悄悄通过。一个「拿不到就跳过」的校验器，
// 在缺输入时返回的「通过」与真的全部查过长得一模一样 ——
// 而回测会据此开出实际开不出的仓（silent-risks.md 第 7 条）。
func TestMissingInputMeansUncheckedNotPassed(t *testing.T) {
	cases := []struct {
		name  string
		strip func(*Facts)
		want  []Check
	}{
		{"没有合约规格", func(f *Facts) { f.HasInstrument = false },
			[]Check{CheckTradable, CheckPriceTick, CheckPriceLimit, CheckVolumeRange}},
		{"没有昨结算价", func(f *Facts) { f.HasPreSettlement = false },
			[]Check{CheckPriceLimit}},
		{"没指定取整方向", func(f *Facts) { f.Rounding = refdata.TickRoundingUnknown },
			[]Check{CheckPriceLimit}},
		{"不知道在不在时段内", func(f *Facts) { f.HasSession = false },
			[]Check{CheckSession}},
		{"不知道可用资金", func(f *Facts) { f.HasAvailable = false },
			[]Check{CheckFunds}},
		{"不知道这笔要占多少", func(f *Facts) { f.HasNeed = false },
			[]Check{CheckFunds}},
		{"不知道限仓", func(f *Facts) { f.HasPositionLimit = false },
			[]Check{CheckPositionLimit}},
		{"不知道持仓", func(f *Facts) { f.Position = nil },
			[]Check{CheckPositionLimit}},
		{"没有手数上限", func(f *Facts) { f.Instrument.MaxLimitOrderVolume = 0 },
			[]Check{CheckVolumeRange}},
	}
	for _, c := range cases {
		f := fullFacts(t)
		c.strip(&f)
		res := Validate(openReq(), f)
		got := map[Check]bool{}
		for _, u := range res.Unchecked {
			got[u.Check] = true
			if strings.TrimSpace(u.Missing) == "" {
				t.Errorf("%s：%s 报了「没能查」却没说缺什么 —— "+
					"「跳过了」不指向任何行动", c.name, u.Check)
			}
		}
		for _, w := range c.want {
			if !got[w] {
				t.Errorf("⚠️ %s：%s **没有**落进 Unchecked —— "+
					"它多半被悄悄当成通过了，而那正是本包要防的",
					c.name, w)
			}
		}
		// ⚠️ 有任何一项没查成时，OK() 必须为 false。
		if res.OK() {
			t.Errorf("⚠️ %s：还有 %d 项没查成，OK() 却是 true", c.name, len(res.Unchecked))
		}
	}
}

// TestRejections 穷举八项各自的拒绝。
func TestRejections(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		fix  func(*Facts)
		want Check
		msg  string
	}{
		{"合约不可交易", openReq(), func(f *Facts) { f.Instrument.IsTrading = false },
			CheckTradable, "不可交易"},
		{"不在交易时段", openReq(), func(f *Facts) { f.InSession = false },
			CheckSession, "不在交易时段"},
		{"价格不是 tick 整数倍",
			mkReq(types.Buy, types.Open, d("3163.5"), 1),
			nil, CheckPriceTick, "整数倍"},
		{"高于涨停",
			mkReq(types.Buy, types.Open, d("9999"), 1),
			nil, CheckPriceLimit, "高于涨停价"},
		{"低于跌停",
			mkReq(types.Buy, types.Open, d("1"), 1),
			nil, CheckPriceLimit, "低于跌停价"},
		{"手数超上限",
			mkReq(types.Buy, types.Open, d("3163"), 501),
			nil, CheckVolumeRange, "超过上限"},
		{"平今超过今仓",
			mkReq(types.Sell, types.CloseToday, d("3163"), 4),
			nil, CheckClosable, "平今 4 手超过今仓 3 手"},
		{"平昨超过昨仓（今仓有 3 手也不许挪用）",
			mkReq(types.Sell, types.CloseYesterday, d("3163"), 1),
			nil, CheckClosable, "不可用于平昨"},
		{"裸 CLOSE",
			mkReq(types.Sell, types.Close, d("3163"), 1),
			nil, CheckClosable, "本库**拒绝**而不是按平昨处理"},
		{"资金不够", openReq(), func(f *Facts) { f.Available = d("1") },
			CheckFunds, "可用资金只有"},
		{"超限仓", openReq(), func(f *Facts) { f.PositionLimit = 3 },
			CheckPositionLimit, "超过限仓"},
	}
	seen := map[Check]bool{}
	for _, c := range cases {
		f := fullFacts(t)
		if c.fix != nil {
			c.fix(&f)
		}
		res := Validate(c.req, f)
		if res.Rejected == nil {
			t.Errorf("⚠️ %s：本该被拒，却通过了（未查 %d 项）", c.name, len(res.Unchecked))
			continue
		}
		if res.Rejected.Check != c.want {
			t.Errorf("%s：拒因判成 %s，应为 %s（%s）",
				c.name, res.Rejected.Check, c.want, res.Rejected.Reason)
			continue
		}
		if !strings.Contains(res.Rejected.Reason, c.msg) {
			t.Errorf("%s：拒因里没有 %q：%s", c.name, c.msg, res.Rejected.Reason)
		}
		seen[c.want] = true
		if res.OK() {
			t.Errorf("%s：被拒了 OK() 却是 true", c.name)
		}
	}
	// ⚠️ 八项**每一项都要有拒绝用例**。少一项时那一项的拒绝分支从没走过，
	// 而它写成什么样测试都绿。
	for _, c := range allChecks {
		if !seen[c] {
			t.Errorf("⚠️ 校验项「%s」一条拒绝用例都没有 —— "+
				"它的拒绝分支从没走过，写成什么样都不会红", c)
		}
	}
}

// TestRejectionPriority 断言**同时违反两项时报优先级高的那个**。
//
// ⚠️ 柜台只回一个拒因。乱序会让本库与柜台在「拒因是什么」上分岔，
// 而两边都判「拒绝」—— 差异不会以失败的形式出现，只会在
// 使用者照着拒因去改错地方时显形。
func TestRejectionPriority(t *testing.T) {
	f := fullFacts(t)
	f.Instrument.IsTrading = false // CheckTradable，优先级最高
	f.Available = d("1")           // CheckFunds，靠后
	// 价格同时越界（CheckPriceLimit，中间）
	req := mkReq(types.Buy, types.Open, d("9999"), 1)
	res := Validate(req, f)
	if res.Rejected == nil || res.Rejected.Check != CheckTradable {
		t.Fatalf("⚠️ 三项同时违反，应报优先级最高的「合约可交易」，得到 %v", res.Rejected)
	}
	// 去掉最高的那个，应当报次高的。
	f.Instrument.IsTrading = true
	res = Validate(req, f)
	if res.Rejected == nil || res.Rejected.Check != CheckPriceLimit {
		t.Fatalf("⚠️ 去掉最高的之后应报「涨跌停」，得到 %v", res.Rejected)
	}
	// ⚠️ 判别力：两次必须**不同**，否则「按优先级」这件事没被验到。
}

// TestValidateDoesNotShortCircuit 断言**跑完八项再返回**。
//
// ⚠️ 在第一个拒绝处短路的话，「还有几项没查成」会取决于
// 第一个拒绝出现在第几位 —— 同一笔单在不同输入下报出不同数量的
// 「没能查」，使用者无从判断自己覆盖了多少。
func TestValidateDoesNotShortCircuit(t *testing.T) {
	f := fullFacts(t)
	f.Instrument.IsTrading = false // 最高优先级的拒绝
	f.HasAvailable = false         // 靠后的一项查不了
	f.HasSession = false           // 靠前的一项也查不了
	res := Validate(openReq(), f)
	if res.Rejected == nil || res.Rejected.Check != CheckTradable {
		t.Fatalf("拒因应为 CheckTradable，得到 %v", res.Rejected)
	}
	got := map[Check]bool{}
	for _, u := range res.Unchecked {
		got[u.Check] = true
	}
	if !got[CheckFunds] {
		t.Error("⚠️ 已经在第一项拒绝了，而**排在后面**的 CheckFunds 没能查这件事丢了 —— " +
			"那说明 Validate 短路了")
	}
	if !got[CheckSession] {
		t.Error("排在前面的 CheckSession 没能查也丢了")
	}
}

// TestVolumeNonPositiveIsInputError 断言手数非正**不算交易所的限制**。
func TestVolumeNonPositiveIsInputError(t *testing.T) {
	for _, v := range []int{0, -1} {
		req := openReq()
		req.Volume = v
		res := Validate(req, fullFacts(t))
		if res.Rejected == nil {
			t.Fatalf("手数 %d 竟然通过了", v)
		}
		if !strings.Contains(res.Rejected.Reason, "输入不合法") {
			t.Errorf("⚠️ 手数 %d 的拒因没说清是**输入不合法**而不是交易所限制：%s",
				v, res.Rejected.Reason)
		}
	}
}

// TestAllChecksAreListed 断言 allChecks 与枚举**没有脱节**。
//
// ⚠️ allChecks 同时是「一共有几项」的唯一来源。加了一项而忘了加进去，
// 那一项就永远不在分母里 —— 而覆盖率看起来只会更好。
func TestAllChecksAreListed(t *testing.T) {
	inList := map[Check]bool{}
	for _, c := range allChecks {
		inList[c] = true
	}
	for c := CheckTradable; c <= CheckPositionLimit; c++ {
		if !inList[c] {
			t.Errorf("⚠️ 校验项 %d（%s）不在 allChecks 里 —— "+
				"它永远不在分母里，而覆盖率看起来只会更好", c, c)
		}
		if c.String() == "未知校验" {
			t.Errorf("⚠️ 校验项 %d 没有名字 —— 报告里会出现一个「未知校验」", c)
		}
	}
	if len(allChecks) != int(CheckPositionLimit) {
		t.Errorf("⚠️ allChecks 有 %d 项，而枚举到 %d —— 两者脱节",
			len(allChecks), int(CheckPositionLimit))
	}
}
