package ctperr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// observation 是 testdata/refdata/ctp-reject-codes.json 的一条（与 cmd/oracle 的 rejectObservation 同形，只取用得到的键）。
type observation struct {
	TradingDay   string   `json:"trading_day"`
	Exchange     string   `json:"exchange"`
	Case         string   `json:"case"`
	Violates     []string `json:"violates"`
	Offset       string   `json:"offset"`
	ErrorID      *int     `json:"error_id"`
	ExchangeCode *int     `json:"exchange_code"`
	Outcome      string   `json:"outcome"`
	Source       string   `json:"source"`
}

// reasonOf 把一条观测归到拒因；归不了返回 ReasonUnknown。
//
// ⚠️ 依赖用例标签的只有一处：`violates` 里「涨跌停」分不出涨停还是跌停，只能看 case 里写的是哪一个。
// 平昨那条额外要求 offset=4（CTP 的平昨），不收别的开平标志 —— 语料只测过它。
func reasonOf(o observation) Reason {
	if len(o.Violates) != 1 {
		return ReasonUnknown // ⚠️ 同时违反两项的观测，码归不到任何一项
	}
	switch o.Violates[0] {
	case "最小变动价位":
		return ReasonPriceTick
	case "涨跌停":
		switch {
		case strings.Contains(o.Case, "涨停"):
			return ReasonAboveUpperLimit
		case strings.Contains(o.Case, "跌停"):
			return ReasonBelowLowerLimit
		}
	case "可平量":
		if o.Offset == "4" {
			return ReasonCloseYesterdayExceeds
		}
	case "交易时段":
		// ⚠️ 只认**对照组**那一笔：它除时段外完全合法，所以「恰好违反一项」这句话在它身上是真的。
		// 别的用例即使也落在休息时段，同时还违反着价格类或可平量 —— 那时 violates 会是两项，上面就返回了。
		return ReasonOutsideSession
	}
	return ReasonUnknown
}

func loadCorpus(t *testing.T) []observation {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", "refdata", "ctp-reject-codes.json"))
	if err != nil {
		t.Fatalf("⚠️ 读不到拒单语料：%v —— 表的每一格都失去了出处", err)
	}
	var all []observation
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	return all
}

// TestTableMatchesCorpus 双向钉住表与语料：表里每一格语料里有，语料里每一条可归类的拒单表里有。
//
// ⚠️ 两个方向缺一不可：只查「语料 ⇒ 表」，**手打进表的一格**永远不响 —— 那正是「取值不许先填」要防的。
func TestTableMatchesCorpus(t *testing.T) {
	seen := map[key]bool{}
	used := 0
	for _, o := range loadCorpus(t) {
		if o.Source != "probe" {
			t.Fatalf("⚠️ 语料里有一条 source=%q —— 不是探针写的，不许当出处", o.Source)
		}
		if o.Outcome != "rejected" {
			continue // 挂上了 / 成交了的用例没有码可言
		}
		r := reasonOf(o)
		if r == ReasonUnknown {
			t.Logf("ⓘ 归不了类，跳过：%s %s（violates %v、offset %s）", o.Exchange, o.Case, o.Violates, o.Offset)
			continue
		}
		var got Code
		switch {
		case o.ErrorID != nil && o.ExchangeCode != nil:
			t.Fatalf("⚠️ %s %s 同时带 CTP 码 %d 与前缀码 %d —— 归不到一个码空间，先查语料", o.Exchange, o.Case, *o.ErrorID, *o.ExchangeCode)
		case o.ErrorID != nil:
			got = Code{SpaceCTP, *o.ErrorID}
		case o.ExchangeCode != nil:
			got = Code{SpaceStatusPrefix, *o.ExchangeCode}
		default:
			t.Fatalf("⚠️ %s %s 被拒却一个码都没有 —— 语料或工具出了问题", o.Exchange, o.Case)
		}
		k := key{types.Exchange(o.Exchange), r}
		want, ok := measured[k]
		if !ok {
			t.Errorf("⚠️ 语料有 %s %s = %s，而表里**没有这一格** —— 漏填", o.Exchange, r, got)
			continue
		}
		if want != got {
			t.Errorf("⚠️ %s %s：表写 %s，语料是 %s（交易日 %s）—— 以语料为准", o.Exchange, r, want, got, o.TradingDay)
		}
		seen[k] = true
		used++
	}
	for k, c := range measured {
		if !seen[k] {
			t.Errorf("⚠️ 表里有 %s %s = %s，而语料里**没有一条**对得上的观测 —— 这一格是填的，不是测的", k.exchange, k.reason, c)
		}
	}
	// ⚠️ 反空转：一条都没用上时，上面两个方向都会安静地「全过」。
	if used < len(measured) {
		t.Fatalf("⚠️ 只用上 %d 条观测，表有 %d 格 —— 语料读法或归类坏了", used, len(measured))
	}
	t.Logf("ⓘ 表 %d 格，语料用上 %d 条，逐格双向核对", len(measured), used)
}

