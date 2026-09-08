package kq

import (
	"encoding/json"
	"strings"
	"testing"
)

// ⚠️ 本文件补的是一处真空：脱敏白名单是**凭据与磁盘之间那道闸**，
// 而它此前一个单测都没有。仓库根上的 fixtures_test.go 查的是**落盘之后**的结果，
// 那只能抓已经写出去的东西 —— 一份被写坏的夹具要先进磁盘才会被发现，
// 而「先落盘再擦」正是 runner.dump 的注释里明确不许做的事。

func TestSanitizeKeepsOnlyWhitelisted(t *testing.T) {
	f := Sanitize(
		map[string]any{
			"balance":   1000.5,
			"available": 900.25,
			"user_id":   "e3b0c442-98fc-1c14-9afb-4c8996fb9242", // 丢弃表
			"brand_new": "???",                                  // 两张表都没有
		},
		map[string]any{"SHFE.rb2701": map[string]any{
			"volume_long": 2.0,
			"account_id":  "12345678", // 丢弃表
			"weird_field": 1.0,        // 两张表都没有
		}},
		map[string]any{"t1": map[string]any{
			"price":       3151.0,
			"volume":      1.0,
			"offset":      "OPEN",
			"investor_id": "abc", // 丢弃表
			"mystery":     "x",   // 两张表都没有
		}},
		map[string]any{"SHFE.rb2701": map[string]any{
			"pre_settlement": 3158.0,
			"last_price":     3151.0,
			"account_id":     "12345678", // 丢弃表
			"bid_price1":     3150.0,     // 行情侧的「不留但也不报漂移」
		}},
		map[string]any{"o1": map[string]any{
			"order_id":    "o1",
			"offset":      "CLOSETODAY",
			"volume_left": 2.0,
			// ⚠️ 这个字段 20260909 才第一次在真实截面里出现（只在进了簿
			// 且尚未成交的开仓委托上）。补进用例是因为破坏验证当场演示过：
			// 把它从白名单里拿掉，本文件**照样绿** —— 白名单的单测里没有
			// 带这个字段的样本，而主模块那条守卫读的是已经落好盘的夹具，
			// 改白名单影响不到它。一个只在**下次采集**时才生效的改动，
			// 在现有语料上没有任何守卫看得见。
			"frozen_margin": 2214.1,
			"user_id":     "e3b0c442-98fc-1c14-9afb-4c8996fb9242", // 丢弃表
			"odd_key":     "x",                                    // 两张表都没有
		}},
		[]Notify{{Type: "TEXT", Level: "WARNING", Code: 412,
			Content: "下单,已被服务器拒绝, 原因:下单价格不是价格单位的整倍数"}},
		"20260908", "2026-09-08T13:00:00+08:00", "单测")

	// ⓪ 通知只留结构化的三项，**content 一个字都不留**。
	//
	// ⚠️ 这条断言是本文件里唯一一条「某个东西**必须不在**」的断言。
	// 理由见 Fixture.Notifies 的注释：白名单靠逐个字段点名来保护，
	// 而自由文本从原理上不在它的保护范围内。仓库是公开的，泄漏不可逆。
	if len(f.Notifies) != 1 {
		t.Fatalf("⚠️ 通知没进夹具：%v", f.Notifies)
	}
	if f.Notifies[0].Code != 412 || f.Notifies[0].Level != "WARNING" {
		t.Errorf("⚠️ 通知的结构化部分没留住：%+v", f.Notifies[0])
	}
	if b, err := json.Marshal(f); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(b), "整倍数") {
		t.Errorf("⚠️ 通知的 content 进了夹具 —— 自由文本不在白名单能保护的范围内")
	}

	// ⓪′ 委托上的 frozen_margin 要留住。见上面那段注释。
	if got := f.Orders["o1"]["frozen_margin"]; got != 2214.1 {
		t.Errorf("⚠️ 委托的 frozen_margin 没保留：%v —— "+
			"它只在「进了簿且尚未成交的开仓委托」上出现（kq_facts 49），"+
			"丢了就再也补不回来：那是采集时刻的东西", got)
	}

	// ① 白名单里的键原样保留。
	if f.Account["balance"] != 1000.5 {
		t.Errorf("白名单字段 balance 没保留：%v", f.Account["balance"])
	}
	if f.Positions["SHFE.rb2701"]["volume_long"] != 2.0 {
		t.Errorf("白名单字段 volume_long 没保留")
	}
	if f.Trades["t1"]["price"] != 3151.0 {
		t.Errorf("白名单字段 price 没保留 —— ⚠️ 它是逐笔对冲口径的基线")
	}
	if f.Quotes["SHFE.rb2701"]["pre_settlement"] != 3158.0 {
		t.Errorf("⚠️ 白名单字段 pre_settlement 没保留 —— " +
			"它同时是手续费基准、保证金基准与逐日盯市基线")
	}

	// ② 丢弃表里的键**不出现**，且**不算漂移**。
	for _, c := range []struct{ where, key string }{
		{"账户", "user_id"}, {"持仓", "account_id"}, {"成交", "investor_id"},
	} {
		var got any
		switch c.where {
		case "账户":
			got = f.Account[c.key]
		case "持仓":
			got = f.Positions["SHFE.rb2701"][c.key]
		case "成交":
			got = f.Trades["t1"][c.key]
		}
		if got != nil {
			t.Errorf("⚠️ %s的丢弃字段 %s 出现在夹具里：%v", c.where, c.key, got)
		}
	}

	// ③ ⚠️ 行情侧的规则与其余三处**相反**，这一条把差别钉住。
	//
	// 账户/持仓/成交是「柜台对账户说的话」，多一个键意味着有个概念本库不知道
	// —— 那要报漂移。而行情是公共数据，多一个键通常只是多了一个指标，
	// 报漂移会让守卫天天响。代价是**行情侧没有漂移探测**，写在 Sanitize 的注释里。
	if _, ok := f.Quotes["SHFE.rb2701"]["bid_price1"]; ok {
		t.Error("⚠️ 行情侧未登记的键被保留了 —— 白名单是白名单，不是黑名单")
	}
	if _, ok := f.Quotes["SHFE.rb2701"]["account_id"]; ok {
		t.Error("⚠️ 行情侧的丢弃字段 account_id 出现在夹具里")
	}
	for _, u := range f.Unclassified {
		if strings.HasPrefix(u, "quotes/") {
			t.Errorf("⚠️ 行情侧不该报漂移，却报了 %s —— "+
				"报漂移会让这个守卫天天响，而天天响的守卫等于没有", u)
		}
	}

	// ④ ⚠️ 两张表都没有的键必须进 Unclassified —— 这是漂移探测器本身。
	//
	// 静默丢弃与静默保留是两种不同的坏：前者丢证据，后者可能泄漏。
	// 白名单选的是「漏一个 → 夹具缺字段 → 报错」这一侧，
	// 而报错的载体就是这个列表。
	want := []string{"accounts/brand_new", "positions/weird_field", "trades/mystery",
		"orders/odd_key"}
	got := strings.Join(f.Unclassified, " ")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("⚠️ 未登记的键 %s 没进 Unclassified —— "+
				"字段集漂移就此静默：得到 %v", w, f.Unclassified)
		}
	}
	if len(f.Unclassified) != 4 {
		t.Errorf("Unclassified 应恰好 4 个，得到 %d 个：%v", len(f.Unclassified), f.Unclassified)
	}

	// —— 委托侧（20260909 新增）——
	//
	// ⚠️ 委托进夹具的理由：柜台的 volume_*_frozen_* 与 frozen_margin
	// 都是「挂着的委托」的函数，而夹具里没有委托 ——
	// 于是本库算出来的冻结拿什么去比都比不了。
	o := f.Orders["o1"]
	if o["volume_left"] != 2.0 {
		t.Errorf("⚠️ 委托的 volume_left 没保留：%v —— 冻结量按它算，不是按委托量", o)
	}
	if o["offset"] != "CLOSETODAY" {
		t.Errorf("⚠️ 委托的 offset 没保留 —— 冻的是今仓还是昨仓全靠它：%v", o)
	}
	if _, leaked := o["user_id"]; leaked {
		t.Error("⚠️⚠️ 委托里的 user_id **进了夹具** —— 那是账户 UUID")
	}
}

