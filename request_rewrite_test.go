package futsim

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestRewriteScopeAndIdempotence 在各维度逐项改变前提，防把委托规则外推到所有平仓。
func TestRewriteScopeAndIdempotence(t *testing.T) {
	base := req(t, "DCE.m2701", types.Sell, types.CloseToday, "3000", 1)
	for _, ex := range []types.Exchange{types.DCE, types.CZCE, types.SHFE, types.INE, types.GFEX, types.CFFEX} {
		for _, dt := range []refdata.PositionDateType{refdata.NoUseHistory, refdata.UseHistory, refdata.PositionDateUnknown} {
			for _, hedge := range []types.HedgeFlag{types.Speculation, types.Hedge, types.Arbitrage, types.HedgeUnknown} {
				for _, off := range []types.Offset{types.CloseToday, types.CloseYesterday, types.Close, types.Open, types.ForceClose, types.ForceOff, types.LocalForceClose} {
					r := base
					r.Instrument.Exchange, r.Hedge, r.Offset = ex, hedge, off
					inst := refdata.Instrument{ID: r.Instrument, PositionDateType: dt}
					got, changed := rewriteCloseToday(inst, r)
					want := r
					eligible := (ex == types.DCE || ex == types.CZCE) && dt == refdata.NoUseHistory && hedge == types.Speculation && off == types.CloseToday
					if eligible {
						want.Offset = types.Close
					}
					if got != want || changed != eligible {
						t.Fatalf("委托改写范围错误：%v %v %v %v => %+v", ex, dt, hedge, off, got)
					}
					again, changedAgain := rewriteCloseToday(inst, got)
					if again != got || changedAgain {
						t.Fatal("委托改写不幂等")
					}
				}
			}
		}
	}
}

