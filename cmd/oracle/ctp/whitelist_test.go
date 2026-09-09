package ctp

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// TestFieldDecisionsAreComplete 断言**结构体里的每一个字段都被决定过**。
//
// # ⚠️ 这条比 DIFF 那侧的同类检查强一档，差别值得说清
//
//	DIFF   字段是动态 JSON，只能在**运行时**查「这一份样本里有没有没见过的字段」
//	       ⚠️ 而**样本里没出现过的字段，永远不会被查到**
//	CTP    字段是 Go 结构体，反射能**穷举**
//
// 于是这里说的是「这个结构体里一个字段都没漏」，而不是
// 「这一份样本里没有没见过的」—— 后者会被一个恰好不含某字段的样本骗过去。
//
// # ⚠️ 两个方向都查
//
// 少一个 → 那个字段会被静默带进（或漏出）夹具，而没有任何东西会说话。
// 多一个 → 说明字段被删/改名了，而白名单还留着旧名字 ——
// 那条决定从此**保护不了任何东西**，却仍然看起来像在保护。
func TestFieldDecisionsAreComplete(t *testing.T) {
	cases := []struct {
		name      string
		sample    any
		decisions map[string]decision
	}{
		{"CThostFtdcTradingAccountField", def.CThostFtdcTradingAccountField{}, accountFields},
		{"CThostFtdcInvestorPositionField", def.CThostFtdcInvestorPositionField{}, positionFields},
	}
	for _, c := range cases {
		rt := reflect.TypeOf(c.sample)
		if rt.NumField() < 10 {
			t.Fatalf("⚠️ %s 只反射出 %d 个字段 —— 太少，本条在空转", c.name, rt.NumField())
		}
		inStruct := map[string]bool{}
		var missing []string
		for i := 0; i < rt.NumField(); i++ {
			n := rt.Field(i).Name
			inStruct[n] = true
			if _, ok := c.decisions[n]; !ok {
				missing = append(missing, n)
			}
		}
		sort.Strings(missing)
		for _, n := range missing {
			t.Errorf("⚠️ %s.%s **没有决定** —— 每个字段都要写 keep 或 drop，"+
				"两者都要写理由。⚠️ 没决定的字段不会报错，它只会安静地"+
				"跟着默认行为走，而默认行为在这里是「不进夹具」："+
				"于是一个该留的字段会静默消失，下游把它读成「柜台没给」",
				c.name, n)
		}
		var stale []string
		for n := range c.decisions {
			if !inStruct[n] {
				stale = append(stale, n)
			}
		}
		sort.Strings(stale)
		for _, n := range stale {
			t.Errorf("⚠️ 白名单里有 %s.%s，而结构体里**没有这个字段** —— "+
				"多半是 goctp 升级时字段被改名或删掉了。"+
				"那条决定从此保护不了任何东西，却仍然看起来像在保护", c.name, n)
		}
		kept := 0
		for _, d := range c.decisions {
			if d.Keep {
				kept++
			}
		}
		t.Logf("%-34s 字段 %d 个：留 %d、去 %d", c.name, rt.NumField(), kept, len(c.decisions)-kept)
	}
}

// TestEveryDecisionHasAReason 断言每条决定都写了理由。
//
// ⚠️ 白名单是**送审时要逐键核对**的东西（评审门禁）。一条没有理由的决定
// 与「随手写的」分不开 —— 而核对者无法从一个空理由里看出它是想过的还是漏的。
func TestEveryDecisionHasAReason(t *testing.T) {
	total := 0
	for name, m := range map[string]map[string]decision{
		"accountFields": accountFields, "positionFields": positionFields,
	} {
		for f, d := range m {
			total++
			if strings.TrimSpace(d.Why) == "" {
				t.Errorf("⚠️ %s[%q] 没有理由", name, f)
			}
		}
	}
	if total < 50 {
		t.Fatalf("⚠️ 只查了 %d 条决定 —— 太少，本条在空转", total)
	}
	t.Logf("两张表共 %d 条决定，全部带理由", total)
}

// TestIdentifyingFieldsAreDropped 单独钉住**标识性**字段。
//
// ⚠️ 它与上面两条不是重复：那两条只查「有没有决定」「有没有理由」，
// **不查决定得对不对**。而决定错一处的后果是不可逆的 ——
// 仓库是 public，推上去撤不回来。
//
// 于是这几个名字在这里被**点名**写死：谁把它们改成 keep，这条就红。
func TestIdentifyingFieldsAreDropped(t *testing.T) {
	mustDrop := map[string][]string{
		"accountFields":  {"BrokerID", "AccountID"},
		"positionFields": {"BrokerID", "InvestorID", "InvestUnitID"},
	}
	tables := map[string]map[string]decision{
		"accountFields": accountFields, "positionFields": positionFields,
	}
	for table, names := range mustDrop {
		for _, n := range names {
			d, ok := tables[table][n]
			if !ok {
				t.Errorf("⚠️ %s 里根本没有 %q 这条决定 —— 它是标识性字段，必须显式 drop", table, n)
				continue
			}
			if d.Keep {
				t.Errorf("⚠️ %s[%q] 被标成了 **keep** —— 它是标识性字段。"+
					"⚠️ 这个仓库是 public，推上去撤不回来", table, n)
			}
		}
	}
}
