package fixture

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestFrozenAccountAgainstOracle 把**账户侧**的冻结与柜台比。
//
// 本库从挂着的委托算 `order.Frozen`，与柜台账户截面上的
// `frozen_margin` / `frozen_commission` 逐份比。
//
// # ⚠️ 手续费必须本库自己算
//
// 柜台的委托记录里带着 `frozen_commission`，拿它加起来再与账户的合计比，
// 验的是**柜台自己内部一致**，与本库无关。所以这里走 `fee.Compute`，
// 基准是昨结算价（kq_facts 4）—— 两侧一侧是本库、一侧是柜台。
//
// # 20260909 的两条新观测
//
//	平仓挂单的 frozen_margin      **0** —— 平仓不冻保证金
//	平仓挂单的 frozen_commission  一笔的费额 —— 平仓**冻**手续费
//
// ⚠️ 前一条正好补上 `order.FreezeOf` 文档里标着「本库没有观测」的那处边界：
// 那时是**按 CTP 的模型**实现的（持仓的保证金本来就占着，平仓不额外冻），
// 现在它有实测支撑了。
func TestFrozenAccountAgainstOracle(t *testing.T) {
	all := loadAll(t)
	compared, nonZero := 0, 0
	for _, f := range all {
		if !f.HasOrders {
			continue
		}
		// ⚠️ 先把这份夹具里**委托涉及的每一个合约**的规格凑齐。
		// 凑不齐就整份跳过并说出来，不算一半 —— 算一半的合计会**偏小**，
		// 而偏小的方向是「看起来钱更多」。
		specs := map[string]Spec{}
		skipped := false
		syms, err := LiveOrderSymbols(f)
		if err != nil {
			t.Errorf("⚠️ %s：%v", f.Path, err)
			continue
		}
		for _, sym := range syms {
			mult, hasMult := multipliers[sym]
			_, hasPre := f.PreSettlement(sym)
			if !hasPre || !hasMult {
				t.Logf("ⓘ %s：%s 缺昨结算价或乘数，本份跳过", f.Path, sym)
				skipped = true
				break
			}
			inst, err := types.ParseSymbol(sym, f.TradingDay)
			if err != nil {
				t.Fatal(err)
			}
			product, _ := splitProduct(inst.Product)
			rates, _, ok := ratesOf(product)
			if !ok {
				t.Logf("ⓘ %s：品种 %s 没有登记费率，本份跳过", f.Path, product)
				skipped = true
				break
			}
			mr, ok := ratesFor(product)
			if !ok {
				t.Logf("ⓘ %s：品种 %s 没有登记保证金率，本份跳过", f.Path, product)
				skipped = true
				break
			}
			specs[sym] = Spec{Multiplier: decimal.RequireFromString(mult),
				Commission: rates, Margin: mr}
		}
		if skipped {
			continue
		}
		// ⚠️ 走 FrozenAccountOf —— 与 Rebuild 那条路**同一份实现**。
		// 这里原先自己建一个 order.Book 逐笔 Insert，于是同一件事有两份实现，
		// 而两份之间从来没有任何东西比过。
		got, err := FrozenAccountOf(f, specs)
		if err != nil {
			t.Errorf("⚠️ %s 算冻结：%v", f.Path, err)
			continue
		}
		for _, c := range []struct {
			key  string
			want decimal.Decimal
		}{
			{"frozen_margin", got.Margin},
			{"frozen_commission", got.Commission},
		} {
			counter, ok := numberOf(f.Account, c.key)
			if !ok {
				t.Errorf("⚠️ %s 的账户截面没有 %s", f.Path, c.key)
				continue
			}
			compared++
			if !c.want.IsZero() {
				nonZero++
			}
			if counter.Sub(c.want).Abs().GreaterThan(decimal.RequireFromString("0.0000001")) {
				t.Errorf("⚠️ %s 的 %s：柜台 %s，本库从委托算出 %s —— "+
					"⚠️ 账户侧冻结对不上。先查是**费额算错**、"+
					"**平仓单被当成开仓冻了保证金**，还是"+
					"**开仓单的冻结基准用成了报单价**", f.Path, c.key, counter, c.want)
			}
		}
	}
	// ⚠️ 棘轮，不是 `> 0`。少比几份夹具**不会报错**，只会让覆盖悄悄变小 ——
	// 而那正是筛法漂移（哪些委托要算冻结、哪些夹具凑得齐规格）的表现形式。
	// 20260909：30 个字段。这个数**只许涨**。
	const comparedRatchet = 30
	switch {
	case compared < comparedRatchet:
		t.Errorf("⚠️ 只比了 %d 个字段，此前是 %d —— 覆盖变小了。"+
			"先查是不是有夹具因为凑不齐规格被跳过了，"+
			"或者 LiveOrderSymbols 的筛法与 FrozenAccountOf 分了岔", compared, comparedRatchet)
	case compared > comparedRatchet:
		t.Errorf("ⓘ 比到了 %d 个字段（此前 %d）—— 好消息，"+
			"把 comparedRatchet 改成 %d 钉住它，否则退化不会红",
			compared, comparedRatchet, compared)
	}
	// ⚠️ 判别力：必须有非零。全零的一致什么都不说明。
	if nonZero == 0 {
		t.Errorf("⚠️ 比了 %d 个字段，**没有一个是非零的** —— "+
			"全零的一致什么都不说明：把整块冻结逻辑删掉，它照样一致", compared)
	}
	t.Logf("账户侧冻结对拍：比了 %d 个字段，其中本库算出非零的 %d 个", compared, nonZero)
}

