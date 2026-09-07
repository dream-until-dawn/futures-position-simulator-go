package decimalx

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TestLibrarySemantics 直接向 shopspring/decimal 求证它的取整语义。
//
// ⚠️ 本包的文档注释里写着「Round 是远离零、Truncate 是朝零」——
// **那两句不是我知道的，是这条测试问出来的。**
//
// 依赖库的语义如果只写在注释里而没有测试，它就是一条未经验证的断言；
// 而库升级时语义若变了，**没有任何东西会报警**。
func TestLibrarySemantics(t *testing.T) {
	cases := []struct {
		in           string
		wantRound    string
		wantTruncate string
		why          string
	}{
		{"1.005", "1.01", "1.00", "正数半分：Round 进位，Truncate 舍去"},
		{"1.004", "1.00", "1.00", "正数不足半分"},
		{"1.019", "1.02", "1.01", "正数：两者差一分"},
		{"-1.005", "-1.01", "-1.00", "⚠️ 负数半分：Round 远离零，Truncate 朝零"},
		{"-1.019", "-1.02", "-1.01", "⚠️ 负数：两者差一分，方向与正数相反"},
		{"2.000", "2.00", "2.00", "整数不受影响"},
		{"0", "0", "0", "零"},
	}
	if len(cases) != 7 {
		t.Fatalf("用例数应为 7，实际 %d —— 增删了就同步更新下界", len(cases))
	}
	for _, c := range cases {
		v := d(c.in)
		if got := v.Round(Cents); !got.Equal(d(c.wantRound)) {
			t.Errorf("Round(%s) = %s，期望 %s（%s）—— "+
				"⚠️ 若这条红了，说明依赖库的取整语义变了，本包的注释与实现都要复查",
				c.in, got, c.wantRound, c.why)
		}
		if got := v.Truncate(Cents); !got.Equal(d(c.wantTruncate)) {
			t.Errorf("Truncate(%s) = %s，期望 %s（%s）", c.in, got, c.wantTruncate, c.why)
		}
	}
}

// TestRoundingUnmeasuredRefuses 断言未实测的取整口径**拒绝运行**。
func TestRoundingUnmeasuredRefuses(t *testing.T) {
	_, err := RoundingUnmeasured.Apply(d("1.005"))
	if err == nil {
		t.Fatal("⚠️ 取整口径未实测时本该报错，而不是挑一个默认")
	}
	if !strings.Contains(err.Error(), "实验 5") {
		t.Errorf("报错应指向实验 5，实为 %v", err)
	}
}

// TestThreeCandidatesDiffer 断言三个候选在**同一个输入**上给出不同的数。
//
// ⚠️ 这正是它需要被实测的原因，也是「拿一个整数样本去验证取整实现了」
// 会验不出任何东西的原因。
func TestThreeCandidatesDiffer(t *testing.T) {
	in := d("1.005")
	got := map[string]decimal.Decimal{}
	for _, r := range []Rounding{HalfUpToCent, TruncateToCent, NoRounding} {
		v, err := r.Apply(in)
		if err != nil {
			t.Fatalf("%v 失败: %v", r, err)
		}
		got[r.String()] = v
	}
	if got["四舍五入到分"].Equal(got["截断到分"]) {
		t.Errorf("⚠️ 四舍五入与截断在 %s 上同值 —— 那样这个样本没有判别力", in)
	}
	if got["不取整"].Equal(got["四舍五入到分"]) {
		t.Errorf("⚠️ 不取整与四舍五入在 %s 上同值", in)
	}

	// 整数样本上三者必然同值 —— 把这件事钉住，免得有人拿它去「验证」。
	whole := d("2")
	var first decimal.Decimal
	for i, r := range []Rounding{HalfUpToCent, TruncateToCent, NoRounding} {
		v, _ := r.Apply(whole)
		if i == 0 {
			first = v
			continue
		}
		if !v.Equal(first) {
			t.Fatalf("前提变了：整数上三者本应同值，%v 给出 %s", r, v)
		}
	}
}

