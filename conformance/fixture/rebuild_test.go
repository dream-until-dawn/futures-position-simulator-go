package fixture

import (
	"fmt"
	"sort"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/conformance"
	"github.com/dream-until-dawn/futures-position-simulator-go/view"
	"github.com/shopspring/decimal"
)

// specsFor 组装重建一份夹具所需的规则数据。
//
// ⚠️ 三处数据三个出处，都记着：
//
//	乘数        probes.md §7.2 实测（从每手保证金反解，跨合约互验）
//	手续费率    probes.md §10.2 的分类 + 标定
//	保证金率    probes.md §7.2 实测，至少三档
//
// 而 MaxMarginSide **写死为 false**：实测本口子未启用（kq_facts 5），
// 也就是说它在这里**测不出来**。这不是「本库认为不启用」，
// 是「这个口子上没有能把两者分开的信号」。
func specsFor(t *testing.T, f *Fixture) map[string]Spec {
	t.Helper()
	out := map[string]Spec{}
	for _, sym := range f.Symbols() {
		if len(f.TradesOf(sym)) == 0 {
			continue
		}
		multStr, ok := multipliers[sym]
		if !ok {
			t.Fatalf("%s 没有登记乘数", sym)
		}
		product, _ := splitProduct(f.TradesOf(sym)[0].Instrument.Product)
		comm, _, ok := ratesOf(product)
		if !ok {
			t.Fatalf("品种 %s 没有登记手续费率", product)
		}
		mg, ok := ratesFor(product)
		if !ok {
			t.Fatalf("品种 %s 没有登记保证金率", product)
		}
		out[sym] = Spec{
			Multiplier: decimal.RequireFromString(multStr),
			Commission: comm, Margin: mg,
			MaxMarginSide: false, // 实测本口子未启用，kq_facts 5
		}
	}
	return out
}

// TestRebuildAccountFieldByField 从 pre_balance 重建整个账户，逐字段与柜台比。
//
// ⚠️ 哪些字段有判别力，必须分清 —— 否则「21 个字段全对」听起来比实际强得多：
//
//	同义反复  pre_balance / static_balance / deposit / withdraw
//	          它们是输入或由输入平凡推出，比它们等于比自己抄的数
//	有判别力  commission / close_profit / position_profit / margin
//	真正验收  balance / available / risk_ratio —— 由上面那几个再推一层
func TestRebuildAccountFieldByField(t *testing.T) {
	all := loadAll(t)
	var target *Fixture
	for _, f := range all {
		if f.Path == "status-20260908-7.json" {
			target = f
		}
	}
	if target == nil {
		t.Skip("找不到带行情的夹具")
	}

	snap, err := Rebuild(target, specsFor(t, target))
	if err != nil {
		t.Fatalf("重建失败：%v", err)
	}
	lib, err := view.AccountOf(snap, view.AccountInput{})
	if err != nil {
		t.Fatal(err)
	}

	// ⚠️ 分档记录，而不是笼统地数「对了几个」。
	tautological := map[string]bool{
		"pre_balance": true, "static_balance": true,
		"deposit": true, "withdraw": true,
	}
	// 真正的验收对象：由别的字段再推一层得出的。
	acceptance := map[string]bool{
		"balance": true, "available": true, "risk_ratio": true,
	}

	var fields []conformance.Field
	var skipped []string
	nTaut, nComputed, nAcceptance := 0, 0, 0
	for name, v := range lib {
		o, ok := target.Account[name]
		if !ok {
			t.Errorf("⚠️ 本库渲染了账户字段 %s，而柜台截面里没有它", name)
			continue
		}
		if o.IsText || v.Presence == view.NotModeled || v.Presence == view.NotImplemented {
			skipped = append(skipped, name)
			continue
		}
		f := conformance.Field{
			Name: name, Library: v.Number,
			Oracle: o.Number, OracleAbsent: o.Absent,
			// ⚠️ 同义反复的字段标成**未触发**：值确实一样，但它证明不了什么。
			// 那正是 Untriggered 这一档存在的意义。
			Triggered: !tautological[name],
		}
		if v.Presence == view.Absent {
			f.LibraryAbsent = true
		}
		fields = append(fields, f)
		switch {
		case tautological[name]:
			nTaut++
		case acceptance[name]:
			nAcceptance++
		default:
			nComputed++
		}
	}
	sort.Strings(skipped)

	// ⚠️ 金额与比值**不能共用一个容差**，而这正是 conformance 包自己的注释
	// 警告过的那种混用：「不同字段的最小有意义单位不同，
	// 一个包级默认值会在某些字段上过松而没有任何动静」。
	//
	//	金额  1e-7 —— 比一分钱的百分之一还细，够抓住任何真实差异，
	//	             又宽到能吸收柜台侧的 float64 累加尾巴（实测 1e-13）
	//	比值  1e-9 —— risk_ratio 量级是 1e-2，用金额的容差等于放宽了五个数量级
	//
	// ⚠️ 拿一个容差套两种量纲，「全对」这句话在其中一种上是虚的。
	moneyTol := decimal.RequireFromString("0.0000001")
	ratioTol := decimal.RequireFromString("0.000000001")
	isRatio := map[string]bool{"risk_ratio": true}
	var moneyFields, ratioFields []conformance.Field
	for _, f := range fields {
		if isRatio[f.Name] {
			ratioFields = append(ratioFields, f)
		} else {
			moneyFields = append(moneyFields, f)
		}
	}
	if len(ratioFields) == 0 {
		t.Fatal("⚠️ 一个比值字段都没有 —— 分容差这件事此时没被考验过")
	}
	rMoney := conformance.Classify(target.Path+"·重建的账户·金额", moneyFields, moneyTol)
	rRatio := conformance.Classify(target.Path+"·重建的账户·比值", ratioFields, ratioTol)
	r := mergeReports(rMoney, rRatio)
	t.Logf("账户字段 %d 个参与比对（同义反复 %d / 本库算的 %d / 真正验收 %d），跳过 %v",
		len(fields), nTaut, nComputed, nAcceptance, skipped)
	for _, v := range []conformance.Verdict{
		conformance.Matched, conformance.Untriggered, conformance.Failed,
	} {
		t.Logf("  %-16s %d", v.String(), r.Counts[v])
	}
	for name, verdict := range r.Verdicts {
		if verdict == conformance.Failed {
			t.Errorf("❌ %s：本库 %s，柜台 %s", name,
				lib[name].Number, target.Account[name].Number)
		}
	}
	for _, e := range r.Errs {
		t.Errorf("⚠️ %v", e)
	}

	// —— 判别力守卫 ——
	//
	// ⚠️ 三档都要有：只有同义反复时，「账户对上了」这句话是空的。
	if nAcceptance < 2 {
		t.Fatalf("⚠️ 真正的验收字段只有 %d 个 —— "+
			"balance / available / risk_ratio 是由别的字段再推一层得出的，"+
			"它们才是这条测试的对象", nAcceptance)
	}
	if nComputed < 3 {
		t.Fatalf("⚠️ 本库自己算的字段只有 %d 个 —— 判别力不足", nComputed)
	}
	// ⚠️ 关键的三个必须**真的对上了**，而不是落进未触发或被跳过。
	for _, name := range []string{"balance", "available", "risk_ratio"} {
		if got := r.Verdicts[name]; got != conformance.Matched {
			t.Errorf("⚠️ %s 判为「%v」，应为「对得上且被触发过」—— "+
				"它是整条资金链的出口，错一处上游就全错", name, got)
		}
	}
}

