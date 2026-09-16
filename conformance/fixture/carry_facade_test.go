package fixture

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestReconstructOnFacadeSplitsTheTwoBaselines 是 TestCarrySplitsTheTwoBaselines 在门面上的那一份：
// 收盘前夹具的 rb2701 多头按 3160 结转 ⇒ 全部昨仓、逐笔对冲基线不动、逐日盯市基线 = 结算价。
//
// ⚠️ 用 status-20260908-8 而不是 Carry 那条的 avg-price-20260908：后者没有行情截面的昨结算价（Carry 不要，门面要）。
func TestReconstructOnFacadeSplitsTheTwoBaselines(t *testing.T) {
	f := loadOne(t, "status-20260908-8.json")
	const sym = "SHFE.rb2701"
	spec, ok := specForSymbol(t, f, sym)
	if !ok {
		t.Fatal("rb2701 没有登记规格")
	}
	next := types.NewTradingDay(2026, 9, 9)
	settle := decimal.RequireFromString("3160")
	r, err := ReconstructOnFacade(f, nil, sym, spec, positionDateOf(t, sym), settle, next)
	if err != nil {
		t.Fatal(err)
	}
	p := r.Position
	// 结转用的是交易所结算价（显式给的，不是借来的）⇒ 占用照给
	if !r.HasMargin || !r.MarginLong.IsPositive() {
		t.Errorf("⚠️ 结转之后应当给出占用，得到 HasMargin=%v 多头 %s", r.HasMargin, r.MarginLong)
	}
	s, err := p.Side(types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	held := f.Positions[sym]["volume_long"].Number
	if int64(s.VolumeHistory()) != held.IntPart() || s.VolumeHistory() == 0 || s.VolumeToday() != 0 || p.Day != next {
		t.Errorf("⚠️ 结转之后 今 %d / 昨 %d、交易日 %s，应为 今 0 / 昨 %s（柜台收盘多头）、%s", s.VolumeToday(), s.VolumeHistory(), p.Day, held, next)
	}
	openAvg, basisAvg, differs, err := Split(p, types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	// 逐笔对冲基线 = 柜台收盘前的开仓均价（结算不动它）
	wantOpen := f.Positions[sym]["open_price_long"].Number
	if openAvg.Sub(wantOpen).Abs().GreaterThan(decimal.RequireFromString("0.0000001")) || !basisAvg.Equal(settle) || !differs {
		t.Errorf("⚠️ 逐笔对冲基线 %s（应 %s，不受结算影响）/ 逐日盯市基线 %s（应 %s）/ 分开了 %v", openAvg, wantOpen, basisAvg, settle, differs)
	}
}

// TestReconstructOnFacadeRefuses 断言「结转了个寂寞」与缺输入都报错，不凑。
func TestReconstructOnFacadeRefuses(t *testing.T) {
	f := loadOne(t, "status-20260908-8.json")
	const sym = "SHFE.rb2701"
	spec, ok := specForSymbol(t, f, sym)
	if !ok {
		t.Fatal("rb2701 没有登记规格")
	}
	next := types.NewTradingDay(2026, 9, 9)
	settle := decimal.RequireFromString("3160")

	flat := ""
	for _, s := range f.Symbols() {
		if len(f.TradesOf(s)) == 0 {
			continue
		}
		v, w := f.Positions[s]["volume_long"], f.Positions[s]["volume_short"]
		if !v.Absent && v.Number.IsZero() && !w.Absent && w.Number.IsZero() {
			flat = s
			break
		}
	}
	if flat == "" {
		t.Fatal("⚠️ 夹具里找不到「有成交但收盘空仓」的合约 —— 这一分支没被考验过")
	}
	flatSpec, ok := specForSymbol(t, f, flat)
	if !ok {
		t.Fatalf("%s 没有登记规格", flat)
	}
	noLast := *f
	noLast.Positions = map[string]map[string]Value{}
	for k, v := range f.Positions {
		noLast.Positions[k] = v
	}
	noLast.Positions[sym] = map[string]Value{}
	for k, v := range f.Positions[sym] {
		if k != "last_price" {
			noLast.Positions[sym][k] = v
		}
	}
	later := &Fixture{TradingDay: types.NewTradingDay(2026, 9, 10)}

	for _, c := range []struct {
		name string
		run  func() error
		want string
	}{
		{"没有成交", func() error {
			_, err := ReconstructOnFacade(f, nil, "SHFE.zzz2701", spec, refdata.UseHistory, settle, next)
			return err
		}, "一笔成交都没有"},
		{"收盘空仓", func() error {
			_, err := ReconstructOnFacade(f, nil, flat, flatSpec, refdata.UseHistory, settle, next)
			return err
		}, "空仓"},
		{"结算价为零", func() error {
			_, err := ReconstructOnFacade(f, nil, sym, spec, refdata.UseHistory, decimal.Zero, next)
			return err
		}, ""},
		{"结转方向反了", func() error {
			_, err := ReconstructOnFacade(f, nil, sym, spec, refdata.UseHistory, settle, f.TradingDay)
			return err
		}, "结转方向反了"},
		{"当日夹具与 nextDay 不同", func() error {
			_, err := ReconstructOnFacade(f, later, sym, spec, refdata.UseHistory, settle, next)
			return err
		}, "不同"},
		{"前一日没有最新价", func() error {
			_, err := ReconstructOnFacade(&noLast, nil, sym, spec, refdata.UseHistory, settle, next)
			return err
		}, "没有最新价"},
	} {
		err := c.run()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("⚠️ %s：应当报错（含 %q），得到 %v", c.name, c.want, err)
		}
	}
}