// TestRewriteCancelAndRollback 核撤单释放、重复撤单和 Fill 可恢复失败后的完整状态。
func TestRewriteCancelAndRollback(t *testing.T) {
	s := f13Sim(t, "DCE.m2701", "3000")
	r := req(t, "DCE.m2701", types.Sell, types.CloseToday, "3010", 1)
	for _, id := range []string{"first", "second"} {
		if _, err := s.PlaceAccepted(simNext, id, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Cancel(simNext, "first"); err != nil {
		t.Fatal(err)
	}
	before := rewriteState(t, s)
	if err := s.Cancel(simNext, "first"); err == nil {
		t.Fatal("重复撤单成功")
	}
	if rewriteState(t, s) != before {
		t.Fatal("重复撤单修改状态")
	}
	// 撤掉前一笔后剩余订单仍冻今；成交可从已释放的昨仓开始消费。
	if _, err := s.Fill(simNext, "second"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Position(r.Instrument, r.Hedge)
	if p.VolumeHistory(types.Buy) != 0 || p.VolumeToday(types.Buy) != 1 {
		t.Fatal("撤单后成交未按当前可用仓先平昨")
	}

	s = f13Sim(t, "DCE.m2701", "3000")
	if _, err := s.PlaceAccepted(simNext, "fail", r); err != nil {
		t.Fatal(err)
	}
	px := s.prices[r.Instrument]
	px.hasLast = false
	s.prices[r.Instrument] = px
	before = rewriteState(t, s)
	if _, err := s.Fill(simNext, "fail"); err == nil || !strings.Contains(err.Error(), "缺计价输入") {
		t.Fatalf("故障前提不成立：%v", err)
	}
	if rewriteState(t, s) != before || s.broken != nil {
		t.Fatal("改写挂单成交失败后未完整回滚")
	}
}

// TestRewritePreservesTradeInput 直接灌入已发生成交不受委托改写影响。
func TestRewritePreservesTradeInput(t *testing.T) {
	s := f13Sim(t, "DCE.m2701", "3000")
	tr := trade(t, "DCE.m2701", types.Sell, types.CloseToday, "3010", 1)
	if err := s.ApplyTrade(simNext, tr); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Position(tr.Instrument, tr.Hedge)
	if p.VolumeToday(types.Buy) != 0 || p.VolumeHistory(types.Buy) != 1 {
		t.Fatal("已发生成交被误用委托改写规则")
	}
	if q := s.quotaOf(tr.Instrument, tr.Hedge, types.Buy); q.Explicit != 1 {
		t.Fatal("直接灌入平今丢失显式额度计数")
	}
	tr.Offset = types.Close
	if err := s.ApplyTrade(simNext, tr); err == nil || !strings.Contains(err.Error(), "显式平今扣不扣额度") {
		t.Fatalf("直接灌入后的额度歧义保护丢失：%v", err)
	}
}

// rewriteState 比较完整可持久化状态，不能只比较余额而漏掉持仓、额度或挂单。
func rewriteState(t *testing.T, s *Simulator) string {
	t.Helper()
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRewriteSubmitConsumesHistory 检查今昨消耗、回报及额度；真实不同成本的盈亏由 CTP 夹具测试核对。
func TestRewriteSubmitConsumesHistory(t *testing.T) {
	s := f13Sim(t, "DCE.m2701", "3000")
	r := req(t, "DCE.m2701", types.Sell, types.CloseToday, "3010", 1)
	for i, fee := range []string{"0.75", "1.2"} {
		before := s.Account().Commission
		tr, err := s.Submit(simNext, wall(t, "2026-09-16 10:00"), r)
		if err != nil {
			t.Fatalf("第 %d 笔显式平今应成交：%v", i+1, err)
		}
		if tr.Offset != types.Close {
			t.Errorf("改写成交标志应为 Close，得到 %v", tr.Offset)
		}
		if got := s.Account().Commission.Sub(before); !got.Equal(dec(fee)) {
			t.Errorf("改写平仓手续费 %s，期望 %s", got, fee)
		}
		p, _ := s.Position(r.Instrument, r.Hedge)
		if p.VolumeHistory(types.Buy) != 0 || p.VolumeToday(types.Buy) != 1-i {
			t.Errorf("改写平仓必须先消耗昨仓：今 %d 昨 %d", p.VolumeToday(types.Buy), p.VolumeHistory(types.Buy))
		}
		if q := s.quotaOf(r.Instrument, r.Hedge, types.Buy); q.Explicit != 0 || q.Charged != 1 {
			t.Errorf("改写成交额度错误：%+v", q)
		}
	}
}

// TestRewriteOrdersRoundTrip 覆盖挂单的两种入口、两种成交顺序及存档恢复。
func TestRewriteOrdersRoundTrip(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			s := f13Sim(t, "DCE.m2701", "3000")
			r := req(t, "DCE.m2701", types.Sell, types.CloseToday, "3010", 1)
			before := rewriteState(t, s)
			fr, err := s.FreezeOf(simNext, r)
			if err != nil {
				t.Fatal(err)
			}
			if fr.VolumeHistory != 1 || fr.VolumeToday != 0 {
				t.Fatalf("独立冻结应冻昨仓：%+v", fr)
			}
			if rewriteState(t, s) != before {
				t.Fatal("独立冻结修改了状态")
			}
			for i, id := range []string{"z", "a"} {
				if accepted {
					fr, err = s.PlaceAccepted(simNext, id, r)
				} else {
					fr, err = s.Place(simNext, wall(t, "2026-09-16 10:00"), id, r)
				}
				if err != nil {
					t.Fatal(err)
				}
				stored, got, _ := s.book.Get(id)
				if stored.Offset != types.Close || got.VolumeHistory != 1-i || got.VolumeToday != i || fr.VolumeHistory != got.VolumeHistory {
					t.Fatalf("挂单必须保存有效请求和冻结分片：%+v %+v", stored, got)
				}
				if !fr.Commission.Equal(dec("0.75")) {
					t.Fatal("挂单预占了手续费额度")
				}
			}
			st, err := s.State()
			if err != nil {
				t.Fatal(err)
			}
			restored, err := Restore(submitCfg(t, simNext), roundTrip(t, st))
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{"z", "a"}
			if reverse {
				ids = []string{"a", "z"}
			}
			for i, id := range ids {
				for _, current := range []*Simulator{s, restored} {
					feeBefore := current.Account().Commission
					tr, err := current.Fill(simNext, id)
					if err != nil {
						t.Fatal(err)
					}
					if tr.Offset != types.Close {
						t.Fatal("挂单成交未返回有效标志")
					}
					want := []string{"0.75", "1.2"}[i]
					if !current.Account().Commission.Sub(feeBefore).Equal(dec(want)) {
						t.Fatal("挂单成交未按剩余额度收费")
					}
				}
				if rewriteState(t, s) != rewriteState(t, restored) {
					t.Fatal("恢复后继续成交与原状态不同")
				}
				// 第一笔消耗额度后，余单仍保留挂单时的冻结费；必须能恢复。
				st, err = restored.State()
				if err != nil {
					t.Fatal(err)
				}
				restored, err = Restore(submitCfg(t, simNext), roundTrip(t, st))
				if err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// TestRewriteRejectsWithoutCodesAndTrace 检查有原有码的价格拒因、总可平量和多重违规。
func TestRewriteRejectsWithoutCodesAndTrace(t *testing.T) {
	for _, c := range []struct {
		price  string
		volume int
		check  order.Check
	}{
		{"3000.5", 1, order.CheckPriceTick}, {"3300", 1, order.CheckPriceLimit}, {"2800", 1, order.CheckPriceLimit},
		{"3000", 3, order.CheckClosable}, {"3300.5", 3, order.CheckClosable},
	} {
		for _, place := range []bool{false, true} {
			s := f13Sim(t, "DCE.m2701", "3000")
			before := rewriteState(t, s)
			r := req(t, "DCE.m2701", types.Sell, types.CloseToday, c.price, c.volume)
			var err error
			if place {
				_, err = s.Place(simNext, wall(t, "2026-09-16 10:00"), "r", r)
			} else {
				_, err = s.Submit(simNext, wall(t, "2026-09-16 10:00"), r)
			}
			var rejected *match.RejectedError
			if !errors.As(err, &rejected) || rejected.Rejection.Check != c.check {
				t.Fatalf("改写拒因错误，期望 %v，得到 %v", c.check, err)
			}
			if _, ok := rejected.Code(); ok {
				t.Error("改写拒单不得返回未实测柜台码")
			}
			if rewriteState(t, s) != before {
				t.Fatal("拒单修改了完整状态")
			}
		}
	}
}

// TestRewriteRestoreRefusesLegacySemantics 防字段未变却沿用旧格式，以及伪造新格式原始挂单。
func TestRewriteRestoreRefusesLegacySemantics(t *testing.T) {
	s := f13Sim(t, "DCE.m2701", "3000")
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	st.Format = 3
	if _, err := Restore(submitCfg(t, simNext), st); err == nil || !strings.Contains(err.Error(), "不迁移") {
		t.Fatalf("旧语义格式 3 必须拒绝：%v", err)
	}
	// 原始 CloseToday 配冻今手数在旧实现自洽，不能只靠冻结重算偶然拒绝。
	r := req(t, "DCE.m2701", types.Sell, types.CloseToday, "3000", 1)
	if _, err := s.PlaceAccepted(simNext, "legacy", r); err != nil {
		t.Fatal(err)
	}
	st, err = s.State()
	if err != nil {
		t.Fatal(err)
	}
	st.Orders[0].Request.Offset = types.CloseToday
	st.Orders[0].Frozen.VolumeToday, st.Orders[0].Frozen.VolumeHistory = 1, 0
	if _, err := Restore(submitCfg(t, simNext), st); err == nil || !strings.Contains(err.Error(), "未转换") {
		t.Fatalf("新格式中的未转换挂单必须明确拒绝：%v", err)
	}
}

// TestRewriteRoundingFailureLeavesState 继承 F13 取整拒绝，四条委托入口都不留痕迹。
func TestRewriteRoundingFailureLeavesState(t *testing.T) {
	for _, path := range []string{"FreezeOf", "Submit", "Place", "PlaceAccepted"} {
		s := f13Sim(t, "DCE.lh2701", "14010")
		before := rewriteState(t, s)
		r := req(t, "DCE.lh2701", types.Sell, types.CloseToday, "14010", 2)
		var err error
		switch path {
		case "FreezeOf":
			_, err = s.FreezeOf(simNext, r)
		case "Submit":
			_, err = s.Submit(simNext, wall(t, "2026-09-16 10:00"), r)
		case "Place":
			_, err = s.Place(simNext, wall(t, "2026-09-16 10:00"), "r", r)
		case "PlaceAccepted":
			_, err = s.PlaceAccepted(simNext, "r", r)
		}
		if err == nil || !strings.Contains(err.Error(), "两种取整") && !strings.Contains(err.Error(), "分段取整") {
			t.Errorf("%s 必须保留取整歧义拒绝：%v", path, err)
		}
		if rewriteState(t, s) != before {
			t.Fatal("取整失败修改了完整状态")
		}
	}
}

// TestRewriteShortAndOnlyHistory 空头是公式对称外推，昨仓单独存在也是推导场景，不冒充实测。
func TestRewriteShortAndOnlyHistory(t *testing.T) {
	for _, side := range []types.Direction{types.Buy, types.Sell} {
		for _, addToday := range []bool{false, true} {
			s, err := New(submitCfg(t, simDay))
			if err != nil {
				t.Fatal(err)
			}
			mark(t, s, "DCE.m2701", "3000", "3000")
			if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", side, types.Open, "3000", 1)); err != nil {
				t.Fatal(err)
			}
			if err := s.Settle(simDay, settlePx(t, "DCE.m2701", "3000"), simNext); err != nil {
				t.Fatal(err)
			}
			markOn(t, s, simNext, "DCE.m2701", "3010", "3000")
			if addToday {
				if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", side, types.Open, "3010", 1)); err != nil {
					t.Fatal(err)
				}
			}
			r := req(t, "DCE.m2701", opposite(side), types.CloseToday, "3020", 1)
			before := s.Account()
			if _, err := s.Submit(simNext, wall(t, "2026-09-16 10:00"), r); err != nil {
				t.Fatal(err)
			}
			wantFee, wantToday := "1.2", 0
			if addToday {
				wantFee, wantToday = "0.75", 1
			}
			p, _ := s.Position(r.Instrument, r.Hedge)
			wantProfit := dec("200")
			if side == types.Sell {
				wantProfit = wantProfit.Neg()
			}
			if p.VolumeHistory(side) != 0 || p.VolumeToday(side) != wantToday || !s.Account().Commission.Sub(before.Commission).Equal(dec(wantFee)) || !s.Account().CloseProfit.Sub(before.CloseProfit).Equal(wantProfit) {
				t.Fatal("空头或纯昨仓改写结果错误")
			}
		}
	}
}

// TestRewriteFailuresAndSameRates 对缺事实、重复编号和同费率场景逐项检查完整状态。
func TestRewriteFailuresAndSameRates(t *testing.T) {
	for _, kind := range []string{"缺时段", "缺规则", "重复编号"} {
		s := f13Sim(t, "DCE.m2701", "3000")
		r := req(t, "DCE.m2701", types.Sell, types.CloseToday, "3000", 1)
		if kind == "缺时段" {
			s.calendar = nil
		}
		if kind == "缺规则" {
			r.Instrument = simInst(t, "DCE.i2701")
		}
		if kind == "重复编号" {
			if _, err := s.PlaceAccepted(simNext, "dup", r); err != nil {
				t.Fatal(err)
			}
		}
		before := rewriteState(t, s)
		_, err := s.Place(simNext, wall(t, "2026-09-16 10:00"), "dup", r)
		if err == nil {
			t.Fatalf("%s 未拒绝", kind)
		}
		if kind == "缺时段" {
			var unchecked *match.UncheckedError
			if !errors.As(err, &unchecked) {
				t.Fatalf("缺时段错误类别变化：%v", err)
			}
		}
		if rewriteState(t, s) != before {
			t.Fatalf("%s 修改了状态", kind)
		}
	}
	s := f13Sim(t, "DCE.y2701", "8000")
	r := req(t, "DCE.y2701", types.Sell, types.CloseToday, "8000", 2)
	before := s.Account().Commission
	if _, err := s.Submit(simNext, wall(t, "2026-09-16 10:00"), r); err != nil {
		t.Fatal(err)
	}
	if !s.Account().Commission.Sub(before).Equal(dec("2.2")) {
		t.Fatal("同费率改写手续费错误")
	}
}
