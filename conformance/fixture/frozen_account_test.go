package fixture

import (
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/internal/decimalx"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
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
		book := order.NewBook()
		skipped := false
		for id, o := range f.Orders {
			alive, ok := aliveOf(o)
			if !ok || !alive {
				continue
			}
			left, ok := numberOf(o, "volume_left")
			if !ok || !left.IsPositive() {
				continue
			}
			sym, ok := textOf(o, "exchange_id", "instrument_id")
			if !ok {
				t.Errorf("⚠️ %s 的委托 %s 读不出合约", f.Path, id)
				continue
			}
			dir, off, err := dirOffsetOf(o)
			if err != nil {
				t.Errorf("⚠️ %s 的委托 %s：%v", f.Path, id, err)
				continue
			}
			if off == types.Close {
				// 裸 CLOSE：⚠️ 冻的是今仓还是昨仓取决于口子，
				// 而**账户侧的金额不受它影响**（手续费一样收、保证金一样不冻）。
				// 所以这里把它当平昨处理是安全的 —— 但要说明为什么安全。
				off = types.CloseYesterday
			}
			pre, hasPre := f.PreSettlement(sym)
			mult, hasMult := multipliers[sym]
			if !hasPre || !hasMult {
				// ⚠️ 缺输入就整份跳过并说出来，不算一半。
				// 算一半的合计会**偏小**，而偏小的方向是「看起来钱更多」。
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
			// ⚠️ 本库自己算手续费 —— 不抄柜台委托记录里的 frozen_commission。
			c, err := fee.Compute(rates, off, pre,
				decimal.RequireFromString(mult), int(left.IntPart()), decimalx.NoRounding)
			if err != nil {
				t.Errorf("%s 算手续费：%v", f.Path, err)
				continue
			}
			// 平仓单不冻保证金（20260909 实测：三份样本的账户 frozen_margin 都是 0）。
			in := order.FreezeInput{Margin: decimal.Zero, Commission: c}
			req := order.Request{Instrument: inst, Direction: dir, Offset: off,
				Hedge: types.Speculation, Price: decimal.Zero,
				Volume: int(left.IntPart())}
			if err := book.Insert(id, req, in); err != nil {
				t.Errorf("%s 的委托 %s 进簿失败：%v", f.Path, id, err)
			}
		}
		if skipped {
			continue
		}
		tot := book.Total()
		for _, c := range []struct {
			key  string
			want decimal.Decimal
		}{
			{"frozen_margin", tot.Margin},
			{"frozen_commission", tot.Commission},
		} {
			got, ok := numberOf(f.Account, c.key)
			if !ok {
				t.Errorf("⚠️ %s 的账户截面没有 %s", f.Path, c.key)
				continue
			}
			compared++
			if !c.want.IsZero() {
				nonZero++
			}
			if got.Sub(c.want).Abs().GreaterThan(decimal.RequireFromString("0.0000001")) {
				t.Errorf("⚠️ %s 的 %s：柜台 %s，本库从委托算出 %s —— "+
					"⚠️ 账户侧冻结对不上。先查是**费额算错**还是"+
					"**平仓单被当成开仓冻了保证金**", f.Path, c.key, got, c.want)
			}
		}
	}
	if compared == 0 {
		t.Skip("还没有记了委托的夹具 —— 账户侧冻结对拍待样本")
	}
	// ⚠️ 判别力：必须有非零。全零的一致什么都不说明。
	if nonZero == 0 {
		t.Errorf("⚠️ 比了 %d 个字段，**没有一个是非零的** —— "+
			"全零的一致什么都不说明：把整块冻结逻辑删掉，它照样一致", compared)
	}
	t.Logf("账户侧冻结对拍：比了 %d 个字段，其中本库算出非零的 %d 个", compared, nonZero)
}

// TestFrozenTotals 直测金额侧的冻结合计。
//
// ⚠️ 它是那段代码**唯一**的验证：Rebuild 里调用它的那一支在现有语料上
// 跑不到（记了委托的夹具全都带昨仓，而 Rebuild 拒绝昨仓）。
// 「有实现」与「实现被跑过」是两回事，而它们在代码上长得一模一样。
func TestFrozenTotals(t *testing.T) {
	specs := map[string]Spec{"SHFE.rb2701": {
		Multiplier: dd("10"),
		Commission: mustRates(t, "rb"),
	}}
	f := &Fixture{
		HasOrders: true,
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
	m, c, err := frozenTotals(f, specs)
	if err != nil {
		t.Fatal(err)
	}
	// 平仓单不冻保证金（20260909 实测）。
	if !m.IsZero() {
		t.Errorf("⚠️ 平仓挂单冻了 %s 保证金 —— 实测柜台不冻（kq_facts 42）", m)
	}
	// 手续费 = 3163 × 10 × 0.00001 = 0.3163
	if !c.Equal(dd("0.3163")) {
		t.Errorf("⚠️ 冻结手续费 %s，应为 0.3163 —— "+
			"已终结的那笔（9 手）是不是被算进来了？", c)
	}

	// ⚠️ **开仓挂单要报错**：它要冻保证金，而本函数没有实现那一支。
	// 没见过的情形不猜 —— 猜出来的冻结额会让可用资金错一大截。
	f.Orders["c"] = withLeft(ord("status", "ALIVE", "exchange_id", "SHFE",
		"instrument_id", "rb2701", "direction", "BUY", "offset", "OPEN"), "1")
	if _, _, err := frozenTotals(f, specs); err == nil {
		t.Error("⚠️ 开仓挂单竟然算出了冻结 —— 它要冻保证金，而那一支没实现；" +
			"静默当成 0 会让可用资金多出一大截")
	}
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
