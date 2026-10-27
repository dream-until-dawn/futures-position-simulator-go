package replay

import (
	"bytes"
	"encoding/json"
	"fmt"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/lifecycle"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/risk"
)

// loadDaily 对直接Go输入和文件输入使用同一严格加载器，许可只能来自调用者Options。
func loadDaily(raw json.RawMessage, opt Options) (*lifecycle.DailyRules, *refdata.Calendar, refdata.Evidence, error) {
	return lifecycle.Load(bytes.NewReader(raw), refdata.LoadOptions{AllowPlaceholder: opt.AllowPlaceholder})
}

// prepareRules 只准备候选配置；报告超限或事件失败时调用方不能提交本结果。
func prepareRules(cfg futsim.Config, e Event, opt Options) (futsim.Config, *RuleBinding, error) {
	if e.Kind != "settle" || e.Settlement == nil || len(e.Settlement.NextRules) == 0 {
		return cfg, nil, nil
	}
	rules, cal, ev, err := loadDaily(e.Settlement.NextRules, opt)
	if err != nil {
		return cfg, nil, err
	}
	if rules.LifecycleDay() != e.Settlement.NextDay || cal == nil || !cal.IsTradingDay(e.Day) {
		return cfg, nil, fmt.Errorf("次日规则日期或日历覆盖不完整")
	}
	next, ok := cal.NextTradingDay(e.Day)
	if !ok || next != e.Settlement.NextDay {
		return cfg, nil, fmt.Errorf("次日规则不能改变严格下一交易日")
	}
	for id := range cfg.PositionLimits {
		if _, err = rules.Instrument(id); err != nil {
			return cfg, nil, err
		}
	}
	updated := cfg
	updated.Day, updated.Rules, updated.Calendar = e.Settlement.NextDay, rules, cal
	return updated, &RuleBinding{Lifecycle: e.Settlement.NextRules, Evidence: ev}, nil
}

// boundRules 加载报告中的日绑定并核证据字段；允许解析placeholder不代表允许真实运行。
func boundRules(binding *RuleBinding) (*lifecycle.DailyRules, *refdata.Calendar, error) {
	if binding == nil {
		return nil, nil, fmt.Errorf("报告缺完整日规则")
	}
	rules, cal, ev, err := loadDaily(binding.Lifecycle, Options{AllowPlaceholder: true})
	if err != nil {
		return nil, nil, err
	}
	if binding.Evidence != ev {
		return nil, nil, fmt.Errorf("报告规则证据等级被更改")
	}
	return rules, cal, nil
}

// lifecycleResult 通过完整State/Config复核规则与账户一致，再生成只读报告。
func lifecycleResult(st futsim.State, rules *lifecycle.DailyRules) (lifecycle.Report, error) {
	if rules == nil || rules.LifecycleDay() != st.Day || rules.Version() != st.RulesVersion {
		return lifecycle.Report{}, fmt.Errorf("报告日规则日期/版本不一致")
	}
	sim, err := futsim.Restore(futsim.Config{Day: st.Day, Rules: rules, Choices: st.Choices}, st)
	if err != nil {
		return lifecycle.Report{}, err
	}
	return sim.AssessLifecycle()
}

// validateRuleReports 独立重算各段绑定和只读报告，不允许版本变化被一般账户连续性掩盖。
func validateRuleReports(report Report) error {
	if report.Initial == nil {
		return nil
	}
	var active *lifecycle.DailyRules
	var calendar *refdata.Calendar
	var err error
	if report.InitialRules != nil {
		active, calendar, err = boundRules(report.InitialRules)
		if err != nil {
			return err
		}
		if calendar == nil || report.InitialRules.Evidence != report.Evidence {
			return fmt.Errorf("初始规则缺日历或证据不符")
		}
		if _, err = lifecycleResult(*report.Initial, active); err != nil {
			return err
		}
	}
	previous := *report.Initial
	for _, frame := range report.Frames {
		e := frame.Event
		transition := e.Settlement != nil && len(e.Settlement.NextRules) > 0
		if transition {
			if e.Kind != "settle" || frame.Rules == nil || !same(json.RawMessage(e.Settlement.NextRules), frame.Rules.Lifecycle) {
				return fmt.Errorf("规则切换缺完整绑定或不在结算事件")
			}
			next, cal, err := boundRules(frame.Rules)
			if err != nil {
				return err
			}
			if cal == nil || !cal.IsTradingDay(e.Day) || next.LifecycleDay() != e.Settlement.NextDay || next.Version() == previous.RulesVersion {
				return fmt.Errorf("规则切换日期/版本不符")
			}
			day, ok := cal.NextTradingDay(e.Day)
			if !ok || day != e.Settlement.NextDay {
				return fmt.Errorf("规则切换日历不符")
			}
			if active != nil {
				if next.Investor() != active.Investor() {
					return fmt.Errorf("报告切换了投资者资格")
				}
				day, ok = calendar.NextTradingDay(e.Day)
				if !ok || day != e.Settlement.NextDay {
					return fmt.Errorf("报告改变原日历下一日")
				}
			}
			active, calendar = next, cal
		} else {
			if frame.Rules != nil || frame.State.RulesVersion != previous.RulesVersion {
				return fmt.Errorf("非切换事件自行更换规则版本")
			}
			if active != nil && e.Kind == "settle" {
				return fmt.Errorf("生命周期结算缺次日规则")
			}
		}
		if e.Kind != "settle" && frame.State.Day != previous.Day {
			return fmt.Errorf("非结算事件改变交易日")
		}
		if active != nil {
			calculated, err := lifecycleResult(frame.State, active)
			if err != nil {
				return err
			}
			if frame.Lifecycle == nil || !same(calculated, *frame.Lifecycle) {
				return fmt.Errorf("生命周期报告与完整状态不符")
			}
		} else if frame.Lifecycle != nil {
			return fmt.Errorf("普通规则伪造生命周期健康报告")
		}
		if e.Kind == "risk" {
			if e.RiskPolicy == nil || frame.Risk == nil {
				return fmt.Errorf("风控报告缺显式策略或结果")
			}
			calculated, err := risk.Assess(frame.State.Account, *e.RiskPolicy)
			if err != nil {
				return err
			}
			if !same(calculated, *frame.Risk) || !same(previous, frame.State) {
				return fmt.Errorf("风控报告与状态不符或只读事件改动状态")
			}
		} else if frame.Risk != nil {
			return fmt.Errorf("非风控事件携带风控结果")
		}
		previous = frame.State
	}
	return nil
}
