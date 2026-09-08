package probe

import (
	"math"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

// TestSolveBaselineRecoversKnownBaseline 正向：给定已知基线合成两个盈亏字段，
// 看反解能否把基线还原回来 —— 并且**不用合约乘数**。
func TestSolveBaselineRecoversKnownBaseline(t *testing.T) {
	cases := []struct {
		name                   string
		last, openPx, baseline float64
		vol, mult              float64
	}{
		{"昨结算低于开仓价", 3160, 3150, 3140, 2, 10},
		{"昨结算高于开仓价", 3100, 3150, 3180, 3, 10},
		{"基线就等于开仓价", 3200, 3150, 3150, 1, 10},
		{"乘数很大（沪金）", 512.50, 512.34, 511.90, 8, 1000},
		{"手数与乘数都变，解应不变", 3160, 3150, 3140, 7, 300},
	}
	// ⚠️ 下界断言用确切条数，不是 > 0：`> 0` 在用例被删到只剩一条时照样绿。
	if len(cases) != 5 {
		t.Fatalf("用例数应为 5，实际 %d —— 用例被增删了，请同步更新下界", len(cases))
	}
	for _, c := range cases {
		fp := (c.last - c.openPx) * c.vol * c.mult
		pp := (c.last - c.baseline) * c.vol * c.mult
		got, ok := solveBaseline(c.last, c.openPx, fp, pp)
		if !ok {
			t.Errorf("%s: 反解失败", c.name)
			continue
		}
		if math.Abs(got-c.baseline) > 1e-6 {
			t.Errorf("%s: 反解 = %v，期望 %v", c.name, got, c.baseline)
		}
	}
}

// TestSolveBaselineRefusesWhenUndeterminable 反向：分母接近零时必须报「解不出」，
// 而不是返回一个看起来合理的数。
//
// ⚠️ 这正是「阴性结果不能当阳性用」：解不出来是「测不出」，
// 不是「基线等于开仓价」。若这里退化成返回 (openPx, true)，
// 上层会把它读成一个结论。
func TestSolveBaselineRefusesWhenUndeterminable(t *testing.T) {
	bad := []struct {
		name                 string
		last, openPx, fp, pp float64
	}{
		{"浮动盈亏恰为零（价格贴着开仓价）", 3150, 3150, 0, 12.5},
		{"浮动盈亏在阈值内", 3150.0000001, 3150, 1e-12, 12.5},
		{"浮动盈亏为负零", 3150, 3150, math.Copysign(0, -1), -12.5},
	}
	if len(bad) != 3 {
		t.Fatalf("反例数应为 3，实际 %d", len(bad))
	}
	for _, c := range bad {
		if _, ok := solveBaseline(c.last, c.openPx, c.fp, c.pp); ok {
			t.Errorf("%s: 本该报解不出，却返回了一个值", c.name)
		}
	}
}

// TestRequireYesterdayGuard 双向验证昨仓前置守卫。
//
// 没有昨仓时这几条实验的候选全部同值，跑出来的绿色什么都不说明 ——
// 与实验 3 用单合约跑是同一个形状，所以必须**拒绝运行**。
func TestRequireYesterdayGuard(t *testing.T) {
	reject := []struct {
		name string
		pos  map[string]any
		dir  kq.Direction
	}{
		{"完全没有持仓字段", map[string]any{}, kq.Buy},
		{"只有今仓", map[string]any{"volume_long_today": 2.0, "volume_long_his": 0.0}, kq.Buy},
		{"多头有昨仓但问的是空头", map[string]any{"volume_long_his": 2.0}, kq.Sell},
		{"截面为 nil", nil, kq.Buy},
	}
	if len(reject) != 4 {
		t.Fatalf("反例数应为 4，实际 %d", len(reject))
	}
	for _, c := range reject {
		if _, err := yesterdayLots(c.pos, "X.y2701", c.dir); err == nil {
			t.Errorf("守卫放过了本该被拒的样本：%s", c.name)
		}
	}

	accept := []struct {
		name string
		pos  map[string]any
		dir  kq.Direction
		want float64
	}{
		{"多头昨仓 2 手", map[string]any{"volume_long_his": 2.0}, kq.Buy, 2},
		{"空头昨仓 3 手", map[string]any{"volume_short_his": 3.0}, kq.Sell, 3},
	}
	if len(accept) != 2 {
		t.Fatalf("正例数应为 2，实际 %d", len(accept))
	}
	for _, c := range accept {
		got, err := yesterdayLots(c.pos, "X.y2701", c.dir)
		if err != nil {
			t.Errorf("守卫拒绝了合法样本 %s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: 返回 %v，期望 %v", c.name, got, c.want)
		}
	}
}
