package futsim

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

func submitCfg(t *testing.T, day types.TradingDay) Config {
	t.Helper()
	return Config{Day: day, PreBalance: dec("1000000"), Rules: simRules(t), Choices: ctpChoices(),
		Calendar: submitCalendar(t), TickRounding: MeasuredTickRounding(),
		PositionLimits: map[types.InstrumentID]int{simInst(t, "DCE.m2701"): 100, simInst(t, "SHFE.ag2702"): 100}}
}

// midway 跑一段含开仓、结算、今仓开仓、平昨挂单的序列，停在 simNext。
func midway(t *testing.T) *Simulator {
	t.Helper()
	s, err := New(submitCfg(t, simDay))
	if err != nil {
		t.Fatal(err)
	}
	mark(t, s, "DCE.m2701", "3361", "3384")
	if _, err := s.Submit(simDay, wall(t, "2026-09-15 10:00"), req(t, "DCE.m2701", types.Buy, types.Open, "3360", 2)); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(simDay, settlePx(t, "DCE.m2701", "3370"), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, "DCE.m2701", "3375", "3370")
	if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Buy, types.Open, "3372", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Place(simNext, wall(t, "2026-09-16 10:00"), "cy", req(t, "DCE.m2701", types.Sell, types.CloseYesterday, "3380", 1)); err != nil {
		t.Fatal(err)
	}
	return s
}

func roundTrip(t *testing.T, st State) State {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestStateRoundTripContinuesIdentically 钉住：跑到一半导出 → JSON → 恢复，两边接着跑剩下的序列，每一步账户与持仓逐字段相同。
func TestStateRoundTripContinuesIdentically(t *testing.T) {
	a := midway(t)
	st, err := a.State()
	if err != nil {
		t.Fatal(err)
	}
	b, err := Restore(submitCfg(t, simNext), roundTrip(t, st))
	if err != nil {
		t.Fatalf("⚠️ 自己导出的存档恢复不了：%v", err)
	}
	m := simInst(t, "DCE.m2701")
	same := func(step string) {
		t.Helper()
		if !sameSnapshot(a.Account(), b.Account()) {
			t.Errorf("⚠️ %s 之后账户不同：\n原 %+v\n复 %+v", step, a.Account(), b.Account())
		}
		pa, _ := a.Position(m, types.Speculation)
		pb, _ := b.Position(m, types.Speculation)
		la, _ := pa.Side(types.Buy)
		lb, _ := pb.Side(types.Buy)
		if fmtLots(la.Lots()) != fmtLots(lb.Lots()) {
			t.Errorf("⚠️ %s 之后多头明细不同：\n原 %s\n复 %s", step, fmtLots(la.Lots()), fmtLots(lb.Lots()))
		}
		if strings.Join(a.Live(), ",") != strings.Join(b.Live(), ",") {
			t.Errorf("⚠️ %s 之后挂单簿不同：%v / %v", step, a.Live(), b.Live())
		}
	}
	same("恢复")
	for _, step := range []struct {
		name string
		do   func(*Simulator) error
	}{
		{"挂单成交", func(s *Simulator) error { _, err := s.Fill(simNext, "cy"); return err }},
		{"最新价 3390", func(s *Simulator) error {
			return s.Mark(simNext, Quote{Instrument: m, Last: dec("3390"), HasLast: true})
		}},
		{"平今 1", func(s *Simulator) error {
			_, err := s.Submit(simNext, wall(t, "2026-09-16 10:01"), req(t, "DCE.m2701", types.Sell, types.CloseToday, "3390", 1))
			return err
		}},
	} {
		if err := step.do(a); err != nil {
			t.Fatalf("原：%s：%v", step.name, err)
		}
		if err := step.do(b); err != nil {
			t.Fatalf("复：%s：%v", step.name, err)
		}
		same(step.name)
	}
	if a.Account().CloseProfit.IsZero() {
		t.Error("前提：序列里要有平仓，否则上面比的只是开仓")
	}
}

func fmtLots(ls interface{}) string { b, _ := json.Marshal(ls); return string(b) }

// TestStateDecimalsAreStrings 钉住存档里的小数是字符串（float64 往返会让价格与费率丢位，design.md §6.5）。
func TestStateDecimalsAreStrings(t *testing.T) {
	st, err := midway(t).State()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	js := string(b)
	for _, want := range []string{`"PreBalance":"1000`, `"OpenPrice":"3360"`, `"PreSettlement":"3370"`} {
		if !strings.Contains(js, want) {
			t.Errorf("⚠️ 存档里没有 %s —— 小数没按字符串存", want)
		}
	}
}

// TestRestoreRefusesTamperedState 钉住十类不一致各自报错。
func TestRestoreRefusesTamperedState(t *testing.T) {
	base, err := midway(t).State()
	if err != nil {
		t.Fatal(err)
	}
	m := simInst(t, "DCE.m2701")
	for _, c := range []struct {
		name string
		edit func(*State, *Config)
		want string
	}{
		{"格式版本", func(s *State, _ *Config) { s.Format = 99 }, "格式"},
		{"口径改一项", func(s *State, _ *Config) { s.Choices.Mark++ }, "口径"},
		{"规则版本", func(s *State, _ *Config) { s.RulesVersion++ }, "规则数据版本"},
		{"交易日", func(_ *State, c *Config) { c.Day = simDay }, "交易日"},
		{"合约不在规则里", func(s *State, _ *Config) { s.Positions[0].Instrument = simInst(t, "DCE.i2701") }, "i2701"},
		{"PositionDateType 不一致", func(s *State, _ *Config) { s.Positions[0].DateType++ }, "PositionDateType"},
		{"明细手数为 0", func(s *State, _ *Config) { s.Positions[0].Long[0].Volume = 0 }, "第 1 片：手数 0 不为正"},
		{"昨仓片排在今仓片后", func(s *State, _ *Config) {
			l := s.Positions[0].Long // 昨 2（一片）+ 今 1（一片）⇒ 首尾对调即今仓片在前
			l[0], l[len(l)-1] = l[len(l)-1], l[0]
		}, "排在今仓片之后"},
		{"账户占用被手改", func(s *State, _ *Config) { s.Account.CurrMargin = s.Account.CurrMargin.Add(dec("1")) }, "重算"},
		{"挂单冻结与账户不符", func(s *State, _ *Config) { s.Account.FrozenCommission = s.Account.FrozenCommission.Add(dec("1")) }, "冻结"},
	} {
		st := roundTrip(t, base)
		cfg := submitCfg(t, simNext)
		c.edit(&st, &cfg)
		if st.Positions[0].Instrument != m && c.name != "合约不在规则里" {
			t.Fatalf("前提：存档第一条持仓是 m2701")
		}
		_, err := Restore(cfg, st)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("⚠️ %s：要报含「%s」的错，得到 %v", c.name, c.want, err)
		}
	}
	// 反向：原样恢复不报错，否则一个「一律报错」的 Restore 也能过上面
	if _, err := Restore(submitCfg(t, simNext), roundTrip(t, base)); err != nil {
		t.Errorf("原样存档恢复失败：%v", err)
	}
	// 失效态不许导出
	s := midway(t)
	s.broken = errors.New("合成的失效")
	if _, err := s.State(); err == nil {
		t.Error("⚠️ 失效态导出了存档")
	}
}
