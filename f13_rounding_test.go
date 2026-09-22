package futsim

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// f13Sim：sym 在 simDay 开多 1 @px、按 px 结算，simNext 再开多 1 @px —— 今 1 昨 1、额度 1，停在 simNext。带报单配置（Submit / Place 要用）。
func f13Sim(t *testing.T, sym, px string) *Simulator {
	t.Helper()
	cfg := submitCfg(t, simDay)
	cfg.PositionLimits = map[types.InstrumentID]int{simInst(t, sym): 100}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, sym, px, px)
	if err := s.ApplyTrade(simDay, trade(t, sym, types.Buy, types.Open, px, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(simDay, settlePx(t, sym, px), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, sym, px, px)
	if err := s.ApplyTrade(simNext, trade(t, sym, types.Buy, types.Open, px, 1)); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestSplitCloseRoundingDivergenceRefuses 钉住 F13 决策点 1（design.md 门面形状 §17）：一笔裸平拆成平今 / 平昨两段时，
// 「分段取整」与「按笔取整」给出不同的数 ⇒ 报错且状态不动（ApplyTrade / Submit / Place 三条路径）；同值 ⇒ 照收。
//
//	lh（按额 平昨 0.0002 / 平今 0.0004，乘数 16），2 手额度 1：
//	  @14010  平今 89.664 + 平昨 44.832：分段 89.66 + 44.83 = 134.49，按笔 134.496 → 134.50   ⇒ 不同，报错
//	  @14000  平今 89.600 + 平昨 44.800：两种都是 134.40                                     ⇒ 照收
//	jd（合成：按额三档同 0.00015，每手 平昨 2 / 平今 1），2 手额度 1：
//	  @14010  按额 33.624 + 33.624：分段 33.62 + 33.62 = 67.24，按笔 67.248 → 67.25          ⇒ 不同，报错
func TestSplitCloseRoundingDivergenceRefuses(t *testing.T) {
	at := wall(t, "2026-09-16 10:00")
	refuse := func(sym, px string) {
		t.Helper()
		for _, path := range []string{"ApplyTrade", "Submit", "Place"} {
			s := f13Sim(t, sym, px)
			before, live := s.Account(), len(s.Live())
			q := s.quotaOf(simInst(t, sym), types.Speculation, types.Buy)
			var err error
			switch path {
			case "ApplyTrade":
				err = s.ApplyTrade(simNext, trade(t, sym, types.Sell, types.Close, px, 2))
			case "Submit":
				_, err = s.Submit(simNext, at, req(t, sym, types.Sell, types.Close, px, 2))
			case "Place":
				_, err = s.Place(simNext, at, "c", req(t, sym, types.Sell, types.Close, px, 2))
			}
			if err == nil || !strings.Contains(err.Error(), "§17 决策点 1") {
				t.Errorf("⚠️ %s @%s %s：两种取整写法不同，要报错（点名 §17 决策点 1），得到 %v", sym, px, path, err)
				continue
			}
			if !sameSnapshot(s.Account(), before) || len(s.Live()) != live || s.quotaOf(simInst(t, sym), types.Speculation, types.Buy) != q {
				t.Errorf("⚠️ %s @%s %s：报错之后状态变了", sym, px, path)
			}
		}
	}
	refuse("DCE.lh2701", "14010")
	refuse("DCE.jd2701", "14010")

	s := f13Sim(t, "DCE.lh2701", "14000")
	before := s.Account().Commission
	if err := s.ApplyTrade(simNext, trade(t, "DCE.lh2701", types.Sell, types.Close, "14000", 2)); err != nil {
		t.Fatalf("⚠️ lh @14000：两种取整写法同值，不该报错：%v", err)
	}
	if got := s.Account().Commission.Sub(before); !got.Equal(dec("134.4")) {
		t.Errorf("lh @14000 裸平 2 手收 %s，期望 89.6 + 44.8 = 134.4", got)
	}
}
