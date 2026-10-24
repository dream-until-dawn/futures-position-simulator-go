package exchange

import (
	"fmt"
	"strings"
	"testing"
)

// TestOfflineSHFECloseIsOptional 验证缺失旁证不会删除有效结算行；无效结算仍须拒绝。
func TestOfflineSHFECloseIsOptional(t *testing.T) {
	for _, close := range []string{`""`, `null`, `0`, `"invalid"`, `"101"`} {
		input := fmt.Sprintf(`{"report_date":"20260907","o_curinstrument":[{"PRODUCTID":"rb_f","PRODUCTCLASS":"1","DELIVERYMONTH":"2701","SETTLEMENTPRICE":"100","CLOSEPRICE":%s}]}`, close)
		rows, rep, err := ParseSHFE(strings.NewReader(input), day20260907())
		row, ok := rows["SHFE.rb2701"]
		if err != nil || !ok || rep.Kept != 1 || row.Settlement.String() != "100" || row.HasClose != (close == `"101"`) {
			t.Fatalf("缺收盘价丢失结算行：close=%s rows=%v report=%v err=%v", close, rows, rep, err)
		}
	}
	input := `{"report_date":"20260907","o_curinstrument":[{"PRODUCTID":"rb_f","PRODUCTCLASS":"1","DELIVERYMONTH":"2701","SETTLEMENTPRICE":"","CLOSEPRICE":"101"}]}`
	rows, rep, err := ParseSHFE(strings.NewReader(input), day20260907())
	if err == nil && (len(rows) != 0 || rep.Kept != 0) {
		t.Fatal("收盘价替代了缺失结算价")
	}
}
