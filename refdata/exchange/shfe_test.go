package exchange

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func fixturePath() string {
	return filepath.Join("..", "..", "testdata", "refdata", "shfe-kx20260907.json")
}

func day20260907() types.TradingDay { return types.NewTradingDay(2026, 9, 7) }

func parseFixture(t *testing.T) (map[string]Daily, Report) {
	t.Helper()
	f, err := os.Open(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, rep, err := ParseSHFE(f, day20260907())
	if err != nil {
		t.Fatalf("解析失败：%v\n%s", err, rep)
	}
	return got, rep
}

// TestParseSHFEAgreesWithTheOracle 是一次**跨来源**核对。
//
// ⚠️ 它比「解析器能跑」重要得多：本包存在的全部理由就是提供一个
// **独立于柜台**的结算价来源。拿柜台自己的结算价去验柜台自己的逐日盯市，
// 是同义反复。
//
// 下面三个数来自快期在**交易日 20260908** 报的 `pre_settlement`
// （即 20260907 的结算价，probes.md §10.2），而本文件来自上期所。
// 两边是完全不同的两条通路。
func TestParseSHFEAgreesWithTheOracle(t *testing.T) {
	got, rep := parseFixture(t)
	t.Logf("%s", rep)

	// 快期在交易日 20260908 给的昨结算价（probes.md §10.2 的那张表）。
	oracle := map[string]string{
		"SHFE.rb2610": "3100",
		"SHFE.rb2701": "3158",
		"SHFE.rb2705": "3189",
		"SHFE.cu2701": "108330",
		"SHFE.ag2702": "16095",
	}
	if len(oracle) < 3 {
		t.Fatalf("核对样本 %d 个 —— 太少", len(oracle))
	}
	// ⚠️ 判别力：核对的合约必须跨多个品种。
	// 全是 rb 的话，「两边一致」只证明了一个品种上的一条通路。
	products := map[string]bool{}
	for sym := range oracle {
		products[sym[:len(sym)-4]] = true
	}
	if len(products) < 3 {
		t.Fatalf("⚠️ 核对样本只覆盖 %d 个品种 —— 跨来源一致这句话判别力不足", len(products))
	}

	for sym, want := range oracle {
		d, ok := got[sym]
		if !ok {
			t.Errorf("⚠️ 上期所日行情里没有 %s —— 而柜台那边有它的昨结算价", sym)
			continue
		}
		if !d.Settlement.Equal(decimal.RequireFromString(want)) {
			t.Errorf("⚠️ %s：上期所今结算 %s，快期报的昨结算 %s —— "+
				"两条独立通路对不上，这比任何一边算错都严重",
				sym, d.Settlement, want)
		}
	}
	t.Logf("跨来源核对 %d 个合约、%d 个品种，全部一致", len(oracle), len(products))
}

// TestParseSHFEDropsAreItemised 断言丢弃是**看得见的**。
//
// ⚠️ 一个静默丢行的解析器，会在上游改格式时给出一份看起来完全正常、
// 只是少了几个合约的结果，而少掉的那几个恰恰可能是要用的那几个。
func TestParseSHFEDropsAreItemised(t *testing.T) {
	got, rep := parseFixture(t)
	if rep.Rows != 332 {
		t.Errorf("夹具应有 332 行，得到 %d —— 夹具换了就同步改这个数", rep.Rows)
	}
	// 每一行都要有去处：留下的 + 丢掉的 = 总行数。
	dropped := 0
	for _, n := range rep.Dropped {
		dropped += n
	}
	if rep.Kept+dropped != rep.Rows {
		t.Errorf("⚠️ 留下 %d + 丢弃 %d ≠ 总行数 %d —— 有行既没留下也没被记账，"+
			"那种行是真正静默的", rep.Kept, dropped, rep.Rows)
	}
	if len(got) != rep.Kept {
		t.Errorf("结果里 %d 个合约，报告说留下 %d 个", len(got), rep.Kept)
	}
	// ⚠️ 下界：一条都没丢时，上面那条恒等式恒真而丢弃分类从未被走到。
	// 实测这份文件里有 31 行被丢弃：26 行小计 + 5 行 TAS。
	// ⚠️ 不是「27 + 5」——有一行**既是小计又是 TAS**，
	// PRODUCTCLASS 先判，于是它记在「非期货」那一类里。「+」暗示互斥，那里不互斥。
	if dropped < 20 {
		t.Errorf("⚠️ 只丢了 %d 行 —— 实测这份文件有 26 行小计与 5 行 TAS（另有一行两者皆是，记在非期货类），"+
			"丢得太少说明它们被当成合约放进去了：%s", dropped, rep)
	}
	// 汇总行与 TAS 必须各自被认出来，而不是混进一个笼统的「解析失败」。
	for _, want := range []string{dropSummary, dropNonFuture} {
		if rep.Dropped[want] == 0 {
			t.Errorf("⚠️ 丢弃分类里没有 %q —— 分类笼统等于没分类：%s", want, rep)
		}
	}
	if !strings.Contains(rep.String(), "丢弃") {
		t.Error("报告里没有丢弃明细")
	}
}

// TestParseSHFERefusesTheWrongDay 断言拿错一天会报错。
//
// ⚠️ 这是本包最危险的一种错：拿错一天的结算价**不会有任何动静**，
// 数看起来完全正常，只是错了一天，而逐日盯市的每一分钱都挂在它上面。
func TestParseSHFERefusesTheWrongDay(t *testing.T) {
	f, err := os.Open(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, _, err = ParseSHFE(f, types.NewTradingDay(2026, 9, 4))
	if err == nil {
		t.Fatal("⚠️ 要 20260904 却拿到 20260907 的文件，解析器没报错 —— " +
			"拿错一天不会有任何动静：数看起来完全正常，只是错了一天")
	}
	if !strings.Contains(err.Error(), "report_date") {
		t.Errorf("报错了但不是因为日期：%v", err)
	}
}

// TestParseSHFERejectsMalformed 断言几种坏输入都报错而不是给半份结果。
func TestParseSHFERejectsMalformed(t *testing.T) {
	day := day20260907()
	cases := []struct {
		name, body, msgHas string
	}{
		{"没有 report_date", `{"o_curinstrument":[]}`, "report_date"},
		{"一行都没有", `{"report_date":"20260907","o_curinstrument":[]}`, "一行都没有"},
		{"全是汇总行", `{"report_date":"20260907","o_curinstrument":[
			{"PRODUCTID":"cu_f","PRODUCTCLASS":"1","DELIVERYMONTH":"小计",
			 "SETTLEMENTPRICE":"","PRESETTLEMENTPRICE":""}]}`, "一个合约都没解析出来"},
		{"结算价为零", `{"report_date":"20260907","o_curinstrument":[
			{"PRODUCTID":"rb_f","PRODUCTCLASS":"1","DELIVERYMONTH":"2701",
			 "SETTLEMENTPRICE":0,"PRESETTLEMENTPRICE":3160}]}`, "一个合约都没解析出来"},
		{"截断的 JSON", `{"report_date":"20260907","o_curinstrument":[{"PRODUCT`, "解析"},
	}
	if len(cases) != 5 {
		t.Fatalf("用例 %d 条，应为 5 —— 增删了就同步改这个数", len(cases))
	}
	for _, c := range cases {
		_, _, err := ParseSHFE(strings.NewReader(c.body), day)
		if err == nil {
			t.Errorf("⚠️ %s：没报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.msgHas) {
			t.Errorf("%s：错误信息里没有 %q：%v", c.name, c.msgHas, err)
		}
	}
}

// TestSettlementKeepsExactText 断言价格不经 float64。
//
// ⚠️ 上期所的价格里有小数（如 `au` 报到 0.02），而 JSON 数值经 float64
// 往返之后会得到 0.10000000000000001 这样的值。金额上的每一次这种损失
// 都会在逐日盯市里被乘以手数与乘数放大。
func TestSettlementKeepsExactText(t *testing.T) {
	// ⚠️ 判据要选一个 float64 **装不下**的数。
	//
	// 第一版用的是 0.1，那不成立：0.1 经 float64 往返回来，
	// decimal.NewFromFloat 给的仍是 "0.1"（它取最短表示）。
	// 也就是那个判据无论走不走 float64 都通过。
	//
	// 这里的 17 位有效数字超出 float64 的精度，往返必然丢位。
	const exact = "123456789.123456789"
	body := `{"report_date":"20260907","o_curinstrument":[
		{"PRODUCTID":"au_f","PRODUCTCLASS":"1","DELIVERYMONTH":"2702",
		 "SETTLEMENTPRICE":` + exact + `,"PRESETTLEMENTPRICE":` + exact + `}]}`
	got, _, err := ParseSHFE(strings.NewReader(body), day20260907())
	if err != nil {
		t.Fatal(err)
	}
	d := got["SHFE.au2702"]
	if s := d.Settlement.String(); s != exact {
		t.Errorf("⚠️ 结算价 %s 解析成 %s —— 经过 float64 了", exact, s)
	}
	if s := d.PreSettlement.String(); s != exact {
		t.Errorf("⚠️ 昨结算价 %s 解析成 %s —— 经过 float64 了", exact, s)
	}

	// 对照：证明上面那条判据**真的**能抓住 float64。
	//
	// ⚠️ 第一版的对照写成 `if 0.1+0.2 == 0.3 { t.Skip(...) }`，而那是空的：
	// 两边都是无类型常量，Go 在编译期用任意精度折叠，根本没走 float64 ——
	// 于是它恒真、整条测试恒被 skip，而被 skip 的正是守着最要紧性质的那条。
	// 用变量强制成运行时的 float64 运算。
	var f float64
	if _, err := fmt.Sscan(exact, &f); err != nil {
		t.Fatal(err)
	}
	if decimal.NewFromFloat(f).String() == exact {
		t.Errorf("⚠️ 对照失效：%s 经 float64 往返之后仍是它自己，"+
			"说明这个判据分不开走没走 float64", exact)
	}
}

// TestFixtureIsRealExchangeData 断言夹具确实是交易所那份，不是手写的。
//
// ⚠️ 一份手写的「像模像样」的夹具，会让解析器测试全绿而实际解析不了真数据。
func TestFixtureIsRealExchangeData(t *testing.T) {
	b, err := os.ReadFile(fixturePath())
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		ReportDate  string           `json:"report_date"`
		Instruments []map[string]any `json:"o_curinstrument"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	// 真数据的特征：品种数量、TAS 合约、以及价格字段的**空字符串**这种联合类型。
	products := map[string]bool{}
	emptyPrices := 0
	for _, r := range raw.Instruments {
		if p, ok := r["PRODUCTID"].(string); ok {
			products[p] = true
		}
		if s, ok := r["SETTLEMENTPRICE"].(string); ok && s == "" {
			emptyPrices++
		}
	}
	if len(products) < 15 {
		t.Errorf("⚠️ 夹具里只有 %d 个 PRODUCTID —— 真的日行情有二十几个品种，"+
			"疑似手写", len(products))
	}
	if emptyPrices == 0 {
		t.Error("⚠️ 夹具里没有一个空字符串价格 —— " +
			"而那正是上期所的真实形状（小计行与 TAS），解析器专门为它写了 numText。" +
			"没有它的夹具证不了那段代码是对的")
	}
	t.Logf("夹具：%d 个 PRODUCTID，%d 个空字符串价格", len(products), emptyPrices)
}

// TestParseSHFEDistinguishesNotYetSettled 断言「结算尚未发生」不被读成「解析失败」。
//
// ⚠️ 实测（自然日 2026-09-08 14:4x，日盘尚未收盘）：
// kx20260908.dat **已经存在且返回 200**，332 行俱全，
// 而全部 SETTLEMENTPRICE 是空字符串 —— 交易所盘中就发布这个文件，
// 结算之后才填价。
//
// **「文件在」不等于「结算发生了」。**
// 把这种情形与解析失败合并，会让人去查解析器，而真正的原因是「时候未到」。
//
// 它同时是一条**独立的结算判据**，与柜台那两条（quotes.settlement 由 "-" 变成数、
// pre_balance 推进）互不依赖。
func TestParseSHFEDistinguishesNotYetSettled(t *testing.T) {
	body := `{"report_date":"20260907","o_curinstrument":[
		{"PRODUCTID":"rb_f","PRODUCTCLASS":"1","DELIVERYMONTH":"2701",
		 "SETTLEMENTPRICE":"","PRESETTLEMENTPRICE":3160},
		{"PRODUCTID":"rb_f","PRODUCTCLASS":"1","DELIVERYMONTH":"2705",
		 "SETTLEMENTPRICE":"","PRESETTLEMENTPRICE":3187},
		{"PRODUCTID":"cu_f","PRODUCTCLASS":"1","DELIVERYMONTH":"小计",
		 "SETTLEMENTPRICE":"","PRESETTLEMENTPRICE":""},
		{"PRODUCTID":"sc_tas","PRODUCTCLASS":"6","DELIVERYMONTH":"2610",
		 "SETTLEMENTPRICE":"","PRESETTLEMENTPRICE":""}]}`
	_, rep, err := ParseSHFE(strings.NewReader(body), day20260907())
	if err == nil {
		t.Fatal("⚠️ 结算价全为空时应当报错")
	}
	if !strings.Contains(err.Error(), "结算尚未发生") {
		t.Errorf("⚠️ 报错了但没说「结算尚未发生」，而是：%v —— "+
			"合并成「解析失败」会让人去查解析器，而真正的原因是时候未到", err)
	}
	// ⚠️ 汇总行与 TAS 不该被算进「结算价为空」那一类，
	// 否则一份**只有汇总行**的文件也会被判成「尚未结算」。
	if rep.Dropped[dropEmptyPrice] != 2 {
		t.Errorf("「结算价为空」应为 2 条（两个 rb），得到 %d：%s",
			rep.Dropped[dropEmptyPrice], rep)
	}

	// 对照：同样全为空但**一条期货都没有**（只有汇总与 TAS）——
	// 那不是「尚未结算」，那是这份文件里根本没有期货行。
	only := `{"report_date":"20260907","o_curinstrument":[
		{"PRODUCTID":"cu_f","PRODUCTCLASS":"1","DELIVERYMONTH":"小计",
		 "SETTLEMENTPRICE":"","PRESETTLEMENTPRICE":""}]}`
	_, _, err = ParseSHFE(strings.NewReader(only), day20260907())
	if err == nil {
		t.Fatal("应当报错")
	}
	if strings.Contains(err.Error(), "结算尚未发生") {
		t.Errorf("⚠️ 一条期货都没有却被判成「结算尚未发生」—— "+
			"那会让人白等一个永远不会填上的价：%v", err)
	}
}
