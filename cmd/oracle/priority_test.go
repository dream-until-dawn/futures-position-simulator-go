package main

import (
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
	"github.com/dream-until-dawn/futures-position-simulator-go/ctperr"
)

func code(space ctperr.Space, v int) ctperr.Code { return ctperr.Code{Space: space, Value: v} }

// TestPairVerdict 钉住「同时违反两项时报的是哪一项」的判定。
//
// ⚠️ 判据是**码相等**而不是关键字。判别力集中在三个「判不了」的分支上 ——
// 一个只会回答 a 或 b 的实现，会把下面三种情形全部说成一个结论：
//
//	两项的码撞在一起   这一对分不开，不是「都赢了」
//	有一项没有观测     拿到的码归不到任何一项
//	两个都对不上       这一笔违反的多半不是我以为的那两项（20260914 bc 那次正是）
func TestPairVerdict(t *testing.T) {
	sess := side{Code: code(ctperr.SpaceStatusPrefix, 26), Name: "交易时段", Has: true}
	tick := side{Code: code(ctperr.SpaceStatusPrefix, 48), Name: "最小变动价位", Has: true}
	for _, c := range []struct {
		name       string
		got        ctperr.Code
		a, b       side
		wantWinner string
		wantWhyHas string
	}{
		{"拿到 a 的码 ⇒ a 先", sess.Code, sess, tick, "交易时段", "单独违反时的码"},
		{"拿到 b 的码 ⇒ b 先", tick.Code, sess, tick, "最小变动价位", "单独违反时的码"},
		{"两项码撞号 ⇒ 分不开", sess.Code, sess, sess, "", "这一对分不开"},
		{"b 没观测 ⇒ 判不了", tick.Code, sess, side{Name: "最小变动价位"}, "", "判不了"},
		{"a 没观测 ⇒ 判不了", tick.Code, side{Name: "交易时段"}, tick, "", "判不了"},
		{"两个都对不上 ⇒ 谁都没预言到", code(ctperr.SpaceCTP, 99), sess, tick, "", "谁都没预言到"},
		// ⚠️ 码空间这一维不许丢：值相同、空间不同的两个码是**两个**码。
		{"值同而空间不同 ⇒ 不算命中", code(ctperr.SpaceCTP, 26), sess, tick, "", "谁都没预言到"},
	} {
		winner, why := pairVerdict(c.got, c.a, c.b)
		if winner != c.wantWinner {
			t.Errorf("⚠️ %s：判成 %q，应为 %q（why=%s）", c.name, winner, c.wantWinner, why)
		}
		if !strings.Contains(why, c.wantWhyHas) {
			t.Errorf("⚠️ %s 的说明没说到点上（要含 %q）：%s", c.name, c.wantWhyHas, why)
		}
	}
}

// TestPairVerdictIsSymmetric 钉住两项**换个位置**给出同一个结论。
//
// ⚠️ 「谁先」是柜台的性质，不是我传参顺序的性质。而一个先比 a 再比 b 的实现，
// 在「撞号」那一档上会随参数顺序给出不同答案 —— 那种偏向在真数据上看不出来。
func TestPairVerdictIsSymmetric(t *testing.T) {
	sess := side{Code: code(ctperr.SpaceStatusPrefix, 26), Name: "交易时段", Has: true}
	tick := side{Code: code(ctperr.SpaceStatusPrefix, 48), Name: "最小变动价位", Has: true}
	for _, got := range []ctperr.Code{sess.Code, tick.Code, code(ctperr.SpaceCTP, 99)} {
		w1, _ := pairVerdict(got, sess, tick)
		w2, _ := pairVerdict(got, tick, sess)
		if w1 != w2 {
			t.Errorf("⚠️ 拿到 %s 时，两项换个位置就换了答案（%q vs %q）—— "+
				"「谁先」是柜台的性质，不是传参顺序的性质", got, w1, w2)
		}
	}
	// 撞号那一档也要对称（它是最容易被顺序偏向吃掉的一档）。
	same := side{Code: sess.Code, Name: "另一项", Has: true}
	if w1, _ := pairVerdict(sess.Code, sess, same); w1 != "" {
		t.Errorf("⚠️ 撞号却判出了赢家 %q", w1)
	}
	if w2, _ := pairVerdict(sess.Code, same, sess); w2 != "" {
		t.Errorf("⚠️ 撞号（换个位置）却判出了赢家 %q", w2)
	}
}

