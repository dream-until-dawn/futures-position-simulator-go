package futsim

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestAcceptedRequestDirection 不跑柜台八项校验不等于接受无法解释的委托方向。
func TestAcceptedRequestDirection(t *testing.T) {
	s := f13Sim(t, "DCE.m2701", "3000")
	before := rewriteState(t, s)
	r := req(t, "DCE.m2701", types.Buy, types.Close, "3000", 1)
	r.Direction = types.Direction(0)
	if _, err := s.PlaceAccepted(simNext, "invalid", r); err == nil || !strings.Contains(err.Error(), "方向") {
		t.Fatalf("非法方向被接受或拒错原因：%v", err)
	}
	if rewriteState(t, s) != before {
		t.Fatal("非法方向拒绝后留下冻结或序号")
	}
}

// TestRestoreRefusesInvalidOrderDirection 正常裸平存档可恢复，篡改为未知方向必须明确拒绝。
func TestRestoreRefusesInvalidOrderDirection(t *testing.T) {
	s := f13Sim(t, "DCE.m2701", "3000")
	if _, err := s.PlaceAccepted(simNext, "valid", req(t, "DCE.m2701", types.Sell, types.Close, "3000", 1)); err != nil {
		t.Fatal(err)
	}
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	cfg := submitCfg(t, simNext)
	if _, err = Restore(cfg, st); err != nil {
		t.Fatalf("正对照恢复失败：%v", err)
	}
	for _, direction := range []types.Direction{0, 9} {
		bad := st
		bad.Orders = append([]OrderState(nil), st.Orders...)
		bad.Orders[0].Request.Direction = direction
		if _, err = Restore(cfg, bad); err == nil || !strings.Contains(err.Error(), "方向") {
			t.Fatalf("非法方向存档被接受或拒错原因：%d %v", direction, err)
		}
	}
}

// TestRestoreRefusesNonPositiveOrderPrice 费用和保证金采用昨结算价时，零挂单价仍然不合法。
func TestRestoreRefusesNonPositiveOrderPrice(t *testing.T) {
	cfg := submitCfg(t, simDay)
	cfg.Choices.FeeBasis = fee.PreSettlement
	cfg.Choices.FreezeMargin = order.FreezeAtPreSettlement
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, "DCE.m2701", "3000", "3000")
	if _, err = s.PlaceAccepted(simDay, "valid", req(t, "DCE.m2701", types.Buy, types.Open, "3000", 1)); err != nil {
		t.Fatal(err)
	}
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Restore(cfg, st); err != nil {
		t.Fatalf("正对照存档不能恢复：%v", err)
	}
	for _, price := range []string{"0", "-1"} {
		bad := st
		bad.Orders = append([]OrderState(nil), st.Orders...)
		bad.Orders[0].Request.Price = dec(price)
		if _, err = Restore(cfg, bad); err == nil || !strings.Contains(err.Error(), "价格") {
			t.Fatalf("非正挂单价存档被接受或拒错原因：%s %v", price, err)
		}
	}
}
