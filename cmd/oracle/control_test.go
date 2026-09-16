package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/ctp"
)

// TestControlVerdict 钉住对照组四种结局的处置。
//
// ⚠️ 判别力在「被拒」那一档：那正是 20260916 真的发生过的事
// （CZCE.MA701 / GFEX.si2701 当前状态禁止报单，四条用例全拿到 26），
// 而在对照组之前，那一轮**看起来完全成功**。
func TestControlVerdict(t *testing.T) {
	resting := ctp.OrderState{Status: def.THOST_FTDC_OST_NoTradeQueueing}
	for _, c := range []struct {
		name            string
		st              ctp.OrderState
		insErr          error
		wantProceed     bool
		wantCancel      bool
		wantErrContains string
	}{
		{"挂上了 ⇒ 继续并撤单", resting, nil, true, true, ""},
		{"被拒了 ⇒ 整轮不跑", ctp.OrderState{
			Status: def.THOST_FTDC_OST_Canceled, StatusMsg: "26:x"},
			nil, false, false, "整轮不跑"},
		{"成交了 ⇒ 整轮不跑且有敞口", ctp.OrderState{VolumeTraded: 1}, nil, false, false, "账上有敞口"},
		{"发不出去 ⇒ 整轮不跑", ctp.OrderState{}, errors.New("boom"), false, false, "连它的结局都不知道"},
	} {
		proceed, cancel, err := controlVerdict("CZCE.MA701", c.st, c.insErr)
		if proceed != c.wantProceed || cancel != c.wantCancel {
			t.Errorf("⚠️ %s：proceed=%v cancel=%v，应为 %v / %v",
				c.name, proceed, cancel, c.wantProceed, c.wantCancel)
		}
		if c.wantErrContains == "" {
			if err != nil {
				t.Errorf("⚠️ %s 不该报错：%v", c.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("⚠️ %s 应当报错并停下 —— 不停下的话，后面四条拿到的码会以机器可读的形式进语料", c.name)
		} else if !strings.Contains(err.Error(), c.wantErrContains) {
			t.Errorf("⚠️ %s 的理由没说到点上（要含 %q）：%v", c.name, c.wantErrContains, err)
		}
	}
}

// TestControlVerdictNeverProceedsOnAnythingButResting 是上表的反面守卫。
//
// ⚠️ 一个「除了发不出去以外一律继续」的实现，会让上表里三行照样通过判定值之外的部分；
// 这里直接钉住**只有挂上了才继续**。
func TestControlVerdictNeverProceedsOnAnythingButResting(t *testing.T) {
	for _, st := range []ctp.OrderState{
		{Status: def.THOST_FTDC_OST_Canceled},
		{Status: def.THOST_FTDC_OST_AllTraded, VolumeTraded: 1},
		{},
	} {
		if proceed, _, _ := controlVerdict("CZCE.MA701", st, nil); proceed {
			t.Errorf("⚠️ status=%q 已成交=%d 也继续了 —— 只有「挂上了」才说明这个合约报得进单",
				string(rune(st.Status)), st.VolumeTraded)
		}
	}
	// 反向：挂着的那一档必须继续，否则「一律不跑」也能让上面全绿。
	resting := ctp.OrderState{Status: def.THOST_FTDC_OST_NoTradeQueueing}
	if proceed, cancel, err := controlVerdict("CZCE.MA701", resting, nil); !proceed || !cancel || err != nil {
		t.Errorf("⚠️ 挂上了却不继续（proceed=%v cancel=%v err=%v）—— 那样这条命令永远跑不了",
			proceed, cancel, err)
	}
}

// TestCodeTextOnlyGivesTheCode 钉住错误消息里**只放码、不放原话**。
//
// ⚠️ StatusMsg 是柜台自由文本，纪律是「可以进本地日志，不进入库的夹具」。
// 而一条把原话拼进去的错误消息，很容易被人整段抄进语料或文档。
func TestCodeTextOnlyGivesTheCode(t *testing.T) {
	got := codeText("26:已撤单，被拒绝CZCE:当前状态禁止报单")
	if got != "26" {
		t.Errorf("⚠️ 只该给码，得到 %q", got)
	}
	if codeText("没有前缀码的一段自由文本") != "无" {
		t.Errorf("⚠️ 没有前缀码时要说「无」—— 与「码是 0」必须分得开")
	}
}

// TestControlRunsBeforeAnyCase 是对照组的**接线测试**：它必须排在四条用例之前。
//
// ⚠️ 判定全绿、忘了接线的表现是「一切正常」—— 命令照样跑完四条、照样拿到四个码，
// 而其中一些的归因是错的（20260916 那次：10:15–10:30 盘中休息里五个交易所全给 26）。
// ⚠️ 排在**之后**同样没用：那时错误归因已经写进语料了。
//
// 形状与 TestSlicePreconditionCheckedBeforeAnyOrder 同源（AST 位置比较），
// 而它守的是**位置**不是行为 —— 行为那一侧由 TestControlVerdict 那几条守。
func TestControlRunsBeforeAnyCase(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "reject.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "runCTPReject" {
			fn = fd
		}
	}
	if fn == nil {
		t.Fatal("⚠️ 找不到 runCTPReject —— 它改名了，本条守卫失效")
	}
	ctrlPos, loopPos := token.NoPos, token.NoPos
	ast.Inspect(fn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if id, ok := v.Fun.(*ast.Ident); ok && id.Name == "runControl" && ctrlPos == token.NoPos {
				ctrlPos = v.Pos()
			}
		case *ast.RangeStmt:
			// 四条用例的那个循环：range rejectCases
			if id, ok := v.X.(*ast.Ident); ok && id.Name == "rejectCases" && loopPos == token.NoPos {
				loopPos = v.Pos()
			}
		}
		return true
	})
	if ctrlPos == token.NoPos {
		t.Fatal("⚠️ runCTPReject 里找不到 runControl 的调用 —— " +
			"要么对照组被删了，要么改名了而本条守卫在空转")
	}
	if loopPos == token.NoPos {
		t.Fatal("⚠️ 找不到 `range rejectCases` 那个循环 —— 本条守卫在空转")
	}
	if ctrlPos > loopPos {
		t.Errorf("⚠️ 对照组排在四条用例**之后**（%s vs %s）—— "+
			"那时错误归因已经写进语料了，晚一步等于没有",
			fset.Position(ctrlPos), fset.Position(loopPos))
	}
}
