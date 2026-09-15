package futsim

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestPlaceAcceptedSkipsValidation 钉住 PlaceAccepted 不跑八项：Place 拒掉的零头价位单（快期照收，kq_facts 45）它记得进，
// 冻结与 FreezeOf 相同，撤单后账户逐字段回到挂单前。
func TestPlaceAcceptedSkipsValidation(t *testing.T) {
	s := submitSim(t, ctpChoices(), "1000000")
	odd := req(t, "SHFE.ag2702", types.Buy, types.Open, "15460.5", 1) // ag2702 最小变动价位 1
	var rej *match.RejectedError
	if _, err := s.Place(simDay, wall(t, "2026-09-15 10:00"), "p", odd); !errors.As(err, &rej) || rej.Rejection.Check != order.CheckPriceTick {
		t.Fatalf("前提：零头价位单 Place 要拒在最小变动价位：%v", err)
	}
	before := s.Account()
	want, err := s.FreezeOf(simDay, odd)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.PlaceAccepted(simDay, "a", odd)
	if err != nil {
		t.Fatalf("⚠️ 柜台已接受的零头价位单记不进来：%v", err)
	}
	acc := s.Account()
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) || !acc.FrozenMargin.Equal(want.Margin) || !acc.FrozenCommission.Equal(want.Commission) {
		t.Errorf("⚠️ PlaceAccepted 冻 %+v、账户冻 %s / %s，FreezeOf 算的 %+v", got, acc.FrozenMargin, acc.FrozenCommission, want)
	}
	if live := s.Live(); len(live) != 1 || live[0] != "a" {
		t.Errorf("簿上应当只有 a：%v", live)
	}
	if err := s.Cancel(simDay, "a"); err != nil {
		t.Fatal(err)
	}
	if !sameSnapshot(before, s.Account()) {
		t.Errorf("撤单后账户没回到挂单前：\n%+v\n%+v", before, s.Account())
	}
}

// TestPlaceAcceptedKeepsBooksConsistent 钉住不校验也要守的两条：冻住的手数（连同簿上已冻的）不超过持仓、冻结额不超过可用。
// 以及价格不为正、重复编号。任何一条失败，状态不动。
//
// ⚠️ 第二笔平今是判别点：单笔各自不超、合计超 —— 只拿这一笔与持仓比的守卫放得过去。
func TestPlaceAcceptedKeepsBooksConsistent(t *testing.T) {
	s := rbTodayAndHistory(t, kqChoices()) // 多 今 1 / 昨 3
	closeToday := req(t, "SHFE.rb2701", types.Sell, types.CloseToday, "3010", 1)
	if _, err := s.PlaceAccepted(simNext, "t1", closeToday); err != nil {
		t.Fatal(err)
	}
	after := s.Account()
	for _, c := range []struct {
		name, id, want string
		r              order.Request
	}{
		{"合计超今仓", "t2", "两边持仓不一致", closeToday},
		{"单笔超昨仓", "y4", "两边持仓不一致", req(t, "SHFE.rb2701", types.Sell, types.CloseYesterday, "3010", 4)},
		{"平空头而无空仓", "s1", "两边持仓不一致", req(t, "SHFE.rb2701", types.Buy, types.CloseYesterday, "3010", 1)},
		{"价格为零", "z", "不为正", req(t, "SHFE.rb2701", types.Buy, types.Open, "0", 1)},
		{"重复编号", "t1", "已经在簿上", req(t, "SHFE.rb2701", types.Buy, types.Open, "3010", 1)},
		{"资金不够", "big", "两边资金不一致", req(t, "SHFE.rb2701", types.Buy, types.Open, "3010", 900)},
	} {
		_, err := s.PlaceAccepted(simNext, c.id, c.r)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("⚠️ %s：要报「%s」，得到 %v", c.name, c.want, err)
		}
		if live := s.Live(); len(live) != 1 || !sameSnapshot(after, s.Account()) {
			t.Errorf("%s：被拒之后状态变了：簿 %v", c.name, live)
		}
	}
	// 反向：撤掉 t1 之后同一笔平今记得进 —— 上面「合计超今仓」拒的是合计，不是这笔单本身
	if err := s.Cancel(simNext, "t1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlaceAccepted(simNext, "t2", closeToday); err != nil {
		t.Errorf("撤掉 t1 之后平今 1 手要记得进：%v", err)
	}
}

// TestRestoreChecksAsYesterdaySplit 钉住：记作平昨的裸 CLOSE 挂单，存档里的今 / 昨拆分也要核。
//
// ⚠️ 簿按开平标志重算手数，而这笔单在簿上仍是 Close ⇒ 拆分取的是存档自己的数；
// 金额核对也分不开（记作平昨时手续费与拆分无关）。只改拆分（冻昨 1 → 冻今 1）而不核手数，恢复出来的簿会冻错一边。
func TestRestoreChecksAsYesterdaySplit(t *testing.T) {
	s := rbTodayAndHistory(t, kqChoices())
	if _, err := s.PlaceAccepted(simNext, "k", req(t, "SHFE.rb2701", types.Sell, types.Close, "3010", 1)); err != nil {
		t.Fatal(err)
	}
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Day: simNext, Rules: simRules(t), Choices: kqChoices()}
	if _, err := Restore(cfg, roundTrip(t, st)); err != nil {
		t.Fatalf("⚠️ 合法存档恢复不了：%v", err)
	}
	bad := roundTrip(t, st)
	bad.Orders[0].Frozen.VolumeToday, bad.Orders[0].Frozen.VolumeHistory = 1, 0
	if _, err := Restore(cfg, bad); err == nil || !strings.Contains(err.Error(), "与按委托重算的（今") {
		t.Errorf("⚠️ 把记作平昨的裸 CLOSE 改成冻今 1，恢复要被拒：%v", err)
	}
}
