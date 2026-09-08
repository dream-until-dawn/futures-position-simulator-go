package probe

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/cmd/oracle/kq"
)

func pos(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// TestClosableSide 穷举「挑哪一边来平」的判定。
//
// ⚠️ 三条性质各自有代价：
//
//	今优先      挑错了就去补一份已经有的样本，本次实验白跑
//	方向相反    搞反会在双向持仓上开出反向仓位，柜台**不报错**
//	读不到≠零   在未知前提上跑出来的结论与真结论长得一模一样
func TestClosableSide(t *testing.T) {
	cases := []struct {
		name    string
		p       map[string]any
		wantErr bool
		side    string
		offset  kq.Offset
		orderD  kq.Direction
	}{
		{
			name:   "只有多头今仓 → 平今",
			p:      pos("volume_long_today", 3.0, "volume_long_his", 0.0, "volume_short_today", 0.0, "volume_short_his", 0.0),
			side:   "long",
			offset: kq.CloseToday,
			orderD: kq.Sell,
		},
		{
			name:   "只有多头昨仓 → 平昨",
			p:      pos("volume_long_today", 0.0, "volume_long_his", 3.0, "volume_short_today", 0.0, "volume_short_his", 0.0),
			side:   "long",
			offset: kq.Close,
			orderD: kq.Sell,
		},
		{
			name:   "多头今昨都有 → 今优先",
			p:      pos("volume_long_today", 2.0, "volume_long_his", 3.0, "volume_short_today", 0.0, "volume_short_his", 0.0),
			side:   "long",
			offset: kq.CloseToday,
			orderD: kq.Sell,
		},
		{
			name:   "只有空头昨仓 → 平昨，下单方向为买",
			p:      pos("volume_long_today", 0.0, "volume_long_his", 0.0, "volume_short_today", 0.0, "volume_short_his", 2.0),
			side:   "short",
			offset: kq.Close,
			orderD: kq.Buy,
		},
		{
			// ⚠️ 这一条是「今优先」真正的考验：今仓在**另一边**。
			// 只在同一边比今昨的实现会在这里选中多头昨仓，而那份样本已经有了。
			name:   "多头只有昨仓、空头有今仓 → 跨边也今优先",
			p:      pos("volume_long_today", 0.0, "volume_long_his", 5.0, "volume_short_today", 1.0, "volume_short_his", 0.0),
			side:   "short",
			offset: kq.CloseToday,
			orderD: kq.Buy,
		},
		{
			name:    "两边全空 → 报错",
			p:       pos("volume_long_today", 0.0, "volume_long_his", 0.0, "volume_short_today", 0.0, "volume_short_his", 0.0),
			wantErr: true,
		},
		{
			// ⚠️ 字段缺席不当成零：那会让整个实验在一个未知的前提上跑。
			name:    "字段缺席 → 报错，不当成零",
			p:       pos("volume_long_today", 3.0), // 缺 _his 与整个空头
			wantErr: true,
		},
		{
			name:    "空截面 → 报错",
			p:       pos(),
			wantErr: true,
		},
	}
	okN, errN := 0, 0
	for _, c := range cases {
		got, err := closableSide(c.p)
		if c.wantErr {
			errN++
			if err == nil {
				t.Errorf("%s：本该报错，却选了 %s/%s —— "+
					"一个在错误前提下跑出来的结论，与真结论长得一模一样",
					c.name, got.side, got.offset)
			}
			continue
		}
		okN++
		if err != nil {
			t.Errorf("%s：%v", c.name, err)
			continue
		}
		if got.side != c.side {
			t.Errorf("%s：选了 %s，应为 %s", c.name, got.side, c.side)
		}
		if got.offset != c.offset {
			t.Errorf("%s：offset 为 %s，应为 %s —— "+
				"⚠️ 挑错了就去补一份已经有的样本，本次实验白跑",
				c.name, got.offset, c.offset)
		}
		if d := got.orderDir(); d != c.orderD {
			t.Errorf("%s：下单方向 %s，应为 %s —— "+
				"⚠️ 方向搞反会在双向持仓上开出反向仓位，且柜台不报错",
				c.name, d, c.orderD)
		}
	}
	// ⚠️ 两侧都要有样本，否则这张表在测一个恒真（或恒假）的判定。
	if okN == 0 || errN == 0 {
		t.Fatalf("⚠️ 用例只覆盖一侧（成立 %d / 报错 %d）", okN, errN)
	}
}

// TestFmtFrozenKeepsNonZero 断言渲染**不会把非零的那个藏起来**。
//
// ⚠️ 折叠零值是为了让动了的那个显眼；如果折叠逻辑写错，
// 结果是一份「六个字段全为零」的日志 —— 而那正好是本实验最想否定的结论。
func TestFmtFrozenKeepsNonZero(t *testing.T) {
	all0 := map[string]string{}
	for _, k := range frozenFields {
		all0[k] = "0.0000"
	}
	if got := fmtFrozen(all0); !strings.Contains(got, "全为零") {
		t.Errorf("全零应当明说，得到 %q", got)
	}
	for _, k := range frozenFields {
		m := map[string]string{}
		for _, kk := range frozenFields {
			m[kk] = "0.0000"
		}
		m[k] = "1.0000"
		got := fmtFrozen(m)
		if !strings.Contains(got, k+"=1.0000") {
			t.Errorf("⚠️ %s 非零却没被打出来：%q —— "+
				"那会让一次真的冻结被记成「全为零」", k, got)
		}
		if strings.Contains(got, "全为零") {
			t.Errorf("⚠️ %s 非零，却仍报「全为零」：%q", k, got)
		}
	}
	// 「无值」与「缺字段」都不是零，必须照实打出来。
	for _, v := range []string{"-", "(缺字段)", "(无截面)"} {
		m := map[string]string{}
		for _, kk := range frozenFields {
			m[kk] = "0.0000"
		}
		m["volume_long_frozen_today"] = v
		if got := fmtFrozen(m); !strings.Contains(got, v) {
			t.Errorf("⚠️ %q 被吞掉了：%q —— 「无值」不是「零」", v, got)
		}
	}
}

// TestPositionFrozenUsesTheGuards 断言几个判定**真的在实验路径上**。
//
// ⚠️ 方法论第 28 条：把判定抽成纯函数、穷举它，却忘了在生产路径上调用 ——
// 那时穷举测的是一段死代码。这里一次查三个。
func TestPositionFrozenUsesTheGuards(t *testing.T) {
	body := funcBody(t, "exp_position_frozen.go", "expPositionFrozen")
	for _, want := range []struct{ name, why string }{
		{"closableSide", "前提判定抽出来了却没接回去，穷举测试测的是一段死代码"},
		{"orderDir", "⚠️ 方向搞反会在双向持仓上开出反向仓位，且柜台不报错"},
		{"waitFrozen", "⚠️ 睡固定秒数会在慢的那一次把「还没冻」记成「不冻结」"},
	} {
		if !strings.Contains(body, want.name+"(") {
			t.Errorf("⚠️ expPositionFrozen 里没有调用 %s —— %s", want.name, want.why)
		}
	}
	// ⚠️ 反向：不许出现写死的方向常量。写死会绕过 orderDir 那条性质，
	// 而上面那条 Contains 检查在「两者都在」时照样通过。
	for _, bad := range []string{"kq.Buy", "kq.Sell"} {
		if strings.Contains(body, bad) {
			t.Errorf("⚠️ expPositionFrozen 里出现写死的 %s —— "+
				"下单方向必须由 orderDir 从持仓方向推出", bad)
		}
	}
}

// TestPositionFrozenLimitPriceIsFar 断言**下单用的那个价**来自 FarPrice。
//
// ⚠️ 这一条刻意不用「正文里出现 FarPrice」来判 —— 那条判据是假的：
// FarPrice 在同一个函数的日志里也出现一次，
// 于是把 LimitPrice 换成写死的数之后，文本检查照样通过。
// 判据必须落在**那个字段**上。
func TestPositionFrozenLimitPriceIsFar(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "exp_position_frozen.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	ast.Inspect(f, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "OrderReq" {
			return true
		}
		for _, e := range cl.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "LimitPrice" {
				continue
			}
			found++
			call, ok := kv.Value.(*ast.CallExpr)
			if !ok {
				t.Errorf("⚠️ LimitPrice 不是个调用 —— "+
					"写死的价会当场成交并**吃掉种子**，得到 %T", kv.Value)
				continue
			}
			s, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || s.Sel.Name != "FarPrice" {
				t.Error("⚠️ LimitPrice 不是 FarPrice 算出来的 —— " +
					"被接受的那一支会当场成交并**吃掉种子**")
			}
		}
		return true
	})
	if found != 1 {
		t.Fatalf("⚠️ 在 exp_position_frozen.go 里找到 %d 处 OrderReq.LimitPrice，"+
			"应为 1 —— 本条守卫的判据落空了（0 处时它全绿）", found)
	}
}

// funcBody 返回某个函数在源码里的正文文本。
func funcBody(t *testing.T, file, fn string) string {
	t.Helper()
	src, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	var out string
	ast.Inspect(src, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if !ok || fd.Name.Name != fn || fd.Body == nil {
			return true
		}
		out = raw[fd.Body.Pos()-1 : fd.Body.End()-1]
		return false
	})
	if out == "" {
		t.Fatalf("⚠️ 在 %s 里找不到函数 %s —— 本条守卫在空串上跑（那会全绿）", file, fn)
	}
	return out
}
