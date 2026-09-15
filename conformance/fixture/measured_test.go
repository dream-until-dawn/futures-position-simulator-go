package fixture

import (
	"os"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/shopspring/decimal"
)

// TestMeasuredRulesFileLoads 钉住入库的实测规则：五个品种的保证金率与手续费率、两个合约的 PositionDateType，
// 以及分类标记与 probes.md §10.2 一致（rb / m 跨月份判过，i / cu / ag 只有一个月份）。
func TestMeasuredRulesFileLoads(t *testing.T) {
	f, err := os.Open(measuredRulesPath())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := LoadMeasuredRules(f)
	if err != nil {
		t.Fatal(err)
	}
	wantMargin := map[string]string{"rb": "0.07", "m": "0.07", "i": "0.11", "cu": "0.11", "ag": "0.22"}
	for p, w := range wantMargin {
		if got, ok := r.MarginByProduct[p]; !ok || !got.Equal(decimal.RequireFromString(w)) {
			t.Errorf("%s 保证金率 %s（有 %v），期望 %s", p, got, ok, w)
		}
	}
	for p := range wantMargin {
		c, ok := r.CommissionByProduct[p]
		if !ok {
			t.Errorf("⚠️ %s 有保证金率而没有手续费率 —— cmd/oracle 的 BuildSpecs 会把它整个跳过", p)
			continue
		}
		if wantClassified := p == "rb" || p == "m"; c.Classified != wantClassified {
			t.Errorf("⚠️ %s classified=%v，probes.md §10.2 是 %v", p, c.Classified, wantClassified)
		}
	}
	if len(r.CommissionByProduct) != len(r.MarginByProduct) {
		t.Errorf("手续费率 %d 个品种、保证金率 %d 个 —— 两边品种集合不同时 BuildSpecs 只剩交集", len(r.CommissionByProduct), len(r.MarginByProduct))
	}
	if r.PositionDate["SHFE.rb2701"] != refdata.UseHistory || r.PositionDate["DCE.m2701"] != refdata.NoUseHistory {
		t.Errorf("PositionDateType 与 kq_facts 24 不符：%v", r.PositionDate)
	}
}

// TestMeasuredRulesRefuseMalformed 断言读不成就报错，不给默认值、不静默跳过。
func TestMeasuredRulesRefuseMalformed(t *testing.T) {
	margin := `"margin_rate_by_product": [{"product": "rb", "rate": "0.07", "evidence": "e"}]`
	posDate := `"position_date_type": [{"instrument": "SHFE.rb2701", "type": "use_history", "evidence": "e"}]`
	comm := func(row string) string { return `"commission_rate_by_product": [` + row + `]` }
	good := `{"product": "rb", "by_money": "0.00001", "by_volume": "0", "classified": true, "calibrated_on": "SHFE.rb2701", "evidence": "e"}`
	doc := func(parts ...string) string { return "{" + strings.Join(parts, ",") + "}" }

	if _, err := LoadMeasuredRules(strings.NewReader(doc(margin, comm(good), posDate))); err != nil {
		t.Fatalf("前提：合法的文件要读得出：%v", err)
	}
	for _, c := range []struct {
		name, body, want string
	}{
		{"手续费没有 evidence", doc(margin, comm(strings.Replace(good, `"evidence": "e"`, `"evidence": " "`, 1)), posDate), "没有 evidence"},
		{"手续费没写标定合约", doc(margin, comm(strings.Replace(good, `"SHFE.rb2701"`, `""`, 1)), posDate), "标定用的合约"},
		{"手续费没写 classified", doc(margin, comm(strings.Replace(good, `"classified": true, `, ``, 1)), posDate), "classified"},
		{"按额按手都非零", doc(margin, comm(strings.Replace(good, `"by_volume": "0"`, `"by_volume": "1"`, 1)), posDate), "同时为零或同时非零"},
		{"按额按手都为零", doc(margin, comm(strings.Replace(good, `"0.00001"`, `"0"`, 1)), posDate), "同时为零或同时非零"},
		{"手续费为负", doc(margin, comm(strings.Replace(good, `"0.00001"`, `"-0.00001"`, 1)), posDate), "非负数"},
		{"手续费品种出现两次", doc(margin, comm(good+","+good), posDate), "手续费率出现两次"},
		{"没有手续费段", doc(margin, posDate), "手续费率 0 条"},
		{"保证金品种出现两次", doc(`"margin_rate_by_product": [{"product": "rb", "rate": "0.07", "evidence": "e"},{"product": "rb", "rate": "0.08", "evidence": "e"}]`, comm(good), posDate), "保证金率出现两次"},
		{"PositionDateType 出现两次", doc(margin, comm(good), `"position_date_type": [{"instrument": "SHFE.rb2701", "type": "use_history", "evidence": "e"},{"instrument": "SHFE.rb2701", "type": "no_use_history", "evidence": "e"}]`), "PositionDateType 出现两次"},
		{"保证金率为零", doc(strings.Replace(margin, `"0.07"`, `"0"`, 1), comm(good), posDate), "不是正数"},
		{"令牌不认识", doc(margin, comm(good), strings.Replace(posDate, "use_history", "usehistory", 1)), "不认识"},
		{"未知字段", doc(margin, comm(good), posDate, `"extra": 1`), "读实测规则失败"},
	} {
		_, err := LoadMeasuredRules(strings.NewReader(c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("⚠️ %s：应当报错（含 %q），得到 %v", c.name, c.want, err)
		}
	}
}
