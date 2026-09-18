package main

import (
	"reflect"
	"testing"

	"github.com/shopspring/decimal"
)

// TestSettleRealPairsKeepTheirVerdicts 钉住两对**真截面**上的结算判定，从读截面一路走到判定（评审 20260918 nit）。
//
// 此前这两个结论只写在文档里：TestSettleVerdictIsEqualityNotNearest 用的是合成的费，
// 将来改 settleInputFrom（过夜腿读法、多合约行情）时，这两个结论可能悄悄变了，没人知道。
//
//	20260917 → 20260918   残差 +0.028   五候选里只命中 (i-t)；七候选（带粒度）里 (i-t)/(i-o)/(i-l) 同值命中（一手单）
//	20260918 → 20260921   残差 +0.032   七候选一个都不中 ⇒ (ii)
func TestSettleRealPairsKeepTheirVerdicts(t *testing.T) {
	d := decimal.RequireFromString
	ten := decimal.NewFromInt(10)
	cases := []struct {
		name, end, next string
		mult            map[string]decimal.Decimal
		orders          []settleOrder
		pred, residual  string
		want5, want7    []string
	}{
		{
			name: "20260917→20260918", end: "ctp-status-20260917.json", next: "ctp-slices-20260918.json",
			mult: map[string]decimal.Decimal{"CZCE.MA701": ten},
			orders: []settleOrder{{d("0.2"), 1, 1}, {d("0.2"), 1, 1}, {d("2"), 1, 1}, {d("2"), 1, 1},
				{d("12.455"), 1, 1}, {d("12.449"), 1, 1}, {d("12.455"), 1, 1}, {d("12.452"), 1, 1}, {d("12.455"), 1, 1}, {d("12.452"), 1, 1}},
			pred: "19997514.852", residual: "0.028",
			want5: []string{"i-t"}, want7: []string{"i-t", "i-o", "i-l"},
		},
		{
			name: "20260918→20260921", end: "ctp-status-20260918.json", next: "ctp-status-20260921.json",
			mult: map[string]decimal.Decimal{"DCE.m2701": ten, "CZCE.MA701": ten},
			orders: []settleOrder{{d("2"), 1, 1}, {d("2"), 1, 1}, {d("6"), 1, 1}, {d("2"), 1, 1}, {d("0.2"), 1, 1}, {d("2"), 1, 1},
				{d("23.59"), 2, 1}, {d("23.578"), 2, 1}, {d("23.578"), 2, 1}, {d("23.566"), 2, 1}},
			pred: "19995806.368", residual: "0.032",
			want5: nil, want7: nil,
		},
	}
	for _, c := range cases {
		in, err := settleInputFrom(loadCTPFixture(t, c.end), loadCTPFixture(t, c.next), c.mult)
		if err != nil {
			t.Fatalf("%s：%v", c.name, err)
		}
		fees, sum := []decimal.Decimal{}, decimal.Zero
		for _, o := range c.orders {
			fees = append(fees, o.Fee)
			sum = sum.Add(o.Fee)
		}
		if !sum.Equal(in.Commission.Round(6)) {
			t.Fatalf("%s：逐单费合计 %s ≠ 账户手续费 %s", c.name, sum, in.Commission.Round(6))
		}
		pred, res := settleResidual(in)
		if !pred.Equal(d(c.pred)) || !res.Round(6).Equal(d(c.residual)) {
			t.Errorf("⚠️ %s：推算 %s、残差 %s，登记的是 %s / %s", c.name, pred, res.Round(6), c.pred, c.residual)
		}
		if got := settleVerdict(res, settleRoundingResiduals(fees)); !reflect.DeepEqual(got, c.want5) {
			t.Errorf("⚠️ %s：五候选判定 %v，登记的是 %v", c.name, got, c.want5)
		}
		g, err := settleGranularityResiduals(c.orders)
		if err != nil {
			t.Fatal(err)
		}
		if got := settleVerdictAmong(settleGranularityCandidates, res, g); !reflect.DeepEqual(got, c.want7) {
			t.Errorf("⚠️ %s：七候选判定 %v，登记的是 %v", c.name, got, c.want7)
		}
	}
}
