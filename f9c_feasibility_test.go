package futsim

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// f9cClosePairFixture 为未来原子撮合提供真实业务失败：第一笔消耗额度，第二笔出现取整分歧。
// 这里只用现有手动 Fill 验证可行性，不实现或提前调用 F9c。
func f9cClosePairFixture(t *testing.T) (*Simulator, Config) {
	t.Helper()
	const sym = "DCE.lh2701"
	cfg := submitCfg(t, simDay)
	cfg.Calendar = nil
	cfg.PositionLimits = map[types.InstrumentID]int{simInst(t, sym): 100}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, sym, "14010", "14010")
	if err = s.ApplyTrade(simDay, trade(t, sym, types.Buy, types.Open, "14010", 2)); err != nil {
		t.Fatal(err)
	}
	if err = s.Settle(simDay, settlePx(t, sym, "14010"), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, sym, "14010", "14010")
	if err = s.ApplyTrade(simNext, trade(t, sym, types.Buy, types.Open, "14010", 2)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PlaceAccepted(simNext, "z-first", req(t, sym, types.Sell, types.Close, "14010", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PlaceAccepted(simNext, "a-second", req(t, sym, types.Sell, types.Close, "14010", 2)); err != nil {
		t.Fatal(err)
	}
	cfg.Day = simNext
	return s, cfg
}

// TestOrderedClosePairFailureFeasibility 验明“先成一笔、第二笔失败”确实存在，避免假原子性用例。
func TestOrderedClosePairFailureFeasibility(t *testing.T) {
	s, _ := f9cClosePairFixture(t)
	before := rewriteState(t, s)
	if _, err := s.Fill(simNext, "z-first"); err != nil {
		t.Fatalf("前一笔必须确实成功：%v", err)
	}
	afterFirst := rewriteState(t, s)
	if afterFirst == before {
		t.Fatal("前一笔没有改变状态，原子性前提无判别力")
	}
	if _, err := s.Fill(simNext, "a-second"); err == nil || !strings.Contains(err.Error(), "§17 决策点 1") {
		t.Fatalf("第二笔必须因取整分歧失败：%v", err)
	}
	if rewriteState(t, s) != afterFirst {
		t.Fatal("单笔失败回滚改变已提交的前一笔或第二笔挂单")
	}
}
