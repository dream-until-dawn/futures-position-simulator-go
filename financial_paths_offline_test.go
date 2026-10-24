package futsim

import (
	"bytes"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
	"testing"
)

func TestOfflineCloseUsesByDate(t *testing.T) {
	cfg, id := offlineFixture(t)
	s := offlineNew(t, cfg, id)
	d := decimal.NewFromInt
	if err := s.ApplyTrade(cfg.Day, match.Trade{Instrument: id, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: d(3000), Volume: 1}); err != nil {
		t.Fatal(err)
	}
	next := cfg.Day + 1
	if err := s.Settle(cfg.Day, map[types.InstrumentID]decimal.Decimal{id: d(3100)}, next); err != nil {
		t.Fatal(err)
	}
	if err := s.Mark(next, Quote{Instrument: id, Last: d(3200), HasLast: true, PreSettlement: d(3100), HasPreSettlement: true}); err != nil {
		t.Fatal(err)
	}
	tr := match.Trade{Instrument: id, Direction: types.Sell, Offset: types.CloseYesterday, Hedge: types.Speculation, Price: d(3200), Volume: 1}
	if err := s.ApplyTrade(next, tr); err != nil {
		t.Fatal(err)
	}
	if !s.Account().CloseProfit.Equal(d(1000)) || !s.Account().Balance.Equal(cfg.PreBalance.Add(d(1998))) {
		t.Fatalf("跨日平仓混用逐笔盈亏：%+v", s.Account())
	}
	_, before := offlineState(t, s)
	if err := s.ApplyTrade(next, tr); err == nil {
		t.Fatal("无仓平昨被接受")
	}
	_, after := offlineState(t, s)
	if !bytes.Equal(before, after) {
		t.Fatal("失败平仓改变状态")
	}
}

func TestOfflineFreezeUsesTodayLeg(t *testing.T) {
	cfg, id := offlineFixture(t)
	s := offlineNew(t, cfg, id)
	for _, dir := range []types.Direction{types.Buy, types.Sell} {
		req := order.Request{Instrument: id, Direction: dir, Offset: types.Open, Hedge: types.Speculation, Price: decimal.NewFromInt(3010), Volume: 1}
		f, err := s.FreezeOf(cfg.Day, req)
		if err != nil || !f.Margin.Equal(decimal.NewFromInt(3010)) {
			t.Fatalf("挂单冻结错误使用昨仓腿：%+v %v", f, err)
		}
		req.Price = decimal.Zero
		if _, err = s.FreezeOf(cfg.Day, req); err == nil {
			t.Fatal("零价冻结未拒绝")
		}
	}
}

func TestOfflineForceOffsetsRemainUnmeasured(t *testing.T) {
	cfg, id := offlineFixture(t)
	cfg.Choices.UndatedCloseOnUseHistory = UndatedCloseAsYesterday
	s := offlineNew(t, cfg, id)
	if got := s.datedOffset(refdata.UseHistory, types.Close); got != types.CloseYesterday {
		t.Fatal("普通裸平未按已选口径改写")
	}
	for _, off := range []types.Offset{types.ForceClose, types.ForceOff, types.LocalForceClose} {
		if got := s.datedOffset(refdata.UseHistory, off); got != off {
			t.Fatalf("未观测强平标志被猜改写：%v -> %v", off, got)
		}
		_, before := offlineState(t, s)
		err := s.ApplyTrade(cfg.Day, match.Trade{Instrument: id, Direction: types.Sell, Offset: off, Hedge: types.Speculation, Price: decimal.NewFromInt(3000), Volume: 1})
		_, after := offlineState(t, s)
		if err == nil || !bytes.Equal(before, after) {
			t.Fatalf("未知强平路径未拒绝或改变状态：%v", err)
		}
	}
}