// TestIsTickMultiple 覆盖最小变动价位的整数倍判定。
func TestIsTickMultiple(t *testing.T) {
	cases := []struct {
		price, tick string
		want        bool
		why         string
	}{
		{"3150", "1", true, "螺纹 tick=1"},
		{"3150.5", "1", false, "半个 tick"},
		{"512.34", "0.02", true, "沪金 tick=0.02，512.34 = 25617×0.02"},
		{"512.35", "0.02", false, "沪金：不是 0.02 的整数倍"},
		{"3.005", "0.005", true, "国债 tick=0.005"},
		{"3.003", "0.005", false, "国债：不是整数倍"},
		{"7600", "0.2", true, "股指 tick=0.2"},
		{"7600.1", "0.2", false, "股指：半个 tick"},
	}
	if len(cases) != 8 {
		t.Fatalf("用例数应为 8，实际 %d", len(cases))
	}
	for _, c := range cases {
		got, err := IsTickMultiple(d(c.price), d(c.tick))
		if err != nil {
			t.Errorf("%s / %s 报错: %v", c.price, c.tick, err)
			continue
		}
		if got != c.want {
			t.Errorf("IsTickMultiple(%s, %s) = %v，期望 %v（%s）",
				c.price, c.tick, got, c.want, c.why)
		}
	}
	if _, err := IsTickMultiple(d("3150"), decimal.Zero); err == nil {
		t.Error("tick 为零本该报错")
	}
}

// TestAlignToTick 覆盖三个对齐方向。
func TestAlignToTick(t *testing.T) {
	cases := []struct {
		price, tick string
		mode        TickMode
		want        string
	}{
		{"512.35", "0.02", TickNearest, "512.36"},
		{"512.35", "0.02", TickDown, "512.34"},
		{"512.35", "0.02", TickUp, "512.36"},
		{"3150.4", "1", TickDown, "3150"},
		{"3150.4", "1", TickUp, "3151"},
		{"3150.4", "1", TickNearest, "3150"},
		{"3150.6", "1", TickNearest, "3151"},
		{"3150", "1", TickDown, "3150"},
	}
	if len(cases) != 8 {
		t.Fatalf("用例数应为 8，实际 %d", len(cases))
	}
	for _, c := range cases {
		got, err := AlignToTick(d(c.price), d(c.tick), c.mode)
		if err != nil {
			t.Errorf("%s 对齐失败: %v", c.price, err)
			continue
		}
		if !got.Equal(d(c.want)) {
			t.Errorf("AlignToTick(%s, %s, %d) = %s，期望 %s", c.price, c.tick, c.mode, got, c.want)
		}
		// 对齐后必须真的是整数倍 —— 否则对齐函数自己就错了。
		ok, _ := IsTickMultiple(got, d(c.tick))
		if !ok {
			t.Errorf("⚠️ 对齐后的 %s 仍不是 %s 的整数倍", got, c.tick)
		}
	}

	// TickDown 与 TickUp 在**已经对齐**的价格上同值 —— 钉住这个无判别力的样本。
	down, _ := AlignToTick(d("3150"), d("1"), TickDown)
	up, _ := AlignToTick(d("3150"), d("1"), TickUp)
	if !down.Equal(up) {
		t.Fatalf("前提变了：已对齐的价格上两个方向本应同值，实为 %s vs %s", down, up)
	}

	bad := []struct {
		price, tick string
		mode        TickMode
	}{
		{"3150", "0", TickNearest},
		{"3150", "-1", TickNearest},
		{"0", "1", TickNearest},
		{"-3150", "1", TickNearest},
		{"3150", "1", TickMode(99)},
	}
	if len(bad) != 5 {
		t.Fatalf("反例数应为 5，实际 %d", len(bad))
	}
	for _, c := range bad {
		if _, err := AlignToTick(d(c.price), d(c.tick), c.mode); err == nil {
			t.Errorf("AlignToTick(%s, %s, %d) 本该报错", c.price, c.tick, c.mode)
		}
	}
}
