package futsim

// 跨包守卫：每一个「未实测参数」的检查，在**退化输入**下也必须触发。
//
// ⚠️ 这条测试是被一个真实的洞逼出来的。2026-09-07 评审实测：
//
//	margin.Compute(nil, PriceBasisUnmeasured, ByProduct) → (Result{}, nil)   没报错
//	margin.Compute(nil, SettlementAll, SideScopeUnmeasured) → error          报了
//
// 病灶是**不对称**：`scope` 是前置检查，`basis` 只在逐 leg 循环里才被消费，
// 而 `len(legs)==0` 的早返回在它之前。
//
// 零值报错的设计意图**不是算对数，是告诉调用方它没配这个参数**。
// 空仓时放行意味着：引擎在空仓状态下初始化并跑通第一步会拿到绿灯，
// 等它第一次开仓错误才出现 —— **配置错误与它的报告点被错开在不同时间、
// 不同位置**。
//
// 形状上它是「零值假通过」的同族：**守卫通过，是因为什么都没被执行。**
//
// 所以这条测试用**最退化的输入**（空切片、零手数）逐个穿透所有未实测参数：
// 若某个守卫只在「真的有东西要算」时才触发，它就会在这里露出来。

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/pnl"
	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestUnmeasuredGuardsFireOnDegenerateInput 断言每个未实测参数在退化输入下仍报错。
func TestUnmeasuredGuardsFireOnDegenerateInput(t *testing.T) {
	one := decimal.NewFromInt(1)

	cases := []struct {
		name     string
		call     func() error
		wantHint string // 报错里必须点名的实验编号
	}{
		{
			name:     "margin: 计价基准未实测 + 空持仓",
			call:     func() error { _, err := margin.Compute(nil, margin.PriceBasisUnmeasured, margin.ByProduct); return err },
			wantHint: "实验 1",
		},
		{
			name: "margin: 合并范围未实测 + 空持仓",
			call: func() error {
				_, err := margin.Compute(nil, margin.SettlementAll, margin.SideScopeUnmeasured)
				return err
			},
			wantHint: "实验 3",
		},
		{
			name: "margin: 两个都未实测 + 空持仓",
			call: func() error {
				_, err := margin.Compute(nil, margin.PriceBasisUnmeasured, margin.SideScopeUnmeasured)
				return err
			},
			wantHint: "实验",
		},
		{
			name: "pnl: 计价基准未实测 + 空持仓",
			call: func() error {
				_, err := pnl.PositionProfit(nil, types.Buy, pnl.Prices{}, pnl.MarkUnmeasured, one)
				return err
			},
			wantHint: "实验 1/2",
		},
		{
			name: "pnl: 浮动盈亏同样",
			call: func() error {
				_, err := pnl.FloatProfit(nil, types.Buy, pnl.Prices{}, pnl.MarkUnmeasured, one)
				return err
			},
			wantHint: "实验 1/2",
		},
		{
			name:     "decimalx: 取整口径未实测 + 零金额",
			call:     func() error { _, err := decimalx.RoundingUnmeasured.Apply(decimal.Zero); return err },
			wantHint: "实验 5",
		},
		{
			name: "fee: 取整口径未实测 + 零费率",
			call: func() error {
				_, err := fee.Compute(fee.Rates{}, types.Open, one, one, 1, decimalx.RoundingUnmeasured)
				return err
			},
			wantHint: "实验 5",
		},
	}
	// ⚠️ 下界用确切条数：新增一个未实测参数却忘了加用例，这条先红。
	if len(cases) != 7 {
		t.Fatalf("用例数应为 7，实际 %d —— 新增未实测参数时必须同步加一条穿透用例", len(cases))
	}

	for _, c := range cases {
		err := c.call()
		if err == nil {
			t.Errorf("⚠️ %s：本该报错，却静默通过 —— "+
				"守卫可能只在「真的有东西要算」时才触发，"+
				"那样配置错误与它的报告点会被错开在不同时间、不同位置", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantHint) {
			t.Errorf("%s：报错应点名「%s」，实为 %v", c.name, c.wantHint, err)
		}
	}
}

// TestCloseOrderIsNotAlwaysRequired 记录一个**刻意的例外**。
//
// ⚠️ position.CloseOrder 只在「消耗顺序真正起作用」时才被要求——
// 显式 CloseToday / CloseYesterday 本就不需要它。
//
// 这不是漏，是对的：**把一个用不到的参数也强制要求，会逼调用方随便填一个**，
// 而随便填的那个值在别的调用点就是错的。这条测试把这个例外钉住，
// 免得下一个人按上面那条「所有未实测参数都要前置」的规律把它「修」成必填。
func TestCloseOrderIsNotAlwaysRequired(t *testing.T) {
	const d1, d2 = types.TradingDay(20260907), types.TradingDay(20260908)
	inst, err := types.ParseNative(types.SHFE, "rb2701", d1)
	if err != nil {
		t.Fatal(err)
	}
	p, err := position.New(inst, types.Speculation, d1, refdata.UseHistory)
	if err != nil {
		t.Fatal(err)
	}
	px := decimal.NewFromInt(3150)
	if err := p.Open(types.Buy, d1, px, 1); err != nil {
		t.Fatal(err)
	}

	// 显式平今：不需要消耗顺序，零值也应放行。
	if _, err := p.Close(types.Buy, types.CloseToday, d1, 1, position.CloseOrderUnmeasured); err != nil {
		t.Errorf("显式平今不需要消耗顺序，不该报错：%v", err)
	}

	// 而裸 Close 需要：同样的零值必须报错。
	q, _ := position.New(inst, types.Speculation, d1, refdata.UseHistory)
	_ = q.Open(types.Buy, d1, px, 1)
	_ = q.Settle(d1, decimal.NewFromInt(3160), d2)
	_ = q.Open(types.Buy, d2, decimal.NewFromInt(3170), 1)
	if _, err := q.Close(types.Buy, types.Close, d2, 1, position.CloseOrderUnmeasured); err == nil {
		t.Error("⚠️ 裸 Close 需要消耗顺序，零值本该报错")
	}
}
