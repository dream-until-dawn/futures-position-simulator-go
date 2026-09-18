package ctpfixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestSettleTruncationAgainstCTP：交易日 20260917 收盘后的账户 + 当日全部逐笔手续费 + 结算价 ⇒ 次日上日结存，与柜台一分不差。
//
// 这是 §13 #5「结算时逐笔截断到分」的对拍（design.md 门面形状 §14，F10）。实现之前跑过一次：本库给 19997514.852，差 0.028。
//
// ⚠️ 截断由本库的 fee.TruncateToCent 算 —— 不在测试里手写截断，否则是拿测试自己的算术验测试。
// ⚠️ 数据缺口照实写：ctp-fee-deltas-20260917.json 只有 DCE.j2701 那六笔；之前的四笔（E1 / E2 各 0.2、郑商所种子各 2，
// 见 state.md 的 #5 事前登记）只以合计出现在第一笔的 commission_before 里。四笔各自整分 ⇒ 按合计喂与逐笔喂截断结果相同 ——
// 这一句是**从记录推得**，本条只断言合计本身是整分。
func TestSettleTruncationAgainstCTP(t *testing.T) {
	fx := loadCTP(t)
	end, ok := fx["ctp-status-20260917.json"]
	if !ok {
		t.Fatal("⚠️ 缺 ctp-status-20260917.json（交易日 20260917 收盘后）")
	}
	next, ok := fx["ctp-slices-20260918.json"]
	if !ok {
		t.Fatal("⚠️ 缺 ctp-slices-20260918.json（交易日 20260918 的上日结存）")
	}
	const day, nextDay = types.TradingDay(20260917), types.TradingDay(20260918)
	if end.TradingDay != "20260917" || next.TradingDay != "20260918" {
		t.Fatalf("交易日 %s / %s，期望 20260917 / 20260918", end.TradingDay, next.TradingDay)
	}
	a, err := account.New("CNY", day, num(t, end.Account, "PreBalance").Round(6), account.AlgorithmOnlyLost)
	if err != nil {
		t.Fatal(err)
	}
	if !num(t, end.Account, "Deposit").IsZero() || !num(t, end.Account, "Withdraw").IsZero() {
		t.Fatal("⚠️ 当日有出入金 —— 本条没接这两项")
	}
	if err := a.AddCloseProfit(day, num(t, end.Account, "CloseProfit").Round(6)); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.FromSlash("../../testdata/refdata/ctp-fee-deltas-20260917.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		TradingDay string `json:"trading_day"`
		Before     string `json:"commission_before"`
		Fee        string `json:"fee"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 6 {
		t.Fatalf("⚠️ 逐笔费只有 %d 笔（下界 6）", len(rows))
	}
	earlier := decimal.RequireFromString(rows[0].Before).Round(6)
	if !earlier.Equal(earlier.Truncate(2)) {
		t.Fatalf("⚠️ 第一笔之前的合计 %s 不是整分 —— 「按合计喂与逐笔喂截断相同」的推断不成立", earlier)
	}
	fees := []decimal.Decimal{earlier}
	for _, r := range rows {
		if r.TradingDay != "20260917" {
			t.Fatalf("逐笔费的交易日是 %s", r.TradingDay)
		}
		fees = append(fees, decimal.RequireFromString(r.Fee).Round(6)) // 去柜台 double 的尾巴（§13 #5 登记的规则）
	}
	sum := decimal.Zero
	for _, f := range fees {
		at, err := fee.TruncateToCent.Apply(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.AddCommission(day, f, at); err != nil {
			t.Fatal(err)
		}
		sum = sum.Add(f)
	}
	if want := num(t, end.Account, "Commission").Round(6); !sum.Equal(want) {
		t.Fatalf("⚠️ 逐笔费合计 %s ≠ 账户手续费 %s —— 当日还有没记下的成交", sum, want)
	}

	// 过夜腿：结算时的持仓盈亏 = 结算价 × 手数 × 乘数 − 盯市基线（PositionCost）
	pp, legs := decimal.Zero, 0
	for key, p := range end.Positions {
		vol := num(t, p, "Position")
		if vol.IsZero() {
			continue
		}
		if key != "CZCE.MA701/2/1" {
			t.Fatalf("⚠️ 多出一条过夜腿 %s —— 本条只接了 MA701（乘数 10）", key)
		}
		settle := num(t, next.Quotes["CZCE.MA701"], "PreSettlementPrice")
		pp = pp.Add(settle.Mul(vol).Mul(decimal.NewFromInt(10)).Sub(num(t, p, "PositionCost")))
		legs++
	}
	if legs != 1 {
		t.Fatalf("过夜腿 %d 条，期望 1", legs)
	}
	if err := a.SetPositionProfit(day, pp); err != nil {
		t.Fatal(err)
	}
	if err := a.Settle(day, nextDay); err != nil {
		t.Fatal(err)
	}
	got, want := a.Snapshot().PreBalance, num(t, next.Account, "PreBalance").Round(6)
	if !got.Equal(want) {
		t.Errorf("⚠️ 次日上日结存：本库 %s，柜台 %s，差 %s（实现前差 0.028）", got, want, want.Sub(got))
	}
}
