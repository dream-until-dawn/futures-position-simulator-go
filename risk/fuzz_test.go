package risk

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/shopspring/decimal"
)

// FuzzRiskDeposit 检验补资充分且按分最小；金额范围有界，输入全为合成状态。
func FuzzRiskDeposit(f *testing.F) {
	f.Add(int64(1000), int64(0), uint64(850), uint64(0), uint16(80), false)
	f.Add(int64(1), int64(-100), uint64(40), uint64(10), uint16(30), true)
	f.Add(int64(10), int64(100), uint64(0), uint64(0), uint16(100), true)
	f.Fuzz(func(t *testing.T, base, profit int64, margin, reserve uint64, pct uint16, onlyLoss bool) {
		base %= 200000
		if base < 0 {
			base = -base
		}
		base++
		st := baseState()
		st.PreBalance = decimal.NewFromInt(base)
		st.PositionProfit = decimal.NewFromInt(profit % 200000)
		st.CurrMargin = decimal.NewFromInt(int64(margin % 1000000))
		st.ExchangeMargin = st.CurrMargin.Div(decimal.NewFromInt(2))
		st.FrozenMargin = decimal.NewFromInt(int64(reserve % 1000))
		if onlyLoss {
			st.Algorithm = account.AlgorithmOnlyLost
		}
		p := Policy{MaxRiskRatio: decimal.NewFromInt(int64(pct%200 + 1)).Div(decimal.NewFromInt(100)), MinEquity: decimal.NewFromInt(1)}
		r, err := Assess(st, p)
		if err != nil {
			t.Fatal(err)
		}
		if r.RequiredDeposit.IsNegative() || r.NeedsMarginCall != r.RequiredDeposit.IsPositive() {
			t.Fatalf("风控信号与补资不一致 %+v", r)
		}
		apply := func(deposit decimal.Decimal) Report {
			t.Helper()
			acc, err := account.Restore(st)
			if err != nil {
				t.Fatal(err)
			}
			if deposit.IsPositive() {
				if err = acc.Deposit(st.Day, deposit); err != nil {
					t.Fatal(err)
				}
			}
			out, err := Assess(acc.State(), p)
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		if after := apply(r.RequiredDeposit); after.NeedsMarginCall || !after.RequiredDeposit.IsZero() {
			t.Fatalf("补资不充分 before=%+v after=%+v", r, after)
		}
		if r.RequiredDeposit.IsPositive() {
			less := r.RequiredDeposit.Sub(decimal.New(1, -2))
			if after := apply(less); !after.NeedsMarginCall {
				t.Fatalf("补资多收至少一分 deposit=%s policy=%+v state=%+v", r.RequiredDeposit, p, st)
			}
		}
	})
}
