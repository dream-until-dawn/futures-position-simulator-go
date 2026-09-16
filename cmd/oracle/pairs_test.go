package main

import (
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/ctperr"
)

// TestPairCasesViolateExactlyTwo 钉住每一条 pairCase **恰好**声明两项。
//
// ⚠️ 声明三项（或一项）的输入拿回来的码归不了因，而它在输出里与一条好观测长得一样。
func TestPairCasesViolateExactlyTwo(t *testing.T) {
	if len(pairCases) == 0 {
		t.Fatal("⚠️ pairCases 是空的 —— 下面的循环一条也不跑")
	}
	for _, pc := range pairCases {
		if len(pc.Violates) != 2 {
			t.Errorf("⚠️ %q 声明了 %d 项（要恰好 2）：%v", pc.Name, len(pc.Violates), pc.Violates)
		}
		if pc.A == ctperr.ReasonUnknown || pc.B == ctperr.ReasonUnknown {
			t.Errorf("⚠️ %q 有一侧是零值拒因 —— 它会永远走「判不了」那一支", pc.Name)
		}
		if pc.A == pc.B {
			t.Errorf("⚠️ %q 两侧是同一个拒因 —— 那不是一对", pc.Name)
		}
		if pc.AName == "" || pc.BName == "" || pc.Why == "" {
			t.Errorf("⚠️ %q 缺名字或缺「为什么恰好违反这两项」的说明", pc.Name)
		}
	}
}

// TestPairCasesStartWithAPositiveControl 钉住第一条是正对照。
//
// ⚠️ 没有正对照的话，「后面两对都答可平量」既可能是真结论，
// 也可能是这套构造根本没违反到价格类那一项 —— 两者在输出里长得一样。
func TestPairCasesStartWithAPositiveControl(t *testing.T) {
	if !pairCases[0].Control {
		t.Fatal("⚠️ 第一条不是正对照 —— 它必须排在最前，后面几对的可信度全挂在它身上")
	}
	if pairCases[0].Predict == "" {
		t.Error("⚠️ 正对照没有写下已知答案 —— 那就没法说它「复现」了什么")
	}
	n := 0
	for _, pc := range pairCases {
		if pc.Control {
			n++
		}
	}
	if n != 1 {
		t.Errorf("⚠️ 有 %d 条正对照 —— 恰好一条，多了会让 controlHeld 被后一条覆盖", n)
	}
}

// TestPairCasesArePreRegistered 钉住每一条都带**事前登记**的预言。
//
// ⚠️ 事后说「我本来就觉得会这样」不算预言。预言写死在代码里、随提交落盘，
// 于是「跑出来和预期一样」这句话有一个可核的对应物。
func TestPairCasesArePreRegistered(t *testing.T) {
	for _, pc := range pairCases {
		if pc.Predict == "" {
			t.Errorf("⚠️ %q 没有事前登记的预言", pc.Name)
			continue
		}
		// 预言必须点到两侧之一 —— 一句「不确定」也能塞满这一栏。
		if !strings.Contains(pc.Predict, pc.AName) && !strings.Contains(pc.Predict, pc.BName) {
			t.Errorf("⚠️ %q 的预言既没点名 %q 也没点名 %q：%s", pc.Name, pc.AName, pc.BName, pc.Predict)
		}
	}
}

// TestPairCasesCannotTrade 钉住每一条都**成不了交**。
//
// ⚠️ 这批探针的安全前提不是运气：平昨那两条账上无仓（平不掉任何东西），
// 而唯一一条开仓单是**买在跌停之下**。
func TestPairCasesCannotTrade(t *testing.T) {
	for _, pc := range pairCases {
		switch {
		case pc.Off == def.THOST_FTDC_OF_CloseYesterday:
			// 无仓平昨：成不了交，因为没有仓可平。
		case pc.Dir == def.THOST_FTDC_D_Buy:
			// 买单：价格必须在跌停**之下**（跌停价本身可能被封板成交）。
			md := &def.CThostFtdcDepthMarketDataField{LowerLimitPrice: 100, UpperLimitPrice: 200}
			if px := pc.Price(md, 2); px >= 100 {
				t.Errorf("⚠️ %q 的买单挂在 %.2f，不在跌停 100 之下 —— 放过了可能成交", pc.Name, px)
			}
		default:
			t.Errorf("⚠️ %q 是卖出开仓，本测试没有为它写安全判据 —— 先想清楚它为什么成不了交", pc.Name)
		}
	}
}
