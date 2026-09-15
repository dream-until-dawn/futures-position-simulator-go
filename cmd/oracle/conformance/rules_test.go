package conformance

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance/fixture"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/shopspring/decimal"
)

// TestBuildSpecsNeedsEveryMeasuredPiece 钉住 BuildSpecs 缺任何一样实测规则就不给这个合约规格（调用方记进「没登记规则数据」跳过），
// 齐全时手续费率照实带上（F7a：`-carry` 走门面记账要它）。
func TestBuildSpecsNeedsEveryMeasuredPiece(t *testing.T) {
	d := decimal.RequireFromString
	comm := refdata.CommissionRates{OpenByMoney: d("0.00001"), CloseByMoney: d("0.00001"), CloseTodayByMoney: d("0.00001")}
	rules := MeasuredRules{
		MarginByProduct:     map[string]decimal.Decimal{"rb": d("0.07"), "m": d("0.07"), "i": d("0.11")},
		CommissionByProduct: map[string]fixture.MeasuredCommission{"rb": {Rates: comm, Classified: true, CalibratedOn: "SHFE.rb2701"}, "i": {Rates: comm}},
		PositionDate: map[string]refdata.PositionDateType{
			"SHFE.rb2701": refdata.UseHistory, "DCE.m2701": refdata.NoUseHistory, "DCE.i2701": refdata.NoUseHistory,
		},
	}
	specs := map[string]fixture.ContractSpec{
		"SHFE.rb2701": {Instrument: "SHFE.rb2701", Product: "rb", VolumeMultiple: d("10")},
		"DCE.m2701":   {Instrument: "DCE.m2701", Product: "m", VolumeMultiple: d("10")},   // 没有手续费率
		"DCE.i2701":   {Instrument: "DCE.i2701", Product: "i", VolumeMultiple: d("0")},    // 乘数不为正
		"SHFE.cu2701": {Instrument: "SHFE.cu2701", Product: "cu", VolumeMultiple: d("5")}, // 没有 PositionDateType
	}
	out := BuildSpecs(specs, rules)
	if len(out) != 1 {
		t.Fatalf("⚠️ 只有 rb2701 齐全，得到 %d 个规格：%v", len(out), out)
	}
	rb, ok := out["SHFE.rb2701"]
	if !ok {
		t.Fatal("rb2701 齐全却没有规格")
	}
	if !rb.Commission.OpenByMoney.Equal(d("0.00001")) || !rb.Margin.LongByMoney.Equal(d("0.07")) || rb.PositionDate != refdata.UseHistory || !rb.Multiplier.Equal(d("10")) {
		t.Errorf("⚠️ rb2701 规格没照实带上实测规则：%+v", rb)
	}
	if _, ok := out["DCE.m2701"]; ok {
		t.Error("⚠️ m2701 没有手续费率却给了规格 —— 门面记账会拿零费率顶上")
	}
}