// TestCaseReasonCoversEveryCase 钉住四条用例**每一条**都映射得到一个拒因。
//
// ⚠️ 映射不上的那一条会安静地走「另一项没有观测」那一支，
// 于是它永远判不出先后 —— 而输出上看起来只是「这一对判不了」，像是柜台的问题。
func TestCaseReasonCoversEveryCase(t *testing.T) {
	if len(rejectCases) == 0 {
		t.Fatal("⚠️ 用例清单是空的 —— 下面的循环一条也不跑")
	}
	for _, rc := range rejectCases {
		r, ok := caseReason(rc)
		if !ok || r == ctperr.ReasonUnknown {
			t.Errorf("⚠️ 用例 %q（violates %v、off %q）映射不到拒因 —— "+
				"它会安静地走「另一项没有观测」那一支，永远判不出先后",
				rc.Name, rc.Violates, string(rc.Off))
		}
	}
	// 反向：一个恒返回某个拒因的映射也能让上面全绿 —— 钉住它确实在分辨。
	seen := map[ctperr.Reason]bool{}
	for _, rc := range rejectCases {
		if r, ok := caseReason(rc); ok {
			seen[r] = true
		}
	}
	if len(seen) < 3 {
		t.Errorf("⚠️ 四条用例只映射出 %d 种拒因 —— 至少该有最小变动价位 / 涨停 / 跌停 / 可平量 里的三种", len(seen))
	}
}

// TestCodeOfPrefersCTPSpace 钉住两个码位的取法：ErrorID 非零时是 CTP 空间，否则看前缀。
//
// ⚠️ 「缺」与「零」必须分得开：价格类拒单的 ErrorID **恒为 0**，
// 把它当成「CTP 码 0」会让每一条价格类观测都归到一个不存在的码上。
func TestCodeOfPrefersCTPSpace(t *testing.T) {
	if c, ok := codeOf(ctp.OrderState{ErrorID: 30, StatusMsg: "CTP:平仓量超过持仓量"}); !ok ||
		c != code(ctperr.SpaceCTP, 30) {
		t.Errorf("⚠️ ErrorID 非零时要取 CTP 空间，得到 %v %v", c, ok)
	}
	if c, ok := codeOf(ctp.OrderState{ErrorID: 0, StatusMsg: "48:价格不是最小变动价位的整数倍"}); !ok ||
		c != code(ctperr.SpaceStatusPrefix, 48) {
		t.Errorf("⚠️ ErrorID 为零时要取前缀码，得到 %v %v", c, ok)
	}
	if _, ok := codeOf(ctp.OrderState{StatusMsg: "没有前缀码的一段文本"}); ok {
		t.Error("⚠️ 一个码都没有时要说没有 —— 不许造一个 0 出来")
	}
}

// TestPriorityUsesTheSameInputsAsReject 钉住这条命令用的就是 ctp-reject 那四条用例。
//
// ⚠️ 两套输入会让「换个时段跑同一笔」这个判据落空：
// 优先级是拿「同一笔输入在两种时段下的回答」比出来的，输入一旦分家就不是同一个实验了。
func TestPriorityUsesTheSameInputsAsReject(t *testing.T) {
	var closeYd int
	for _, rc := range rejectCases {
		if rc.Off == def.THOST_FTDC_OF_CloseYesterday {
			closeYd++
		}
	}
	if closeYd == 0 {
		t.Error("⚠️ 四条用例里没有平昨那一条 —— 「交易时段 × 可平量」这一对就构造不出来了")
	}
	if len(rejectCases) < 4 {
		t.Errorf("⚠️ 用例只有 %d 条 —— 这条命令能量到的对数随之减少", len(rejectCases))
	}
}
