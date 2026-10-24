package replay

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"io"
)

// ReadReport 有界读取完整报告，检查内部连续性；不声称独立核实柜台或外部输入真实性。
func ReadReport(r io.Reader) (Report, error) {
	var result Report
	raw, err := io.ReadAll(io.LimitReader(r, MaxReportBytes+1))
	if err != nil {
		return result, err
	}
	if err = strictDecodeSized(raw, &result, MaxReportBytes); err != nil {
		return result, err
	}
	return result, validateReport(result)
}

func same(a, b any) bool { x, _ := json.Marshal(a); y, _ := json.Marshal(b); return bytes.Equal(x, y) }

func validateReport(r Report) error {
	if r.Format != Format || len(r.Frames) > MaxEvents {
		return fmt.Errorf("报告格式或数量无效")
	}
	if r.Status != "passed" && r.Status != "failed" {
		return fmt.Errorf("报告状态未知")
	}
	if r.Status == "passed" && (r.Failure != "" || r.FailedIndex != nil) {
		return fmt.Errorf("成功报告带失败信息")
	}
	if r.Status == "failed" && r.Failure == "" {
		return fmt.Errorf("失败报告缺少原因")
	}
	if r.Initial == nil {
		if r.Status != "failed" || r.InitialAccount != nil || r.Final != nil || len(r.Frames) != 0 || r.FailedIndex != nil {
			return fmt.Errorf("未初始化报告包含伪状态")
		}
		return nil
	}
	if r.InitialAccount == nil || r.Final == nil || !r.Evidence.Valid() {
		return fmt.Errorf("报告缺少账户或来源")
	}
	if decoded, err := hex.DecodeString(r.PayloadSHA256); err != nil || len(decoded) != 32 {
		return fmt.Errorf("报告输入指纹无效")
	}
	if r.Initial.Format != futsim.StateFormat || r.Initial.Day != r.Initial.Account.Day {
		return fmt.Errorf("初始存档版本或交易日矛盾")
	}
	if r.FailedIndex != nil && *r.FailedIndex != len(r.Frames) {
		return fmt.Errorf("失败事件不紧接有效前缀")
	}
	if r.Status == "failed" && r.FailedIndex == nil {
		return fmt.Errorf("已初始化失败缺少事件位置")
	}
	initial, err := account.Restore(r.Initial.Account)
	if err != nil {
		return err
	}
	if !same(initial.Snapshot(), r.InitialAccount) {
		return fmt.Errorf("初始账户与存档不符")
	}
	ids := map[string]bool{}
	before := *r.InitialAccount
	last := r.Initial
	for i, f := range r.Frames {
		if f.Event.Day != before.TradingDay || f.State.Day != f.Account.TradingDay || f.State.Format != r.Initial.Format || f.State.Choices != r.Initial.Choices || f.State.RulesVersion != r.Initial.RulesVersion || f.State.LastOrderSequence < last.LastOrderSequence {
			return fmt.Errorf("事件时序、版本或高水位矛盾")
		}
		if f.Index != i || f.Event.ID == "" || ids[f.Event.ID] {
			return fmt.Errorf("报告事件顺序或ID无效")
		}
		ids[f.Event.ID] = true
		acc, err := account.Restore(f.State.Account)
		if err != nil {
			return err
		}
		if !same(acc.Snapshot(), f.Account) || !same(changes(before, f.Account), f.Changes) {
			return fmt.Errorf("事件账户或变化不一致")
		}
		if f.Event.Kind == "settle" {
			if f.Statement == nil {
				return fmt.Errorf("结算报告缺少账单")
			}
			x := f.Statement
			if x.Day != before.TradingDay || x.NextDay != f.Account.TradingDay || !x.PreBalance.Equal(before.PreBalance) || !x.Deposit.Equal(before.Deposit) || !x.Withdraw.Equal(before.Withdraw) || !x.CloseProfit.Equal(before.CloseProfit) || !x.Commission.Equal(last.Account.SettleCommission) || !x.Balance.Equal(f.Account.PreBalance) || !x.NextMargin.Equal(f.Account.CurrMargin) || !x.PreBalance.Add(x.Deposit).Sub(x.Withdraw).Add(x.CloseProfit).Add(x.PositionProfit).Sub(x.Commission).Equal(x.Balance) {
				return fmt.Errorf("结算账单链条不一致")
			}
		} else if f.Statement != nil {
			return fmt.Errorf("非结算事件包含账单")
		}
		before = f.Account
		state := f.State
		last = &state
	}
	if !same(last, r.Final) {
		return fmt.Errorf("最终存档不等于有效前缀")
	}
	return nil
}