// TestSanitizeCarriesTrades 断言成交进得了夹具。
//
// ⚠️ 它此前**根本不进**，而那正好在本项目核心那条设计的死角上：
// 本库存逐笔明细是因为均价是有损压缩，而夹具只存了均价。
func TestSanitizeCarriesTrades(t *testing.T) {
	f := Sanitize(nil, nil, map[string]any{
		"t1": map[string]any{"price": 3150.0, "volume": 1.0, "offset": "OPEN"},
		"t2": map[string]any{"price": 3152.0, "volume": 1.0, "offset": "OPEN"},
	}, nil, nil, nil, "20260908", "2026-09-08T13:00:00+08:00", "")
	if len(f.Trades) != 2 {
		t.Fatalf("⚠️ 成交没进夹具：%v", f.Trades)
	}
	// 两笔不同价 —— 而持仓截面上它们会被压成均价 3151，
	// 那正是夹具里必须有成交的理由。
	if f.Trades["t1"]["price"] == f.Trades["t2"]["price"] {
		t.Error("两笔成交价被弄成一样了")
	}
}

// TestScrubbedCatchesCredentials 断言独立复查抓得住三类东西。
func TestScrubbedCatchesCredentials(t *testing.T) {
	secrets := []Secret{{Name: "KQ_PASSWORD", Value: "hunter2-longenough"}}
	cases := []struct {
		name string
		f    *Fixture
		want string // 期望错误信息里出现的片段；空表示应当通过
	}{
		{"干净", &Fixture{Account: map[string]any{"balance": 1.0}}, ""},
		{"UUID", &Fixture{Account: map[string]any{
			"x": "e3b0c442-98fc-1c14-9afb-4c8996fb9242"}}, "UUID"},
		{"JWT", &Fixture{Account: map[string]any{
			"x": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"}}, "JWT"},
		{"env 值", &Fixture{Account: map[string]any{"x": "hunter2-longenough"}}, "KQ_PASSWORD"},
		{"藏在成交里的 UUID", &Fixture{Trades: map[string]map[string]any{
			"t1": {"x": "e3b0c442-98fc-1c14-9afb-4c8996fb9242"}}}, "UUID"},
	}
	if len(cases) != 5 {
		t.Fatalf("用例 %d 条，应为 5 —— 增删了就同步改这个数", len(cases))
	}
	for _, c := range cases {
		err := Scrubbed(c.f, secrets)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s：应当通过，却报 %v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("⚠️ %s：没被抓住 —— 这一层是白名单之外的独立复查，"+
				"它漏了就只剩一道闸", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误信息里没有 %q：%v", c.name, c.want, err)
		}
		// ⚠️ 错误信息**不许回显凭据值**。
		if strings.Contains(err.Error(), "hunter2-longenough") {
			t.Errorf("⚠️ %s：错误信息里回显了凭据原值 —— "+
				"那会把它写进日志、CI 输出、以及任何贴报告的地方", c.name)
		}
	}
}

// TestBlindSpotsDeclaresWhatItCannotCheck 断言查不了的会被明说出来。
//
// ⚠️ 一个不声明自己盲区的检查器，会让人把「没报错」读成「都查过了」。
func TestBlindSpotsDeclaresWhatItCannotCheck(t *testing.T) {
	bs := BlindSpots([]Secret{
		{Name: "SHORT", Value: "1234"},            // 太短，查不了
		{Name: "LONG", Value: "abcdefghijklmnop"}, // 够长
		{Name: "EMPTY", Value: ""},                // 没配，不算盲区
		{Name: "EDGE", Value: "12345678"},         // 恰好 8 位，够长
	})
	if len(bs) != 1 || bs[0] != "SHORT" {
		t.Errorf("⚠️ 盲区应当恰好是 [SHORT]，得到 %v —— "+
			"多报会训练人忽略告警，少报会让盲区消失", bs)
	}
}
