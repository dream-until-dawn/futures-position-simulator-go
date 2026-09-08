package view

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/shopspring/decimal"
)

// measuredAccountFields 从**夹具**里算出柜台实际给的账户字段集。
//
// ⚠️ 刻意不写死一份手抄的名单：手抄的名单会和视图一起被同一个人改错，
// 而**两个独立来源**（代码里的视图 vs 数据里的截面）才有判别力。
// 这与「双实现一致性」是同一条原理，只是两边一边是代码、一边是实测数据。
func measuredAccountFields(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "testdata", "probes", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	union := map[string]bool{}
	files := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Account map[string]any `json:"account"`
		}
		if json.Unmarshal(b, &f) != nil || len(f.Account) == 0 {
			continue
		}
		files++
		for k := range f.Account {
			union[k] = true
		}
	}
	// ⚠️ 迭代次数下界：一份夹具都没读到时，下面会得到空集，
	// 而空集与视图比对必然「本库多出全部字段」—— 那是个指向错误原因的失败。
	if files < 10 {
		t.Fatalf("只从 %d 份夹具里读到账户截面 —— 太少，"+
			"字段集会不完整而本条会以「本库多出字段」的形式失败，那是错的原因", files)
	}
	out := make([]string, 0, len(union))
	for k := range union {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func emptyView(t *testing.T) Account {
	t.Helper()
	a, err := AccountOf(account.Snapshot{}, AccountInput{})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestAccountViewCoversMeasuredFields 断言视图的字段集与**实测的**完全一致。
func TestAccountViewCoversMeasuredFields(t *testing.T) {
	want := measuredAccountFields(t)
	if len(want) < 15 {
		t.Fatalf("从夹具里只算出 %d 个账户字段 —— 太少，本条可能在空转", len(want))
	}
	if err := emptyView(t).CoverExactly(want); err != nil {
		t.Errorf("⚠️ %v", err)
	}
	t.Logf("实测账户字段 %d 个，视图逐个覆盖", len(want))
}

// TestNotImplementedNeverRendersAsZero 是本包存在的核心断言。
//
// ⚠️ 一个还没实现的字段若渲染成 0，而柜台那边恰好也是 0
// （空仓、无期权、无冻结时大量字段都是 0），对拍会判「一致」——
// **一个缺失的实现伪装成了一致**。那比算错更坏：
// 算错会在某个样本上露出来，缺失永远不会。
func TestNotImplementedNeverRendersAsZero(t *testing.T) {
	a := emptyView(t)
	// float_profit 没给 → 必须是「还没实现」，不是 0。
	v, ok := a["float_profit"]
	if !ok {
		t.Fatal("视图里没有 float_profit")
	}
	if v.Presence != NotImplemented {
		t.Errorf("⚠️ 没提供浮动盈亏时 float_profit 的状态是 %v，应为「还没实现」——"+
			"渲染成 0 会让它在空仓样本上与柜台「一致」", v.Presence)
	}
	if v.Why == "" {
		t.Error("「还没实现」必须写明是什么没实现")
	}
	// 提供了就该有值。
	a2, err := AccountOf(account.Snapshot{}, AccountInput{
		FloatProfit: decimal.RequireFromString("-20"), HasFloatProfit: true})
	if err != nil {
		t.Fatal(err)
	}
	if a2["float_profit"].Presence != Present ||
		!a2["float_profit"].Number.Equal(decimal.RequireFromString("-20")) {
		t.Errorf("提供了浮动盈亏却没渲染出来：%+v", a2["float_profit"])
	}
}

// TestRiskRatioAbsentIsNotZero 断言「没有风险度」不被渲染成 0。
//
// ⚠️ 结存非正时没有风险度。空账户不是「风险度 0%」，穿仓账户更不是 ——
// 都渲染成 0 的话，**最危险的状态会看起来最安全**。
func TestRiskRatioAbsentIsNotZero(t *testing.T) {
	a := emptyView(t) // HasRiskRatio 为 false
	if a["risk_ratio"].Presence == Present {
		t.Error("⚠️ 没有风险度时被渲染成了有值 —— 穿仓账户会看起来像风险度 0%")
	}
	a2, err := AccountOf(account.Snapshot{
		RiskRatio: decimal.RequireFromString("0.0092"), HasRiskRatio: true}, AccountInput{})
	if err != nil {
		t.Fatal(err)
	}
	if a2["risk_ratio"].Presence != Present {
		t.Error("有风险度时应当渲染成有值")
	}
}

// TestValueValidateGates 断言两种声明的完整性门槛。
func TestValueValidateGates(t *testing.T) {
	cases := []struct {
		name   string
		v      Value
		msgHas string
	}{
		{"未声明状态", Value{}, "未声明"},
		{"不建模但没到期版本", Value{Presence: NotModeled, Why: "x"}, "没有到期版本"},
		{"不建模但没理由", Value{Presence: NotModeled, Until: "v1.0.0"}, "没写理由"},
		{"还没实现但没说是什么", Value{Presence: NotImplemented}, "没写是什么没实现"},
	}
	if len(cases) != 4 {
		t.Fatalf("用例 %d 条，应为 4 —— 增删了就同步改这个数", len(cases))
	}
	for _, c := range cases {
		err := c.v.Validate("某字段")
		if err == nil {
			t.Errorf("⚠️ %s：被接受了", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.msgHas) {
			t.Errorf("%s：错误信息里没有 %q：%v", c.name, c.msgHas, err)
		}
	}
	// 对照：完整的声明必须通过。
	for _, v := range []Value{
		Num(decimal.Zero),
		Skip("v1.0.0", "期权不建模"),
		Todo("还没做"),
	} {
		if err := v.Validate("某字段"); err != nil {
			t.Errorf("完整的声明被拒：%+v → %v", v, err)
		}
	}
}

// TestCoverExactlyCatchesBothDirections 断言字段集比对**两个方向都查**。
func TestCoverExactlyCatchesBothDirections(t *testing.T) {
	a := emptyView(t)
	full := a.Fields()
	if len(full) < 15 {
		t.Fatalf("视图只有 %d 个字段 —— 太少，本条可能在空转", len(full))
	}
	// 对照：自己和自己比必须通过。
	if err := a.CoverExactly(full); err != nil {
		t.Fatalf("前提变了：视图与自身的字段集不一致：%v", err)
	}
	// 少一个 → 报「本库多出」。
	if err := a.CoverExactly(full[1:]); err == nil {
		t.Error("⚠️ 期望字段集少一个时没有报错 —— 本库多渲染的字段会永远对不上，看起来像真实差异")
	} else if !strings.Contains(err.Error(), "多出") {
		t.Errorf("错误信息没指出「多出」：%v", err)
	}
	// 多一个 → 报「本库缺」。
	if err := a.CoverExactly(append(append([]string{}, full...), "柜台新加的字段")); err == nil {
		t.Error("⚠️ 期望字段集多一个时没有报错 —— 缺的字段对拍会整个漏掉且不会有动静")
	} else if !strings.Contains(err.Error(), "缺") {
		t.Errorf("错误信息没指出「缺」：%v", err)
	}
}