// TestLookupDoesNotFallBack 钉住没测过的组合返回 false，而不是某个「常见」的码。
//
// ⚠️ 20260916：郑商所与广期所**已经拍了**，于是从这张「没测过」的名单里移出去 ——
// 名单只剩中金所。⚠️ 移出去的同时必须在下面补上「它们现在查得到」的正向断言，
// 否则这次改动的净效果是**少了两个交易所的判别力**，而那在 diff 里看起来只是删了两个词。
func TestLookupDoesNotFallBack(t *testing.T) {
	for _, ex := range []types.Exchange{types.CFFEX} {
		for _, r := range []Reason{ReasonPriceTick, ReasonAboveUpperLimit, ReasonBelowLowerLimit, ReasonCloseYesterdayExceeds} {
			if c, ok := Lookup(ex, r); ok {
				t.Errorf("⚠️ %s %s 查到了 %s —— 这个交易所一条语料都没有", ex, r, c)
			}
			if e, ok := New(ex, r); ok || e != nil {
				t.Errorf("⚠️ New(%s, %s) 造出了 %v —— 没有观测时不许造零码的 Error", ex, r, e)
			}
		}
	}
	if _, ok := Lookup(types.SHFE, ReasonUnknown); ok {
		t.Error("⚠️ 零值拒因查到了码")
	}
	// ⚠️ 反向：测过的要查得到，否则一个恒返回 false 的实现也能过上面。
	if c, ok := Lookup(types.DCE, ReasonCloseYesterdayExceeds); !ok || c != (Code{SpaceCTP, 30}) {
		t.Errorf("大商所平昨超量要查到 CTP 30，得到 %v %v", c, ok)
	}
	// ⚠️ 20260916 从「没测过」名单里移出去的那两个，逐条钉住它们现在查得到 ——
	// 而且钉的是**可平量**那一格：价格类三条五所同号，拿它们做正向断言，
	// 一个「郑商所那几行漏填了」的表也能照样通过（另外三所的值一模一样）。
	for _, c := range []struct {
		ex   types.Exchange
		want Code
	}{
		{types.CZCE, Code{SpaceCTP, 30}},
		{types.GFEX, Code{SpaceCTP, 30}},
	} {
		if got, ok := Lookup(c.ex, ReasonCloseYesterdayExceeds); !ok || got != c.want {
			t.Errorf("⚠️ %s 平昨超量要查到 %v（20260916 实测），得到 %v %v —— "+
				"它刚从「没测过」名单里移出去，这一条是它唯一的对侧", c.ex, c.want, got, ok)
		}
	}
}

// TestCodeSpacesKeepCollidingNumbersApart 钉住撞号的两个 50 在本包里是两个不同的码。
func TestCodeSpacesKeepCollidingNumbersApart(t *testing.T) {
	below, _ := Lookup(types.SHFE, ReasonBelowLowerLimit)
	ctp50 := Code{SpaceCTP, 50}
	if below.Value != 50 {
		t.Fatalf("前提：上期所跌停前缀码是 50，得到 %v", below)
	}
	if below == ctp50 {
		t.Error("⚠️ 前缀 50（跌破跌停）与 CTP 50（平今仓位不足）被判成同一个码 —— 码空间这一维丢了")
	}
}

// TestReasonStringsAreDistinct 钉住每个拒因的名字互不相同、且都不是「未知拒因」。
//
// ⚠️ 这些名字是**读日志的人唯一看得到的东西**：`Error.Error()` 里带的就是它。
// 两个拒因共用一个名字时，一条「平昨超过昨仓」的日志可能说的是完全另一回事，
// 而所有对拍、所有语料、所有码都照常对得上 —— 名字不参与任何比较。
//
// ⚠️ 遍历方式是**从零值往后数到第一个「未知拒因」**：写死一张名单的话，
// 新加一个拒因时这条守卫不会说话（而新加正是最容易撞名字的时候）。
func TestReasonStringsAreDistinct(t *testing.T) {
	seen := map[string]Reason{}
	n := 0
	for r := Reason(1); ; r++ {
		s := r.String()
		if s == ReasonUnknown.String() {
			break // 数完了
		}
		n++
		if prev, dup := seen[s]; dup {
			t.Errorf("⚠️ 拒因 %d 与 %d 共用名字 %q —— 读日志的人分不开这两回事", r, prev, s)
		}
		seen[s] = r
		if r > 100 {
			t.Fatal("⚠️ 数了 100 个还没到头 —— String() 对未知值不返回「未知拒因」了？")
		}
	}
	// ⚠️ 反空转：一个「零个拒因」的枚举也能让上面全过。
	if n < 5 {
		t.Errorf("⚠️ 只数到 %d 个拒因 —— 实测已有五项（价格三项、可平量、交易时段）", n)
	}
	if ReasonUnknown.String() == "" {
		t.Error("⚠️ 零值的名字是空串 —— 它会让上面的循环立刻停，而那看起来像是数完了")
	}
}
