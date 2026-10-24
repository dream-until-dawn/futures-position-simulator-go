package replay

import (
	"encoding/json"
	"fmt"
	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func configuration(p Payload, opt Options) (futsim.Config, refdata.Evidence, error) {
	rules, cal, ev, err := loadRules(p.Rules, opt)
	if err != nil {
		return futsim.Config{}, ev, err
	}
	cfg := futsim.Config{Day: p.Settings.Day, PreBalance: p.Settings.PreBalance, Choices: p.Settings.Choices, Rules: rules, Calendar: cal, TickRounding: map[types.Exchange]refdata.TickRounding{}, PositionLimits: map[types.InstrumentID]int{}}
	if cal == nil || !cal.IsTradingDay(cfg.Day) {
		return cfg, ev, fmt.Errorf("初始交易日不在所附日历中")
	}
	for _, r := range p.Settings.TickRounding {
		if _, ok := cfg.TickRounding[r.Exchange]; ok {
			return cfg, ev, fmt.Errorf("重复交易所取整配置")
		}
		if !r.Exchange.Valid() {
			return cfg, ev, fmt.Errorf("未知交易所")
		}
		if !r.Rounding.Valid() {
			return cfg, ev, fmt.Errorf("未知取整配置")
		}
		cfg.TickRounding[r.Exchange] = r.Rounding
	}
	for _, l := range p.Settings.PositionLimits {
		if _, ok := cfg.PositionLimits[l.Instrument]; ok || l.Volume <= 0 {
			return cfg, ev, fmt.Errorf("重复或无效限仓配置")
		}
		if _, err := rules.Instrument(l.Instrument); err != nil {
			return cfg, ev, err
		}
		cfg.PositionLimits[l.Instrument] = l.Volume
	}
	return cfg, ev, nil
}

// Run 严格顺序重放；每个事件成功才提交，失败返回明确标记的有效前缀。
func Run(input Payload, opt Options) (Report, error) {
	return runBounded(input, opt, MaxReportBytes)
}

func runBounded(input Payload, opt Options, limit int) (Report, error) {
	report := Report{Format: Format, Status: "failed", Frames: []Frame{}}
	fail := func(index *int, err error) (Report, error) {
		report.FailedIndex = index
		message := err.Error()
		if len(message) > 1000 {
			message = message[:1000] + "..."
		}
		report.Failure = message
		return report, err
	}
	// JSON往返隔离所有输入切片/指针，同时对Go直接调用施加与文件输入相同的格式限制。
	raw, err := json.Marshal(input)
	if err != nil {
		return fail(nil, err)
	}
	var p Payload
	if err = strictDecode(raw, &p); err != nil {
		return fail(nil, err)
	}
	compacted, err := compact(raw)
	if err != nil {
		return fail(nil, err)
	}
	report.PayloadSHA256 = fingerprint(compacted)
	if len(p.Events) > MaxEvents {
		return fail(nil, fmt.Errorf("事件数量超过上限"))
	}
	cfg, ev, err := configuration(p, opt)
	report.Evidence = ev
	if err != nil {
		return fail(nil, err)
	}
	var sim *futsim.Simulator
	if p.Initial == nil {
		sim, err = futsim.New(cfg)
	} else {
		sim, err = futsim.Restore(cfg, *p.Initial)
	}
	if err != nil {
		return fail(nil, err)
	}
	st, err := sim.State()
	if err != nil {
		return fail(nil, err)
	}
	initialAccount := sim.Account()
	report.Initial, report.Final, report.InitialAccount = &st, &st, &initialAccount
	initialBytes, _ := json.MarshalIndent(report, "", "  ")
	used := len(initialBytes) + 4096
	if used > limit {
		report.Initial = nil
		report.InitialAccount = nil
		report.Final = nil
		return fail(nil, fmt.Errorf("初始状态超过报告预算"))
	}
	seen := map[string]bool{}
	for index, e := range p.Events {
		if e.ID == "" || seen[e.ID] {
			return fail(&index, fmt.Errorf("事件ID为空或重复: %s", e.ID))
		}
		seen[e.ID] = true
		before := sim.Account()
		beforeState := *report.Final
		currentCfg := cfg
		currentCfg.Day = before.TradingDay
		work, err := futsim.Restore(currentCfg, beforeState)
		if err != nil {
			return fail(&index, err)
		}
		if err = apply(work, cfg, e); err != nil {
			return fail(&index, fmt.Errorf("事件%d %s: %w", index, e.ID, err))
		}
		afterState, err := work.State()
		if err != nil {
			return fail(&index, err)
		}
		after := work.Account()
		frame := Frame{Index: index, Event: e, Account: after, State: afterState, Changes: changes(before, after)}
		if e.Kind == "settle" {
			fee := beforeState.Account.SettleCommission
			frame.Statement = &Statement{Day: e.Day, NextDay: after.TradingDay, PreBalance: before.PreBalance, Deposit: before.Deposit, Withdraw: before.Withdraw, CloseProfit: before.CloseProfit, Commission: fee, Balance: after.PreBalance, NextMargin: after.CurrMargin,
				PositionProfit: after.PreBalance.Sub(before.StaticBalance).Sub(before.CloseProfit).Add(fee)}
		}
		frameBytes, err := json.MarshalIndent(frame, "    ", "  ")
		if err != nil {
			return fail(&index, err)
		}
		// 预算包括最终State的副本；超限时该事件尚未提交。
		finalBytes, _ := json.MarshalIndent(afterState, "  ", "  ")
		if used+len(frameBytes)+len(finalBytes)+8 > limit {
			return fail(&index, fmt.Errorf("报告大小超过上限"))
		}
		used += len(frameBytes) + 8
		sim = work
		report.Frames = append(report.Frames, frame)
		report.Final = &afterState
	}
	report.Status = "passed"
	return report, nil
}

func apply(s *futsim.Simulator, cfg futsim.Config, e Event) error {
	n := 0
	for _, present := range []bool{e.Quote != nil, e.Trade != nil, e.Order != nil, e.Ref != "", e.Amount != nil, e.Settlement != nil} {
		if present {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("事件必须恰有一种负载")
	}
	switch e.Kind {
	case "mark":
		if e.Quote != nil {
			return s.Mark(e.Day, *e.Quote)
		}
	case "trade":
		if e.Trade != nil {
			return s.ApplyTrade(e.Day, *e.Trade)
		}
	case "accept":
		if e.Order != nil {
			_, err := s.PlaceAccepted(e.Day, e.Order.Ref, e.Order.Request)
			return err
		}
	case "fill":
		if e.Ref != "" {
			_, err := s.Fill(e.Day, e.Ref)
			return err
		}
	case "cancel":
		if e.Ref != "" {
			return s.Cancel(e.Day, e.Ref)
		}
	case "deposit":
		if e.Amount != nil {
			return s.Deposit(e.Day, *e.Amount)
		}
	case "withdraw":
		if e.Amount != nil {
			return s.Withdraw(e.Day, *e.Amount)
		}
	case "settle":
		if e.Settlement != nil {
			next, ok := cfg.Calendar.NextTradingDay(e.Day)
			if !ok || next != e.Settlement.NextDay {
				return fmt.Errorf("结算必须使用日历严格下一交易日")
			}
			prices := map[types.InstrumentID]decimal.Decimal{}
			for _, p := range e.Settlement.Prices {
				if _, dup := prices[p.Instrument]; dup || !p.Value.IsPositive() {
					return fmt.Errorf("结算价格重复或无效")
				}
				if _, err := cfg.Rules.Instrument(p.Instrument); err != nil {
					return err
				}
				prices[p.Instrument] = p.Value
			}
			return s.Settle(e.Day, prices, next)
		}
	}
	return fmt.Errorf("未知事件或负载不匹配: %s", e.Kind)
}

func changes(a, b account.Snapshot) Changes {
	return Changes{Balance: b.Balance.Sub(a.Balance), Commission: b.Commission.Sub(a.Commission), CloseProfit: b.CloseProfit.Sub(a.CloseProfit), PositionProfit: b.PositionProfit.Sub(a.PositionProfit), Margin: b.CurrMargin.Sub(a.CurrMargin), FrozenMargin: b.FrozenMargin.Sub(a.FrozenMargin), FrozenCommission: b.FrozenCommission.Sub(a.FrozenCommission), Deposit: b.Deposit.Sub(a.Deposit), Withdraw: b.Withdraw.Sub(a.Withdraw)}
}
