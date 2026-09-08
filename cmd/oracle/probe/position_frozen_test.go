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

func sig(cands []closable) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.side+"/"+string(c.offset)+"/"+string(c.orderDir()))
	}
	return out
}

// TestClosableCandidates 穷举「平哪一边、按什么顺序试」。
//
// ⚠️ 四条性质各自有代价：
//
//	今仓给两个候选  只给 CLOSETODAY 会在 NoUseHistory 合约上必然拒单，
//	                而那正是唯一有今仓的那个合约 —— 目标字段永远够不着
//	零观测优先      排错了就先去补一份已经有的样本
//	方向相反        搞反会在双向持仓上开出反向仓位，柜台**不报错**
//	读不到≠零       在未知前提上跑出来的结论与真结论长得一模一样
func TestClosableCandidates(t *testing.T) {
	cases := []struct {
		name    string
		p       map[string]any
		wantErr bool
		want    []string // 期望的**完整顺序**
	}{
		{
			// 大商所今晚的形状：只有今仓。CLOSETODAY 很可能被拒，
			// 所以 CLOSE 必须作为下一手跟在后面 —— 否则这一边测不到。
			name: "只有多头今仓 → 平今在前，裸平兜底",
			p:    pos("volume_long_today", 3.0, "volume_long_his", 0.0, "volume_short_today", 0.0, "volume_short_his", 0.0),
			want: []string{"long/CLOSETODAY/SELL", "long/CLOSE/SELL"},
		},
		{
			// 上期所今晚的形状：结算后全成了昨仓。
			name: "只有多头昨仓 → 只有裸平一个候选",
			p:    pos("volume_long_today", 0.0, "volume_long_his", 3.0, "volume_short_today", 0.0, "volume_short_his", 0.0),
			want: []string{"long/CLOSE/SELL"},
		},
		{
			name: "多头今昨都有 → 两个候选，不重复发裸平",
			p:    pos("volume_long_today", 2.0, "volume_long_his", 3.0, "volume_short_today", 0.0, "volume_short_his", 0.0),
			want: []string{"long/CLOSETODAY/SELL", "long/CLOSE/SELL"},
		},
		{
			// ⚠️ 20260909 之后空头的**今仓**那两个字段已经有观测了
			// （volume_short_frozen 与 _today），所以这一组**没有**零观测可打 ——
			// 顺序退回自然序。这条用例在那晚之前期望的是空头排前面，
			// 证据一变，期望就该跟着变。
			name: "多头只有昨仓、空头有今仓 → 没有零观测可打，自然序",
			p:    pos("volume_long_today", 0.0, "volume_long_his", 5.0, "volume_short_today", 1.0, "volume_short_his", 0.0),
			want: []string{"long/CLOSE/SELL", "short/CLOSETODAY/BUY", "short/CLOSE/BUY"},
		},
		{
			// ⚠️ 这一条现在是**唯一**还能考验排序的用例：
			// volume_short_frozen_his 是仅剩的零观测字段，而它只有
			// 「空头有昨仓」时才够得着。空头昨仓要过夜，今晚造不出来。
			name: "两边都只有昨仓 → 空头（仅剩的零观测）在前",
			p:    pos("volume_long_today", 0.0, "volume_long_his", 2.0, "volume_short_today", 0.0, "volume_short_his", 2.0),
			want: []string{"short/CLOSE/BUY", "long/CLOSE/SELL"},
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
		got, err := closableCandidates(c.p)
		if c.wantErr {
			errN++
			if err == nil {
				t.Errorf("%s：本该报错，却给出 %v —— "+
					"一个在错误前提下跑出来的结论，与真结论长得一模一样",
					c.name, sig(got))
			}
			continue
		}
		okN++
		if err != nil {
			t.Errorf("%s：%v", c.name, err)
			continue
		}
		g := sig(got)
		if strings.Join(g, ",") != strings.Join(c.want, ",") {
			t.Errorf("%s：\n  得到 %v\n  应为 %v", c.name, g, c.want)
		}
	}
	// ⚠️ 两侧都要有样本，否则这张表在测一个恒真（或恒假）的判定。
	if okN == 0 || errN == 0 {
		t.Fatalf("⚠️ 用例只覆盖一侧（成立 %d / 报错 %d）", okN, errN)
	}
}

// TestClosableTargetIsHonestAboutCloseOrder 断言 CLOSE 在今昨都有时
// **不谎称**自己打中哪个字段。
//
// ⚠️ 消耗顺序是 close-order 实验的问题，本实验没有答案。
// 在日志里写死一个「想打中 _today」，会让读日志的人以为这一点已经定了。
func TestClosableTargetIsHonestAboutCloseOrder(t *testing.T) {
	both := closable{kq.Buy, "long", kq.Close, 2, 3}
	if got := both.target(); !strings.Contains(got, "由柜台定") {
		t.Errorf("⚠️ 今昨都有时裸平的目标写成了 %q —— "+
			"消耗顺序本实验没有答案，写死会让人以为它定了", got)
	}
	// 而只有一边时是确定的，必须说准。
	for _, c := range []struct {
		c    closable
		want string
	}{
		{closable{kq.Buy, "long", kq.Close, 0, 3}, "volume_long_frozen_his"},
		{closable{kq.Buy, "long", kq.Close, 3, 0}, "volume_long_frozen_today"},
		{closable{kq.Sell, "short", kq.CloseToday, 3, 9}, "volume_short_frozen_today"},
	} {
		if got := c.c.target(); got != c.want {
			t.Errorf("target()=%q，应为 %q", got, c.want)
		}
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

// TestZeroObservedIsNotEverything 断言「零观测」表**不是全集**。
//
// ⚠️ 六个字段全列进去的话，排序退化成恒等 —— 那时
// TestClosableCandidates 里那几条关于顺序的断言全都平凡成立，
// 而它们看起来仍然在测顺序。
//
// ⚠️ 这张表会过期：观测到一个就该挪走一个。挪空了本条会红，那是对的 ——
// 到那时这个实验的目的已经达成，排序规则该重写而不是留着空转。
func TestZeroObservedIsNotEverything(t *testing.T) {
	if len(zeroObserved) == 0 {
		t.Fatal("⚠️ 零观测表空了 —— 六个冻结字段都取到过非零值的话，" +
			"本实验的排序规则已经没有意义，该重写而不是留着空转")
	}
	if len(zeroObserved) >= len(frozenFields) {
		t.Fatalf("⚠️ 零观测表有 %d 项，冻结字段共 %d 个 —— "+
			"全列进去会让排序退化成恒等，而关于顺序的断言全都平凡成立",
			len(zeroObserved), len(frozenFields))
	}
	known := map[string]bool{}
	for _, k := range frozenFields {
		known[k] = true
	}
	for k := range zeroObserved {
		if !known[k] {
			t.Errorf("⚠️ 零观测表里的 %q 不是冻结字段之一 —— "+
				"拼错的键谁都打不中，而排序会静默退化", k)
		}
	}
}

// TestPositionFrozenUsesTheGuards 断言几个判定**真的在实验路径上**。
//
// ⚠️ 方法论第 28 条：把判定抽成纯函数、穷举它，却忘了在生产路径上调用 ——
// 那时穷举测的是一段死代码。
func TestPositionFrozenUsesTheGuards(t *testing.T) {
	body := funcBody(t, "exp_position_frozen.go", "expPositionFrozen")
	for _, want := range []struct{ name, why string }{
		{"closableCandidates", "前提判定抽出来了却没接回去，穷举测试测的是一段死代码"},
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

// TestPositionFrozenDumpsWhileHeld 断言**趁冻结还在**就落一份夹具。
//
// ⚠️ 第一版只在实验末尾落盘，于是夹具记的是**撤单之后**的状态 ——
// 六个字段全为零。冻结确实发生过、日志里也写着，
// 而留在仓库里的那份证据一个非零值都没有：证据扫描照样报「零观测」，
// 对拍照样两边都是 0。**证据只活在会话里等于没有证据。**
//
// 判据落在**顺序**上：dump 必须在 CancelOrder 之前。
func TestPositionFrozenDumpsWhileHeld(t *testing.T) {
	body := funcBody(t, "exp_position_frozen.go", "expPositionFrozen")
	i := strings.Index(body, "case frozenChanged:")
	if i < 0 {
		t.Fatal("⚠️ 找不到 frozenChanged 分支 —— 本条守卫落空了")
	}
	arm := body[i:]
	dump := strings.Index(arm, `r.dump("position-frozen-held"`)
	cancel := strings.Index(arm, "CancelOrder")
	switch {
	case dump < 0:
		t.Error("⚠️ 冻结发生时没有落盘 —— " +
			"撤单之后再落，夹具里六个字段全为零，这次观测等于没发生")
	case cancel < 0:
		t.Error("⚠️ 冻结分支里没有撤单 —— 委托会留在盘上")
	case dump > cancel:
		t.Error("⚠️ 落盘排在撤单**之后** —— 拍到的是释放后的稳态，" +
			"六个字段全为零，这次观测等于没发生")
	}
}

// TestPositionFrozenStopsOnFill 断言**成交了就停**，不接着发下一笔。
//
// ⚠️ 挂不上的价却成交了，说明前提已经不成立；此时接着按候选表往下发单，
// 会一笔笔把种子吃光 —— 而日志读起来仍然是「实验在正常推进」。
func TestPositionFrozenStopsOnFill(t *testing.T) {
	body := funcBody(t, "exp_position_frozen.go", "expPositionFrozen")
	i := strings.Index(body, "VolumeLeft == 0")
	if i < 0 {
		t.Fatal("⚠️ 找不到成交判定 —— 本条守卫落空了")
	}
	tail := body[i:]
	if j := strings.Index(tail, "\n\t\t\t}"); j > 0 {
		tail = tail[:j]
	}
	if !strings.Contains(tail, "return") {
		t.Error("⚠️ 委托成交那一支没有 return —— " +
			"会接着按候选表往下发单，一笔笔把种子吃光，而日志读起来仍像正常推进")
	}
}

// TestPositionFrozenLimitPriceIsFar 断言**下单用的那个价**来自 FarPrice。
//
// ⚠️ 这一条刻意不用「正文里出现 FarPrice」来判 —— 那条判据是假的：
// FarPrice 在同一个函数的日志里也出现，
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