func TestFrozenTotals(t *testing.T) {
	specs := map[string]Spec{"SHFE.rb2701": {
		Multiplier: dd("10"),
		Commission: mustRates(t, "rb"),
		Margin:     mustMargin(t, "rb"),
	}}
	f := &Fixture{
		HasOrders:  true,
		TradingDay: mustDay(t, "20260909"),
		Quotes: map[string]map[string]Value{
			"SHFE.rb2701": {"pre_settlement": {Number: dd("3163")}},
		},
		Orders: map[string]map[string]Value{
			"a": withLeft(ord("status", "ALIVE", "exchange_id", "SHFE",
				"instrument_id", "rb2701", "direction", "SELL", "offset", "CLOSETODAY"), "1"),
			// 已终结的不算
			"b": withLeft(ord("status", "FINISHED", "exchange_id", "SHFE",
				"instrument_id", "rb2701", "direction", "SELL", "offset", "CLOSETODAY"), "9"),
		},
	}
	fr, err := FrozenAccountOf(f, specs)
	if err != nil {
		t.Fatal(err)
	}
	// 平仓单不冻保证金（20260909 实测）。
	if !fr.Margin.IsZero() {
		t.Errorf("⚠️ 平仓挂单冻了 %s 保证金 —— 实测柜台不冻（kq_facts 42）", fr.Margin)
	}
	// 手续费 = 3163 × 10 × 0.00001 = 0.3163
	if !fr.Commission.Equal(dd("0.3163")) {
		t.Errorf("⚠️ 冻结手续费 %s，应为 0.3163 —— "+
			"已终结的那笔（9 手）是不是被算进来了？", fr.Commission)
	}

	// ⚠️ 开仓挂单**要冻保证金**，基准是昨结算价而不是报单价。
	// 这一支原先是「遇到就报错」，20260909 有了两次独立实测之后才实现
	// （见 openFrozenMargin 的注释）。
	//
	// rb2701：昨结 3163 × 乘数 10 × 7% = 2214.1
	f.Orders["c"] = withLeft(ord("status", "ALIVE", "exchange_id", "SHFE",
		"instrument_id", "rb2701", "direction", "BUY", "offset", "OPEN"), "1")
	fr2, err := FrozenAccountOf(f, specs)
	if err != nil {
		t.Fatal(err)
	}
	if !fr2.Margin.Equal(dd("2214.1")) {
		t.Errorf("⚠️ 开仓挂单冻结保证金 %s，应为 2214.1（3163×10×7%%）—— "+
			"⚠️ 先查基准是不是用成了**报单价**：那一项在这条用例上分得开，"+
			"因为报单价压根没给", fr2.Margin)
	}
	// 手续费多出开仓那一笔：0.3163 + 0.3163 = 0.6326
	if !fr2.Commission.Equal(dd("0.6326")) {
		t.Errorf("⚠️ 冻结手续费 %s，应为 0.6326（平今一笔 + 开仓一笔）", fr2.Commission)
	}
}