// TestRebuildRefusesHistoryPositions 断言有昨仓时重建会**报错**而不是给个错数。
//
// ⚠️ 今晚就会撞上：昨仓那几手今天的成交里没有一笔能解释它，
// 硬跑会得到一个手数少了几手、但看起来完全正常的账户。
func TestRebuildRefusesHistoryPositions(t *testing.T) {
	all := loadAll(t)
	var target *Fixture
	for _, f := range all {
		if f.Path == "status-20260908-7.json" {
			target = f
		}
	}
	if target == nil {
		t.Skip("找不到带行情的夹具")
	}
	// 合成一个「有昨仓」的截面：直接改一份读进来的夹具。
	sym := "SHFE.rb2701"
	orig := target.Positions[sym]["volume_long_his"]
	target.Positions[sym]["volume_long_his"] = Value{Number: decimal.NewFromInt(2)}
	defer func() { target.Positions[sym]["volume_long_his"] = orig }()

	if _, err := Rebuild(target, specsFor(t, target)); err == nil {
		t.Fatal("⚠️ 有昨仓却照样重建 —— " +
			"会得到一个手数少了几手、但看起来完全正常的账户")
	}
}

// TestRebuildRefusesMissingSpec 断言缺规格时报错而不是填默认值。
func TestRebuildRefusesMissingSpec(t *testing.T) {
	all := loadAll(t)
	var target *Fixture
	for _, f := range all {
		if f.Path == "status-20260908-7.json" {
			target = f
		}
	}
	if target == nil {
		t.Skip("找不到带行情的夹具")
	}
	specs := specsFor(t, target)
	for sym := range specs {
		delete(specs, sym)
		break
	}
	if _, err := Rebuild(target, specs); err == nil {
		t.Error("⚠️ 缺一个合约的规格却照样重建 —— " +
			"一个「差不多能用」的规格会在平今平昨和大边上静默算错")
	}
}

// mergeReports 把两份按不同容差判出来的报告合起来看。
//
// ⚠️ 合并只是为了汇报，判定已经各按各的容差做完了。
// 若反过来先合并再判，就又回到「一个容差套两种量纲」。
func mergeReports(rs ...*conformance.Report) *conformance.Report {
	out := &conformance.Report{
		Sample:   "合并",
		Verdicts: map[string]conformance.Verdict{},
		Counts:   map[conformance.Verdict]int{},
	}
	for _, r := range rs {
		out.Fields = append(out.Fields, r.Fields...)
		out.Errs = append(out.Errs, r.Errs...)
		for k, v := range r.Verdicts {
			if _, dup := out.Verdicts[k]; dup {
				out.Errs = append(out.Errs,
					fmt.Errorf("字段 %s 在两份报告里都出现了 —— 分组分错了", k))
			}
			out.Verdicts[k] = v
		}
		for v, n := range r.Counts {
			out.Counts[v] += n
		}
	}
	return out
}
