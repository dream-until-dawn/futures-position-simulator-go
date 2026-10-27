package risk

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/shopspring/decimal"
)

func amount(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// baseState是明确的合成聚合账户；不代表任何柜台的实际追保政策。
func baseState() account.State {
	return account.State{Currency: "CNY", Day: 20261027, Algorithm: account.AlgorithmAll, PreBalance: amount("1000"), CurrMargin: amount("400"), ExchangeMargin: amount("200")}
}
func policy() Policy {
	return Policy{MaxRiskRatio: amount("0.8"), MinEquity: amount("100"), MinAvailable: amount("50")}
}

// TestRiskSignalsAndDeposit 独立手算三种缺口，并实际入金后重新核验全部判据。
func TestRiskSignalsAndDeposit(t *testing.T) {
	for _, kind := range []string{"healthy", "ratio", "available", "equity", "zero-equity", "negative-equity", "equal-thresholds"} {
		t.Run(kind, func(t *testing.T) {
			st, p := baseState(), policy()
			wantDeposit := "0"
			ratio, available, equity := false, false, false
			hasRatio := true
			switch kind {
			case "ratio":
				st.CurrMargin = amount("850")
				wantDeposit = "62.50"
				ratio = true
			case "available":
				st.FrozenMargin = amount("580")
				st.FrozenCommission = amount("5")
				st.FrozenCash = amount("10")
				wantDeposit = "45"
				available = true
			case "equity":
				p.MinEquity = amount("1100")
				wantDeposit = "100"
				equity = true
			case "zero-equity":
				st.PreBalance = decimal.Zero
				st.CurrMargin = decimal.Zero
				st.ExchangeMargin = decimal.Zero
				wantDeposit = "100"
				available, equity, hasRatio = true, true, false
			case "negative-equity":
				st.PreBalance = decimal.Zero
				st.CloseProfit = amount("-100")
				st.CurrMargin = amount("40")
				st.ExchangeMargin = amount("20")
				wantDeposit = "200"
				available, equity, hasRatio = true, true, false
			case "equal-thresholds":
				st.CurrMargin = amount("800")
				p.MinEquity = amount("1000")
				p.MinAvailable = amount("200")
			}
			before, _ := json.Marshal(st)
			r, err := Assess(st, p)
			if err != nil {
				t.Fatal(err)
			}
			if r.RatioExceeded != ratio || r.AvailableBelowMinimum != available || r.EquityBelowMinimum != equity || r.Account.HasRiskRatio != hasRatio || r.NeedsMarginCall != (ratio || available || equity) || !r.RequiredDeposit.Equal(amount(wantDeposit)) {
				t.Fatalf("风控信号或手算补资错误 kind=%s report=%+v", kind, r)
			}
			if !r.Policy.MaxRiskRatio.Equal(p.MaxRiskRatio) || !r.Policy.MinEquity.Equal(p.MinEquity) || !r.Policy.MinAvailable.Equal(p.MinAvailable) {
				t.Fatal("风控报告丢失实际策略")
			}
			after, _ := json.Marshal(st)
			if string(before) != string(after) {
				t.Fatal("风控评估改动输入状态")
			}
			if r.RequiredDeposit.IsPositive() {
				acc, err := account.Restore(st)
				if err != nil {
					t.Fatal(err)
				}
				if err = acc.Deposit(st.Day, r.RequiredDeposit); err != nil {
					t.Fatal(err)
				}
				again, err := Assess(acc.State(), p)
				if err != nil || again.NeedsMarginCall || !again.RequiredDeposit.IsZero() {
					t.Fatalf("按报告补资后仍未满足策略 report=%+v err=%v", again, err)
				}
			}
		})
	}
}

// TestRiskExactBoundary 展示除法可能把正缺口取成0，实际信号不能被显示精度遮蔽。
func TestRiskExactBoundary(t *testing.T) {
	st := baseState()
	st.PreBalance = amount("1")
	st.CurrMargin = amount("1.00000000000000001")
	st.ExchangeMargin = decimal.Zero
	p := Policy{MaxRiskRatio: amount("1"), MinEquity: amount("0.01")}
	r, err := Assess(st, p)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Account.RiskRatio.Equal(p.MaxRiskRatio) {
		t.Fatal("展示精度反例未成立")
	}
	if !r.RatioExceeded || !r.NeedsMarginCall || !r.RequiredDeposit.Equal(amount("0.01")) {
		t.Fatalf("风险越线被展示除法隐藏 report=%+v", r)
	}
}

// TestRiskCeilingFromGap 防重复小数少补，也防先取整目标权益导致多补。
func TestRiskCeilingFromGap(t *testing.T) {
	for _, tc := range []struct {
		balance, deposit string
		call             bool
	}{{"3", "0.34", true}, {"3.3299", "0.01", true}, {"3.334", "0", false}} {
		st := baseState()
		st.PreBalance = amount(tc.balance)
		st.CurrMargin = amount("1")
		st.ExchangeMargin = decimal.Zero
		r, err := Assess(st, Policy{MaxRiskRatio: amount("0.3"), MinEquity: amount("0.01")})
		if err != nil {
			t.Fatal(err)
		}
		if r.NeedsMarginCall != tc.call || !r.RequiredDeposit.Equal(amount(tc.deposit)) {
			t.Fatalf("风险正缺口取整错误 balance=%s deposit=%s call=%v", tc.balance, r.RequiredDeposit, r.NeedsMarginCall)
		}
	}
}

// TestRiskMoneyCeiling 最低权益/可用的细分小数也必须向上到分，不能只照顾风险度那一项。
func TestRiskMoneyCeiling(t *testing.T) {
	for _, equity := range []bool{false, true} {
		st, p := baseState(), policy()
		p.MinAvailable = amount("600.001")
		if equity {
			p.MinAvailable = decimal.Zero
			p.MinEquity = amount("1000.001")
		}
		r, err := Assess(st, p)
		if err != nil || !r.NeedsMarginCall || !r.RequiredDeposit.Equal(amount("0.01")) {
			t.Fatalf("权益或可用缺口未向上取整 equity=%v report=%+v err=%v", equity, r, err)
		}
	}
}

// TestRiskUsesCompanyAndAvailable 确认公司口径、口径排除浮盈和冻结只扣一次。
func TestRiskUsesCompanyAndAvailable(t *testing.T) {
	st := baseState()
	st.PreBalance = amount("100")
	st.CurrMargin = amount("85")
	st.ExchangeMargin = amount("40")
	r, err := Assess(st, Policy{MaxRiskRatio: amount("0.8"), MinEquity: amount("0.01")})
	if err != nil || !r.RatioExceeded || !r.RequiredDeposit.Equal(amount("6.25")) {
		t.Fatalf("风控使用了交易所而非公司保证金 report=%+v err=%v", r, err)
	}
	st = baseState()
	st.PositionProfit = amount("100")
	for _, onlyLoss := range []bool{false, true} {
		st.Algorithm = account.AlgorithmAll
		if onlyLoss {
			st.Algorithm = account.AlgorithmOnlyLost
		}
		r, err = Assess(st, Policy{MaxRiskRatio: amount("0.8"), MinEquity: amount("1"), MinAvailable: amount("650")})
		want := decimal.Zero
		if onlyLoss {
			want = amount("50")
		}
		if err != nil || r.NeedsMarginCall != onlyLoss || !r.RequiredDeposit.Equal(want) {
			t.Fatalf("风控误把排除浮盈算进可用 onlyLoss=%v report=%+v err=%v", onlyLoss, r, err)
		}
	}
}

// TestRiskRejectsUnknownAndUnbounded 失败只能返回错误和零报告，不能给出假健康结论。
func TestRiskRejectsUnknownAndUnbounded(t *testing.T) {
	for _, kind := range []string{"ratio-zero", "ratio-negative", "equity-zero", "available-negative", "unknown-algorithm", "unknown-currency", "day", "negative-margin", "inconsistent-margin", "policy-exponent", "state-exponent", "coefficient", "result-budget"} {
		t.Run(kind, func(t *testing.T) {
			st, p := baseState(), policy()
			switch kind {
			case "ratio-zero":
				p.MaxRiskRatio = decimal.Zero
			case "ratio-negative":
				p.MaxRiskRatio = amount("-1")
			case "equity-zero":
				p.MinEquity = decimal.Zero
			case "available-negative":
				p.MinAvailable = amount("-1")
			case "unknown-algorithm":
				st.Algorithm = 0
			case "unknown-currency":
				st.Currency = "USD"
			case "day":
				st.Day = 0
			case "negative-margin":
				st.CurrMargin = amount("-1")
			case "inconsistent-margin":
				st.ExchangeMargin = amount("500")
			case "policy-exponent":
				p.MaxRiskRatio = decimal.New(1, 1000)
			case "state-exponent":
				st.FrozenCash = decimal.New(1, -1000)
			case "coefficient":
				st.PreBalance = decimal.NewFromBigInt(new(big.Int).Lsh(big.NewInt(1), 600), 0)
			case "result-budget":
				st.CurrMargin = decimal.NewFromBigInt(new(big.Int).Lsh(big.NewInt(1), 500), 64)
				st.ExchangeMargin = decimal.Zero
				p.MaxRiskRatio = decimal.New(1, -64)
			}
			r, err := Assess(st, p)
			if err == nil || r.NeedsMarginCall || r.Account.Currency != "" {
				t.Fatalf("未知风控输入被当健康 kind=%s report=%+v err=%v", kind, r, err)
			}
			if strings.Contains(kind, "exponent") || kind == "coefficient" || kind == "result-budget" {
				if !strings.Contains(err.Error(), "有限精度") {
					t.Fatalf("风控资源预算未独立拒绝 kind=%s err=%v", kind, err)
				}
			}
		})
	}
}