// mustDay 解析交易日。
func mustDay(t *testing.T, s string) types.TradingDay {
	t.Helper()
	d, err := types.ParseTradingDay(s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// mustMargin 取某品种的实测保证金率，没登记就报错。
func mustMargin(t *testing.T, product string) refdata.MarginRates {
	t.Helper()
	r, ok := ratesFor(product)
	if !ok {
		t.Fatalf("品种 %s 没有登记保证金率", product)
	}
	return r
}

// mustRates 取某品种的实测费率，没登记就报错。
func mustRates(t *testing.T, product string) refdata.CommissionRates {
	t.Helper()
	r, _, ok := ratesOf(product)
	if !ok {
		t.Fatalf("品种 %s 没有登记费率", product)
	}
	return r
}

// TestFrozenIsNotCopiedFromOracle 用**污染**证明本库不是在抄柜台。
//
// # 判据
//
// 「手续费由本库自己算，不抄柜台委托记录里的 frozen_commission」——
// 这句话此前**只写在注释里**：测试查得了两侧相等，查不了这个数**从哪来**。
// 而一次同义反复的对拍与一次真的对拍，在汇总行里长得一模一样。
//
// 污染把它变成一条机械断言：
//
//	把柜台那一侧的来源改成一个荒谬值，再算一遍
//	  值不变  ⇒ 它不是抄来的
//	  值跟着变 ⇒ 那就是循环论证
//
// ⚠️ 这条判据对**所有**「两侧可能同源」的对拍都适用，不只是冻结。
// 本条由评审方 20260909 提出。
func TestFrozenIsNotCopiedFromOracle(t *testing.T) {
	poison := dd("999999")
	checked, poisoned := 0, 0
	for _, f := range loadAll(t) {
		if !f.HasOrders {
			continue
		}
		specs, ok := specsForOrders(t, f)
		if !ok {
			continue
		}
		clean, err := FrozenAccountOf(f, specs)
		if err != nil {
			continue // 缺输入的份数由上面那条测试报，这里不重复
		}
		// 复制一份，把柜台自报的两个金额字段改成荒谬值。
		dirty := *f
		dirty.Orders = map[string]map[string]Value{}
		for id, o := range f.Orders {
			cp := map[string]Value{}
			for k, v := range o {
				if k == "frozen_commission" || k == "frozen_margin" {
					poisoned++
					v = Value{Number: poison}
				}
				cp[k] = v
			}
			dirty.Orders[id] = cp
		}
		got, err := FrozenAccountOf(&dirty, specs)
		if err != nil {
			t.Errorf("⚠️ %s 污染之后算不出来了：%v —— "+
				"那说明本库**读了**柜台自报的那两个字段", f.Path, err)
			continue
		}
		checked++
		if !got.Margin.Equal(clean.Margin) || !got.Commission.Equal(clean.Commission) {
			t.Errorf("⚠️ %s 污染柜台自报的 frozen_* 之后，本库算出来的变了："+
				"保证金 %s→%s、手续费 %s→%s —— **那就是循环论证**："+
				"两侧本来就是同一个数，对拍等于拿一个数和它自己比",
				f.Path, clean.Margin, got.Margin, clean.Commission, got.Commission)
		}
	}
	t.Logf("污染对照：%d 份夹具，改掉 %d 处柜台自报的金额", checked, poisoned)
	// ⚠️ 判别力两条，缺一条这个测试就是空转：
	//   没有夹具可算   → 什么都没证
	//   一处都没污染到 → 「改了也不变」是因为根本没改
	if checked == 0 {
		t.Skip("还没有算得出冻结的夹具 —— 污染对照待样本")
	}
	if poisoned == 0 {
		t.Error("⚠️ 一处 frozen_* 都没污染到 —— " +
			"「改了也不变」不成立，因为根本没改到东西。" +
			"先确认委托记录里真的带着那两个字段（kq_facts 49）")
	}
}

// specsForOrders 凑齐一份夹具里挂单涉及合约的规格；凑不齐就报 false。
func specsForOrders(t *testing.T, f *Fixture) (map[string]Spec, bool) {
	t.Helper()
	syms, err := LiveOrderSymbols(f)
	if err != nil {
		return nil, false
	}
	specs := map[string]Spec{}
	for _, sym := range syms {
		mult, hasMult := multipliers[sym]
		if _, hasPre := f.PreSettlement(sym); !hasPre || !hasMult {
			return nil, false
		}
		inst, err := types.ParseSymbol(sym, f.TradingDay)
		if err != nil {
			return nil, false
		}
		product, _ := splitProduct(inst.Product)
		rates, _, ok := ratesOf(product)
		if !ok {
			return nil, false
		}
		mr, ok := ratesFor(product)
		if !ok {
			return nil, false
		}
		specs[sym] = Spec{Multiplier: decimal.RequireFromString(mult),
			Commission: rates, Margin: mr}
	}
	return specs, true
}
