package futsim

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestOutwardDoesNotBecomeMeasured 通用取整能力不得自动提升为市场实测规则。
func TestOutwardDoesNotBecomeMeasured(t *testing.T) {
	rules := MeasuredTickRounding()
	if len(rules) != 2 || rules[types.SHFE] != refdata.TickFloor || rules[types.DCE] != refdata.TickInward {
		t.Fatal("通用向外能力改变既有实测映射")
	}
	if _, ok := rules[types.CZCE]; ok {
		t.Fatal("郑商所被自动加入实测映射")
	}
}
