package conformance

import (
	"strings"
	"testing"
)

// TestRegistriesAreWellFormed 断言两张登记表本身够格。
//
// ⚠️ 这一条不验任何对拍结果，它验的是**登记表这个机制**没有退化：
// 一个可以随口填的归类档，比没有这一档更坏 —— 它会把真实的不一致
// 洗成「已知」，而洗完之后再也没有东西会把它顶出来。
func TestRegistriesAreWellFormed(t *testing.T) {
	for name, r := range map[string]Registry{"CrossDay": CrossDay(), "Live": Live()} {
		if err := r.Validate(); err != nil {
			t.Errorf("⚠️ %s 登记表不合格：%v", name, err)
		}
		if len(r) < 10 {
			t.Errorf("⚠️ %s 只有 %d 条 —— 太少，疑似大面积丢失", name, len(r))
		}
	}
	// ⚠️ Live 必须**真的**比 CrossDay 多：它多出来的那几条
	// （成本按均价冲减、_yd 口径）只在同一天内部分平仓时才出现，
	// 跨日对拍碰不到。两张表相同的话，分成两张就没有意义。
	cd, lv := CrossDay(), Live()
	if len(lv) <= len(cd) {
		t.Errorf("⚠️ Live（%d 条）没有比 CrossDay（%d 条）多 —— "+
			"分成两张表就没有意义了", len(lv), len(cd))
	}
	// 而 Live 必须**包含** CrossDay 的每一条：跨日的差异在实时上照样成立。
	for name, k := range cd {
		got, ok := lv[name]
		if !ok {
			t.Errorf("⚠️ Live 里缺了 CrossDay 的 %s —— "+
				"跨日成立的差异在实时上照样成立", name)
			continue
		}
		// ⚠️ Live 可以**升级**一条（把「样本区分不了」变成有因果的），
		// 但不许**降级**：那等于实时那条路悄悄放宽了判据。
		if strings.HasSuffix(got.Class, "-?") && !strings.HasSuffix(k.Class, "-?") {
			t.Errorf("⚠️ %s 在 CrossDay 里是 %q，到 Live 里降成了 %q —— "+
				"实时那条路不许比离线宽", name, k.Class, got.Class)
		}
	}
}

// TestSplitSeparatesNovel 断言**第三类**真的会被单独拎出来。
//
// ⚠️ 这是登记表存在的全部理由：把新出现的、还没有解释的不一致
// 从一堆已知里捞出来。混在一个「失败 N 个」里，新东西会被淹掉。
func TestSplitSeparatesNovel(t *testing.T) {
	r := Registry{
		"a": {"测-A", "机制说明", "kq_facts 1"},
		"b": {"测-?", "样本区分不了", ""},
	}
	known, novel := r.Split([]string{"a", "b", "zzz"})
	if len(novel) != 1 || novel[0] != "zzz" {
		t.Errorf("⚠️ 第三类应当只有 zzz，得到 %v —— "+
			"没登记的字段必须落进第三类，否则新问题会被当成已知", novel)
	}
	if len(known["测-A"]) != 1 || len(known["测-?"]) != 1 {
		t.Errorf("已登记的没分好类：%v", known)
	}
	// ⚠️ 空输入时两边都空，而那不该被读成「一切正常」——
	// 调用方要自己卡「比过的字段数」，本函数不替它兜。
	k2, n2 := r.Split(nil)
	if len(k2) != 0 || len(n2) != 0 {
		t.Errorf("空输入应当两边都空，得到 %v / %v", k2, n2)
	}
}

// TestValidateRejectsSloppyEntries 穷举 Validate 该拦下的四种。
//
// ⚠️ 每一种都栽过或差点栽过，逐条列出来是为了让「为什么要拦」留在代码里。
func TestValidateRejectsSloppyEntries(t *testing.T) {
	cases := []struct {
		name string
		e    KnownClass
		want string
	}{
		{"没有前缀", KnownClass{"A", "说明", "出处"}, "没有前缀"},
		{"没有机制说明", KnownClass{"测-A", "  ", "出处"}, "没有机制说明"},
		{"因果类别没有出处", KnownClass{"测-A", "说明", ""}, "没有实测出处"},
		{"分不开却带着出处", KnownClass{"测-?", "说明", "出处"}, "自相矛盾"},
	}
	for _, c := range cases {
		err := Registry{"f": c.e}.Validate()
		if err == nil {
			t.Errorf("⚠️ %s：本该被拦下", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：报错了但没说到点上（找 %q）：%v", c.name, c.want, err)
		}
	}
	// 合格的那一种必须通过 —— 否则上面四条只是「什么都拦」。
	ok := Registry{
		"good":  {"测-A", "机制说明", "kq_facts 1"},
		"unres": {"测-?", "样本区分不了", ""},
	}
	if err := ok.Validate(); err != nil {
		t.Errorf("⚠️ 合格的表被拦下了：%v —— 那说明上面四条只是「什么都拦」", err)
	}
}
