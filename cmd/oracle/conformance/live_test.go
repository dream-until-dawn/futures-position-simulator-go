package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance/fixture"
	"github.com/shopspring/decimal"
)

func repoFile(t *testing.T, parts ...string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join(append([]string{"..", "..", ".."}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestCompareCarriesThroughFacade 钉住 `-carry` 那条路（F7b 起走 ReconstructOnFacade）：用入库的真实夹具当「实时截面」，
// 带昨仓的 rb2701 在给了前一日夹具与交易所结算价时被结转后比上；不给时记进「带昨仓跳过」，不假装比过。
//
// ⚠️ F7b 之前这条路一条测试都没有 —— 换实现（Reconstruct → 门面）时没有任何东西会红。
func TestCompareCarriesThroughFacade(t *testing.T) {
	specs, err := fixture.LoadSpecs(repoFile(t, "testdata", "refdata", "specs-20260908.json"))
	if err != nil {
		t.Fatal(err)
	}
	rules, err := LoadMeasuredRules(repoFile(t, "testdata", "refdata", "measured-rules-20260909.json"))
	if err != nil {
		t.Fatal(err)
	}
	built := BuildSpecs(specs, rules)
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "probes", "status-20260909-2.json"))
	if err != nil {
		t.Fatal(err)
	}
	prev, err := fixture.Load(repoFile(t, "testdata", "probes", "status-20260908-8.json"), "status-20260908-8.json")
	if err != nil {
		t.Fatal(err)
	}
	carry := &Carry{Prev: prev, Settlement: map[string]decimal.Decimal{"SHFE.rb2701": decimal.RequireFromString("3163")}}

	res, err := Compare(raw, built, carry)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(res.Symbols, "SHFE.rb2701（结转）") {
		t.Errorf("⚠️ 给了前一日夹具与结算价，rb2701 应当结转后比上：比了 %v，带昨仓跳过 %v", res.Symbols, res.SkippedHistory)
	}
	// ⚠️ 参数也要传对，不只是接上（评审 20260915）：结算价接错（例如 +50）时 position_price_long 变了，
	// 但那个字段对柜台本来就判「失败」（已登记的口子：柜台给收盘价），Report 的计数与 NovelFailures 都不变 ⇒ 直接断言本库的值等于传入的交易所结算价。
	// 这份截面上只有 rb2701 被比到，字段名唯一。
	// ⚠️ 两个结构性盲区（这份样本上分不出来，不处理）：dateType 换成 NoUseHistory 结果逐字段不变（门面按 §13 #20 一律滚成昨仓）；
	// cur 传 nil 结果不变（status-20260909-2 里 rb2701 当天没有成交）
	found := false
	for _, fd := range res.Report.Fields {
		if fd.Name != "position_price_long" {
			continue
		}
		found = true
		if fd.LibraryAbsent || !fd.Library.Equal(decimal.RequireFromString("3163")) {
			t.Errorf("⚠️ 结转后本库 position_price_long = %s（无值 %v），应等于传入的交易所结算价 3163 —— 结算价接错了", fd.Library, fd.LibraryAbsent)
		}
	}
	if !found {
		t.Error("⚠️ Report 里没有 position_price_long —— 断言空转")
	}

	without, err := Compare(raw, built, nil)
	if err != nil && !strings.Contains(err.Error(), "一个合约都没比到") {
		t.Fatal(err)
	}
	if contains(without.Symbols, "SHFE.rb2701（结转）") || !hasPrefix(without.SkippedHistory, "SHFE.rb2701") {
		t.Errorf("⚠️ 不给 -carry 时 rb2701 要记进带昨仓跳过：比了 %v，跳过 %v", without.Symbols, without.SkippedHistory)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func hasPrefix(xs []string, p string) bool {
	for _, x := range xs {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}
