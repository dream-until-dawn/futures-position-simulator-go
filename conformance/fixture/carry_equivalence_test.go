package fixture

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestReconstructMatchesFacade 钉住留下的第二份实现：Reconstruct（`cmd/oracle -carry` 还在用）与门面上的 ReconstructOnFacade
// 在全部跨日语料上逐片相同（开仓价、逐日盯市基线、昨仓标记）。
//
// ⚠️ 为什么留着它、为什么这样守（design.md「门面的形状」§10「F6b 修订」）：对拍测试已改走门面，
// `cmd/oracle` 的规格没有手续费率、门面记账要它 ⇒ Reconstruct 暂留（删它登记 F7）。两份实现并存时——
//
//	门面退化        对柜台的对拍红（crossday / reconstruct / fixture_test）
//	Reconstruct 漂离  本条红
//	两份同样退化    对拍在门面上，照样红
//
// F6b 迁移前这是一次性探针（98 组相同；给门面的结算价 +1 → 98 组全不同，第八项口径置零 → 40 组不同），迁移后转正。
// 输入覆盖两处调用点实际喂的：reconstruct_test（固定取 status-20260908-8）与 carryStartFor（取上一交易日采集最晚、有成交的那份）。
func TestReconstructMatchesFacade(t *testing.T) {
	all := loadAll(t)
	byPath := map[string]*Fixture{}
	for _, f := range all {
		byPath[f.Path] = f
	}
	const sym = "SHFE.rb2701"
	settle, ok := settlementOf(t, sym)
	if !ok {
		t.Fatalf("⚠️ 上期所日行情里没有 %s 的结算价", sym)
	}
	fixed, ok := byPath["status-20260908-8.json"]
	if !ok {
		t.Fatal("找不到 status-20260908-8.json")
	}
	same := map[string]int{}
	check := func(kind string, prev, cur *Fixture) {
		spec, ok := specForSymbol(t, prev, sym)
		if !ok {
			t.Fatalf("%s 没有登记规格", sym)
		}
		old, oerr := Reconstruct(prev, cur, sym, types.Speculation, positionDateOf(t, sym), settle)
		got, gerr := ReconstructOnFacade(prev, cur, sym, spec, positionDateOf(t, sym), settle, cur.TradingDay)
		switch {
		case oerr != nil || gerr != nil:
			t.Errorf("⚠️ %s %s ← %s：Reconstruct %v / 门面 %v —— 两边都该算得出（迁移时 98 组全算得出）", kind, cur.Path, prev.Path, oerr, gerr)
		case signature(old) != signature(got):
			t.Errorf("⚠️ %s %s ← %s：两份实现分岔了\nReconstruct %s\n门面        %s", kind, cur.Path, prev.Path, signature(old), signature(got))
		default:
			same[kind]++
		}
	}
	for _, f := range all {
		if f.TradingDay.String() != "20260909" {
			continue
		}
		if _, ok := f.Positions[sym]; !ok {
			continue
		}
		check("reconstruct", fixed, f)
		if !f.HasHistoryPosition(sym) {
			continue
		}
		src, ok, why := carryStartFor(t, all, f, sym)
		if !ok {
			t.Errorf("⚠️ %s 带昨仓却找不到结转来源：%s", f.Path, why)
			continue
		}
		check("carrywire", src.prev, f)
	}
	t.Logf("逐片相同：%v", same)
	// ⚠️ 棘轮：迁移时两类各 49 组。少了不报错就是覆盖悄悄变小
	if same["reconstruct"] < 49 || same["carrywire"] < 49 {
		t.Errorf("⚠️ 逐片相同的组数 %v 少于迁移时的各 49 组 —— 覆盖变小了", same)
	}
}
