package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/shopspring/decimal"
)

func loadCTPFixture(t *testing.T, name string) *ctp.Fixture {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "ctp", name))
	if err != nil {
		t.Fatal(err)
	}
	var f ctp.Fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return &f
}

// TestSettleResidualReproducesTheLeadOf5 钉住：判法从两份真截面上**复现**当初引出 §13 #5 的那个观测。
//
// §13 #5 记的是 20260914 → 20260915「推 19997481.3415、实 19997481.46，差 +0.1185」—— 那是手算的。
// ⚠️ 复现不出来，说明登记里的判法与当初手算的不是同一个东西，今晚读出来的残差就没法与旧线索对照。
func TestSettleResidualReproducesTheLeadOf5(t *testing.T) {
	end := loadCTPFixture(t, "ctp-status-20260914-10.json")
	next := loadCTPFixture(t, "ctp-slices-20260915.json")
	in, err := settleInputFrom(end, next, map[string]decimal.Decimal{"DCE.m2701": decimal.NewFromInt(10)})
	if err != nil {
		t.Fatal(err)
	}
	pred, res := settleResidual(in)
	if !pred.Equal(decimal.RequireFromString("19997481.3415")) || !res.Equal(decimal.RequireFromString("0.1185")) {
		t.Errorf("⚠️ 推算 %s、残差 %s，§13 #5 记的是 19997481.3415 / +0.1185 —— 判法与当初手算的不是同一个东西", pred, res)
	}
	if len(in.Legs) != 1 || in.Legs[0].Symbol != "DCE.m2701" || !in.Legs[0].Long || in.Legs[0].Volume != 1 {
		t.Errorf("⚠️ 过夜腿应恰好是 DCE.m2701 多 1 手，得到 %+v", in.Legs)
	}
}

// TestSettleVerdictIsEqualityNotNearest 钉住判定是「相等」而不是「最近」，落在五个之外返回空。
func TestSettleVerdictIsEqualityNotNearest(t *testing.T) {
	preds := settleRoundingResiduals(decs("0.2", "0.2", "2", "2", "12.455", "12.449", "12.455", "12.452", "12.455", "12.452"))
	if hit := settleVerdict(decimal.RequireFromString("0.028"), preds); len(hit) != 1 || hit[0] != "i-t" {
		t.Errorf("残差 0.028 应只命中 i-t，得到 %v", hit)
	}
	// 旧线索那种形状（+0.027）离 i-t 只差 0.001 —— 必须判 (ii)，不能凑成 i-t
	if hit := settleVerdict(decimal.RequireFromString("0.027"), preds); len(hit) != 0 {
		t.Errorf("⚠️ 残差 0.027 命中了 %v —— 登记里写死了「落在五个之外判 (ii)，不往最近的凑」", hit)
	}
	// double 尾巴不许让相等变成不等
	if hit := settleVerdict(decimal.RequireFromString("0.0280000000001"), preds); len(hit) != 1 {
		t.Errorf("⚠️ 带 1e-13 尾巴的 0.028 没命中 i-t：%v —— 去尾巴的规则没生效", hit)
	}
}

// TestSettleInputRefusesToFillIn 钉住：缺乘数、缺次日结算价、同一个交易日，一律报错不补。
func TestSettleInputRefusesToFillIn(t *testing.T) {
	end := loadCTPFixture(t, "ctp-status-20260914-10.json")
	next := loadCTPFixture(t, "ctp-slices-20260915.json")
	if _, err := settleInputFrom(end, next, map[string]decimal.Decimal{}); err == nil || !strings.Contains(err.Error(), "没有给乘数") {
		t.Errorf("⚠️ 缺乘数应当报错：%v", err)
	}
	noQuote := *next
	noQuote.Quotes = map[string]map[string]any{}
	if _, err := settleInputFrom(end, &noQuote, map[string]decimal.Decimal{"DCE.m2701": decimal.NewFromInt(10)}); err == nil || !strings.Contains(err.Error(), "结算价补不上") {
		t.Errorf("⚠️ 次日缺行情应当报错：%v", err)
	}
	if _, err := settleInputFrom(end, end, map[string]decimal.Decimal{"DCE.m2701": decimal.NewFromInt(10)}); err == nil || !strings.Contains(err.Error(), "同一个交易日") {
		t.Errorf("⚠️ 两份截面同一个交易日应当报错：%v", err)
	}
}

// TestSettleResidualUsesPositionCostNotOpenCost 用一条**昨仓**钉住盯市基线取 PositionCost。
//
// ⚠️ 历史那一对（TestSettleResidualReproducesTheLeadOf5）里过夜的是当天开的今仓，OpenCost == PositionCost，
// 两种基线在它上面同值 —— 破坏 673 第一次跑就「仍然绿」，这一条是补的。
//
//	开仓 3000（两天前）、昨结 3100、今结 3150，多 1 手、乘数 10
//	按 PositionCost（31000）盯市  +500   ⇒ 推算 1500
//	按 OpenCost（30000）盯市      +1500  ⇒ 推算 2500（上一次结算已兑现的 +1000 被再算一遍）
func TestSettleResidualUsesPositionCostNotOpenCost(t *testing.T) {
	end := &ctp.Fixture{TradingDay: "20260917",
		Account: map[string]any{"PreBalance": 1000.0, "Deposit": 0.0, "Withdraw": 0.0, "CloseProfit": 0.0, "Commission": 0.0},
		Positions: map[string]map[string]any{"DCE.x2701/2/2": {
			"Position": 1.0, "PosiDirection": "2", "OpenCost": 30000.0, "PositionCost": 31000.0}}}
	next := &ctp.Fixture{TradingDay: "20260918",
		Account: map[string]any{"PreBalance": 1500.0},
		Quotes:  map[string]map[string]any{"DCE.x2701": {"PreSettlementPrice": 3150.0}}}
	in, err := settleInputFrom(end, next, map[string]decimal.Decimal{"DCE.x2701": decimal.NewFromInt(10)})
	if err != nil {
		t.Fatal(err)
	}
	pred, res := settleResidual(in)
	if !pred.Equal(decimal.NewFromInt(1500)) || !res.IsZero() {
		t.Errorf("⚠️ 推算 %s、残差 %s，应为 1500 / 0 —— 盯市基线用的不是 PositionCost（用 OpenCost 会得到 2500 / −1000）", pred, res)
	}
	// 空头同理，方向反过来
	end.Positions["DCE.x2701/2/2"]["PosiDirection"] = "3"
	next.Account["PreBalance"] = 500.0
	in, err = settleInputFrom(end, next, map[string]decimal.Decimal{"DCE.x2701": decimal.NewFromInt(10)})
	if err != nil {
		t.Fatal(err)
	}
	if pred, res := settleResidual(in); !pred.Equal(decimal.NewFromInt(500)) || !res.IsZero() {
		t.Errorf("⚠️ 空头：推算 %s、残差 %s，应为 500 / 0", pred, res)
	}
}
