package account

import (
	"strings"
	"testing"
)

// TestNewRefusesUnmeasuredAlgorithm 钉住只有实测过的两个取值能开户。
func TestNewRefusesUnmeasuredAlgorithm(t *testing.T) {
	for _, c := range []struct {
		alg  Algorithm
		want string // 报错里要有的词；空串表示应当开户成功
	}{
		{AlgorithmUnset, "未指定"},
		{AlgorithmOnlyGain, "没有行为观测"},
		{AlgorithmNone, "没有行为观测"},
		{Algorithm(99), "认不得"},
		{AlgorithmAll, ""},
		{AlgorithmOnlyLost, ""},
	} {
		a, err := New("CNY", d1, d("1000"), c.alg)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s 应当开户成功：%v", c.alg, err)
		case c.want == "" && a.Algorithm != c.alg:
			t.Errorf("%s 开户后 Algorithm = %s", c.alg, a.Algorithm)
		case c.want != "" && err == nil:
			t.Errorf("⚠️ %s 开户成功了 —— 它没有观测，放行就是把枚举名当成了行为", c.alg)
		case c.want != "" && !strings.Contains(err.Error(), c.want):
			t.Errorf("%s 的报错要说「%s」：%v", c.alg, c.want, err)
		}
	}
}

// TestAvailableFollowsAlgorithm 钉住两种算法在**浮盈**上分开、在**浮亏**上一致。
//
// ⚠️ 两侧都要有：只测浮亏的话两种算法给同一个数，
// 这正是 SimNow 上十八份夹具把旧式供了一天的形状（§13 #17）。
func TestAvailableFollowsAlgorithm(t *testing.T) {
	open := func(alg Algorithm, pp string) *Account {
		a, err := New("CNY", d1, d("20000"), alg)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.SetMargin(d1, d("5000"), d("5000")); err != nil {
			t.Fatal(err)
		}
		if err := a.SetPositionProfit(d1, d(pp)); err != nil {
			t.Fatal(err)
		}
		mustCheck(t, a, "设持仓盈亏 "+pp)
		return a
	}
	for _, c := range []struct {
		pp               string
		wantAll, wantOnL string
	}{
		{"150", "15150", "15000"}, // 浮盈：只计浮亏时不进可用
		{"-40", "14960", "14960"}, // 浮亏：两种都立即扣
		{"0", "15000", "15000"},
	} {
		if got := open(AlgorithmAll, c.pp).Available(); !got.Equal(d(c.wantAll)) {
			t.Errorf("全部计算、持仓盈亏 %s：可用 %s，期望 %s", c.pp, got, c.wantAll)
		}
		if got := open(AlgorithmOnlyLost, c.pp).Available(); !got.Equal(d(c.wantOnL)) {
			t.Errorf("⚠️ 只计浮亏、持仓盈亏 %s：可用 %s，期望 %s", c.pp, got, c.wantOnL)
		}
	}
	// 结存不受算法影响：浮盈照样在权益里，只是不能用。
	if all, onl := open(AlgorithmAll, "150").Balance(), open(AlgorithmOnlyLost, "150").Balance(); !all.Equal(onl) {
		t.Errorf("⚠️ 结存随算法变了：%s / %s —— 算法只管可用", all, onl)
	}
}

// TestFloatingGainCannotFundOrdersUnderOnlyLost 钉住「只计浮亏」下浮盈不能拿去冻结、出金。
//
// ⚠️ 这是这条规则真正咬人的地方：Freeze / Withdraw 经 Available 判上限。
func TestFloatingGainCannotFundOrdersUnderOnlyLost(t *testing.T) {
	for _, alg := range []Algorithm{AlgorithmAll, AlgorithmOnlyLost} {
		a, err := New("CNY", d1, d("1000"), alg)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.SetPositionProfit(d1, d("500")); err != nil {
			t.Fatal(err)
		}
		freezeErr := a.Freeze(d1, d("1200"), d("0"))
		withdrawErr := a.Withdraw(d1, d("1200"))
		if alg == AlgorithmOnlyLost {
			if freezeErr == nil || withdrawErr == nil {
				t.Errorf("⚠️ 只计浮亏：可用 1000、浮盈 500，冻结 1200 得 %v、出金 1200 得 %v —— 浮盈被拿去用了",
					freezeErr, withdrawErr)
			}
			continue
		}
		// 反向：全部计算时同一笔要放行，否则一个「一律拒」的实现也能过上面。
		if freezeErr != nil {
			t.Errorf("全部计算：可用 1500 时冻结 1200 应放行：%v", freezeErr)
		}
	}
}
