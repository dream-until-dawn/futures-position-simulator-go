package main

import (
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
)

// TestMatcherDoesNotCrossProducts 断言品种前缀不会串到别的品种上。
//
// ⚠️ 这不是假想的边界：大商所同时有 c（玉米）与 cs（淀粉）。
// 光用 HasPrefix 的话 DCE.c 会把 DCE.cs2701 也拉进来，
// 而它不会报错 —— 多出来的品种带着自己的时段表进汇总，
// 「同品种内一致」那道检查也查不出它，因为它是**另一个**品种。
func TestMatcherDoesNotCrossProducts(t *testing.T) {
	m := matcher([]string{"DCE.c", "SHFE.a"})
	cases := []struct {
		id   string
		want bool
	}{
		{"DCE.c2701", true},   // 玉米，要
		{"DCE.cs2701", false}, // ⚠️ 淀粉，不要 —— 光 HasPrefix 会中
		{"SHFE.a2701", true},
		{"SHFE.ag2702", false}, // ⚠️ 白银，不要
		{"DCE.c", false},       // 只有品种没有月份
		{"DCE.m2701", false},   // 没要的品种
		{"KQ.i@DCE.c", false},  // 合成指数，不要
	}
	if len(cases) != 7 {
		t.Fatalf("用例 %d 条，应为 7 —— 增删了就同步改这个数", len(cases))
	}
	for _, c := range cases {
		if got := m(c.id); got != c.want {
			t.Errorf("⚠️ %s：判为 %v，应为 %v", c.id, got, c.want)
		}
	}
}

// TestMatcherEmptyWantMatchesNothing 断言空清单不会变成「全都要」。
func TestMatcherEmptyWantMatchesNothing(t *testing.T) {
	m := matcher(nil)
	for _, id := range []string{"DCE.c2701", "SHFE.rb2701", ""} {
		if m(id) {
			t.Errorf("⚠️ 空品种清单却匹配上了 %q —— 空不该等于全要", id)
		}
	}
}

// TestExpireDateRefusesUnexpectedClock 断言**到期时刻的约定变了要报错**，不是照样取日期。
//
// ⚠️ 20260910 补：这条守卫此前**一个测试都没有**，而它自己的注释写着
// 「约定变了，取日期部分可能整体偏一天，而偏一天不会有任何动静」——
//
//	一句写下了自己失效后果的注释，底下没有任何东西验着它。
//
// 到期日整体偏一天的后果：`Listed` 会在最后一个交易日把仍在市的合约当成已到期
// （或反过来），而两种都表现为「同步成功，合约数少了几个/多了几个」。
func TestExpireDateRefusesUnexpectedClock(t *testing.T) {
	cn := refdata.CNZone()
	// 实测约定：到期时刻是当地 15:00:00。
	ok := time.Date(2026, 1, 15, 15, 0, 0, 0, cn)
	got, err := expireDate(float64(ok.Unix()))
	if err != nil {
		t.Fatalf("⚠️ 15:00:00 是实测约定，本该接受：%v", err)
	}
	if got != "20260115" {
		t.Errorf("到期日解成 %q，应为 20260115", got)
	}
	// ⚠️ 判别力所在：**差一小时**必须被拒，而不是照样取日期部分。
	// 只测 15:00 那一侧的话，一个「什么都接受」的实现也能过。
	bad := time.Date(2026, 1, 15, 16, 0, 0, 0, cn)
	if _, err := expireDate(float64(bad.Unix())); err == nil {
		t.Error("⚠️ 到期时刻 16:00 被接受了 —— 约定变了却没人喊，" +
			"而它的后果是到期日整体偏一天，且偏一天不会有任何动静")
	}
	// ⚠️ 最危险的一格：**00:30**。取日期部分会落到前一天，
	// 而那个日期看起来完全正常。
	midnight := time.Date(2026, 1, 15, 0, 30, 0, 0, cn)
	if _, err := expireDate(float64(midnight.Unix())); err == nil {
		t.Error("⚠️ 到期时刻 00:30 被接受了 —— 这一格取出来的日期看起来最正常")
	}
	// ⚠️ 缺失那一格要连**诊断**一起钉。
	//
	// 把 `if ts <= 0` 关掉之后：time.Unix(0,0) 落在 1970-01-01 **08:00**，
	// 而 15:00 那个守卫照样拦下它 ⇒ **err 仍然非 nil，只查 err==nil 的话全绿**。
	// 变的只有理由：从「到期时刻缺失或非正」变成「到期时刻是 1970-01-01 08:00:00」。
	//
	//	⚠️ 两个守卫叠在同一个结果上时，删掉外面那个不改变结果，只删掉诊断 ——
	//	而一句说错了原因的诊断，会把查的人带去改另一个地方。
	if err0, want := errText(expireDate(0)), "缺失或非正"; !strings.Contains(err0, want) {
		t.Errorf("⚠️ ts=0 报的是 %q，期望含 %q —— "+
			"外层那个 `ts <= 0` 多半没了，而 1970-01-01 08:00 被 15:00 那条顺手拦下，"+
			"于是结果看起来还是对的", err0, want)
	}
}

// errText 取错误文本；没有错误时返回一个显眼的占位，让断言红在该红的地方。
func errText(_ string, err error) string {
	if err == nil {
		return "（没有报错）"
	}
	return err.Error()
}
