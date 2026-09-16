package futsim

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// groupOf 找出组键为 key 的那一组。
func groupOf(t *testing.T, gs []margin.GroupResult, key string) margin.GroupResult {
	t.Helper()
	for _, g := range gs {
		if g.Key == key {
			return g
		}
	}
	t.Fatalf("⚠️ 分组里没有 %q：%+v", key, gs)
	return margin.GroupResult{}
}

// TestMarginGroupsFollowsTheLatestValuation 钉住 MarginGroups：给的是**最近一次计价**的分组分解，
// 多空两边分开、组键随 SideScope，空仓时为空；结算之后给的是**次日**那一份。
//
// ⚠️ 它是 F8 的出口：对拍那侧此前自己把持仓翻译成 margin.Leg（fixture.MarginOf），
// 而今昨、逐笔、大边这几个维度正是在那层翻译里被压没的。
func TestMarginGroupsFollowsTheLatestValuation(t *testing.T) {
	s := newSim(t) // CTP 口径：SideScope = ByProduct
	if gs := s.MarginGroups(); len(gs) != 0 {
		t.Errorf("⚠️ 空仓时分组应当为空，得到 %+v", gs)
	}
	mark(t, s, "DCE.m2701", "3400", "3422")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3399", 2)); err != nil {
		t.Fatal(err)
	}
	gs := s.MarginGroups()
	if len(gs) != 1 {
		t.Fatalf("⚠️ 一个品种一组，得到 %+v", gs)
	}
	// CTP 预设按品种合并 ⇒ 组键是品种，不是合约
	if gs[0].Key != "DCE.m" {
		t.Errorf("⚠️ ByProduct 下组键应为品种 DCE.m，得到 %q —— 逐合约逐方向的数在这个口径下没有定义", gs[0].Key)
	}
	g := groupOf(t, gs, "DCE.m")
	if !g.LongCompany.Equal(s.Account().CurrMargin) || !g.ShortCompany.IsZero() {
		t.Errorf("⚠️ 只有多头时，多头那边应等于账户占用 %s，得到 多 %s / 空 %s", s.Account().CurrMargin, g.LongCompany, g.ShortCompany)
	}
	// 再开一笔空头：两边都非零，合计仍等于账户占用（本批 MaxMarginSide 为假 ⇒ 不走大边）
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Sell, types.Open, "3399", 1)); err != nil {
		t.Fatal(err)
	}
	g = groupOf(t, s.MarginGroups(), "DCE.m")
	if !g.LongCompany.Add(g.ShortCompany).Equal(s.Account().CurrMargin) || !g.Discriminating() {
		t.Errorf("⚠️ 多空都有时 多 %s + 空 %s 应等于账户占用 %s（未启用大边），且这一组要能分开大边：%v",
			g.LongCompany, g.ShortCompany, s.Account().CurrMargin, g.Discriminating())
	}

	// 结算之后给的是**次日**那一份：账户占用也换成了次日的，两者要对得上
	if err := s.Settle(simDay, settlePx(t, "DCE.m2701", "3384"), simNext); err != nil {
		t.Fatal(err)
	}
	g = groupOf(t, s.MarginGroups(), "DCE.m")
	if !g.LongCompany.Add(g.ShortCompany).Equal(s.Account().CurrMargin) {
		t.Errorf("⚠️ 结算后分组合计 %s 与次日账户占用 %s 不同 —— 给的还是结算前那一份？",
			g.LongCompany.Add(g.ShortCompany), s.Account().CurrMargin)
	}

	// 恢复出来的模拟器也答得出（分组不进存档，按存档的持仓与计价重算）
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(Config{Day: simNext, Rules: simRules(t), Choices: ctpChoices()}, st)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := groupOf(t, r.MarginGroups(), "DCE.m"), g; !got.LongCompany.Equal(want.LongCompany) || !got.ShortCompany.Equal(want.ShortCompany) {
		t.Errorf("⚠️ 恢复之后的分组 多 %s / 空 %s，与恢复前 多 %s / 空 %s 不同", got.LongCompany, got.ShortCompany, want.LongCompany, want.ShortCompany)
	}
}

// TestMarginGroupsKeyFollowsSideScope 钉住组键随口径变：按合约合并时组键是合约 —— 对拍要的逐合约逐方向只在这一档有定义。
func TestMarginGroupsKeyFollowsSideScope(t *testing.T) {
	ch := ctpChoices()
	ch.SideScope = margin.ByInstrument
	s, err := New(Config{Day: simDay, PreBalance: dec("100000"), Rules: simRules(t), Choices: ch})
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, "DCE.m2701", "3400", "3422")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3399", 1)); err != nil {
		t.Fatal(err)
	}
	gs := s.MarginGroups()
	if len(gs) != 1 || gs[0].Key != "DCE.m2701" {
		t.Errorf("⚠️ ByInstrument 下组键应为合约 DCE.m2701，得到 %+v", gs)
	}
}

// TestMarginGroupsIsACopy 钉住返回的是副本：改它不影响模拟器下一次的回答。
func TestMarginGroupsIsACopy(t *testing.T) {
	s := newSim(t)
	mark(t, s, "DCE.m2701", "3400", "3422")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3399", 1)); err != nil {
		t.Fatal(err)
	}
	gs := s.MarginGroups()
	want := gs[0].LongCompany
	gs[0].LongCompany = dec("999999")
	if got := s.MarginGroups()[0].LongCompany; !got.Equal(want) {
		t.Errorf("⚠️ 改了返回的切片之后模拟器答 %s，应仍是 %s —— 返回的必须是副本", got, want)
	}
}
