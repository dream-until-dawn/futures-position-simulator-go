// Package ctpfixture 把 **CTP/SimNow 侧**的夹具接进对拍。
//
// ⚠️ 与 `conformance/fixture` 分开成包，理由不是洁癖：那一侧的夹具来自天勤 DIFF，
// 字段名、结构、可得的量**没有一个相同**。共用一个包会诱使人复用那边的加载器，
// 而复用出来的东西会「跑得通、但一个键都对不上」——
// 空对拍在下游是「跳过」，不是「报错」（同 `cmd/oracle/ctp/whitelist.go` 开头那段）。
package ctpfixture

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

type ctpFixture struct {
	Source     string `json:"source"`
	TradingDay string `json:"trading_day"`
	CapturedAt string `json:"captured_at"`
	// BrokerParams 是柜台的**声明**（经纪商交易参数）。
	//
	// ⚠️ 它 20260909 起就落在每一份夹具里，而加载器此前**根本没读它** ——
	// 于是 20260910 写下的那条可用资金恒等式与它矛盾了整整一天，
	// 而没有任何东西负责把两者对上。见 TestIdentityAgreesWithDeclaredAlgorithm。
	BrokerParams map[string]any            `json:"broker_params"`
	Account      map[string]any            `json:"account"`
	Positions    map[string]map[string]any `json:"positions"`
	Quotes       map[string]map[string]any `json:"quotes"`
}

func loadCTP(t *testing.T) map[string]ctpFixture {
	t.Helper()
	dir := filepath.FromSlash("../../testdata/ctp")
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ctpFixture{}
	for _, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		var f ctpFixture
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("%s：%v", n, err)
		}
		out[filepath.Base(n)] = f
	}
	if len(out) < 5 {
		t.Fatalf("⚠️ 只加载到 %d 份 CTP 夹具（下界 5）—— 目录或后缀变了？本条在空转", len(out))
	}
	return out
}

// flt 取一个 float64 字段，**不转 decimal**。
//
// ⚠️ 存在的理由见 `TestAccountIdentityAgainstCTP`：复现柜台自己的运算时，
// 必须用它自己那种算术。
func flt(t *testing.T, m map[string]any, k string) float64 {
	t.Helper()
	v, ok := m[k]
	if !ok {
		t.Fatalf("⚠️ 字段 %q 不在这份夹具里 —— 白名单改过？本条要比的东西没了", k)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("⚠️ 字段 %q 不是数（%T）", k, v)
	}
	return f
}

func num(t *testing.T, m map[string]any, k string) decimal.Decimal {
	t.Helper()
	v, ok := m[k]
	if !ok {
		t.Fatalf("⚠️ 字段 %q 不在这份夹具里 —— 白名单改过？本条要比的东西没了", k)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("⚠️ 字段 %q 不是数（%T）", k, v)
	}
	return decimal.NewFromFloat(f)
}

// TestMarginAgainstCTP 是**第一条把本库的计算与柜台的数正面对上**的对拍。
//
// # ⚠️ 它比的是什么
//
//	本库   margin.Compute(legs, OpenTodayPreSettleHistory, NoNetting).Exchange
//	柜台   持仓记录里的 UseMargin
//
// # ⚠️ 三个必须写在这里的边界
//
// **一、乘数被折进了价里。** CTP 的持仓记录**不带合约乘数**（它在合约查询里），
// 而 `margin.Compute` 要 `量 × 价 × 乘数 × 率`。这里喂
// `OpenPrice = PositionCost / Volume`、`Multiplier = 1`，乘积不变。
// ⚠️ **代价：本条对「本库怎么处理乘数」是瞎的。**若 `Compute` 把乘数用错了，
// 这条一个字都不会说。⇒ 要治它得让夹具带上 `VolumeMultiple`（待办）。
//
// **二、`OpenTodayPreSettleHistory` 与 CTP 声明的 `OpenPrice` 在这批样本上分不开。**
// SimNow 声明 `MarginPriceType = 4`（开仓价），而本库的候选集里**没有**「全部用开仓价」
// 这一项（margin.PriceBasis 的注释里记着这个缺口）。
// ⚠️ 这批夹具**全是今仓**，而两个候选在今仓上给出同一个数 —— **分不开**。
// ⇒ 分开它要一份**昨仓**样本（state.md 的清单 ①）。
//
// **四、⚠️ 交易所口径与公司口径在这批样本上**恒等**。**
// 七份夹具里 `UseMargin == ExchangeMargin` **无一例外**（0 / 5033.6 / 5051.2）。
// 本条比的是 `.Exchange`，⇒ **它分不开「本库该产出交易所口径还是公司口径」**。
//
// ⚠️ 而这不是本条的局限，是 `cn-futures-rules.md` §13 第 14 条那个盲区
// **在第二个口子上重现，且原因相同：加收为零**。
//
//	快期   加收为零 ⇒ 两口径恒等 ⇒ 分不开
//	CTP    加收为零 ⇒ 两口径恒等 ⇒ 分不开
//
// ⇒ 那条盲区的**性质因此变了**：从「一个口子的局限」变成
// **「我们能接触到的全部数据源的共同局限」** ——
// ⚠️ 而这两句话在 `rules_pending` 里长得一模一样。
// **要解决它需要的不是「换个口子」，是一个真收加收的账户或品种。**
//
// **三之二、⚠️ 「按手数」那一项在这批样本上恒为零。**
// `MarginRateByVolume` 全是 0 ⇒ `exchange = notional×byMoney + volume×byVolume`
// 里的第二项**永远不参与**。**去掉它，本条照样全绿。**
// ⚠️ 与手续费那次同形（silent-risks.md：*一个恒为零的项，让相加与二选一无从区分*）——
// 这里更进一步：**它让那一项连「有没有被算」都测不出来。**
// ⇒ 破坏 265 就钉这个：去掉按手数那一项，本条**仍然绿**，是**预期的绿**。
//
// **三、`NoNetting`。** 样本里只有单方向单合约，⚠️ 于是三种 SideScope
// 给出同一个数 —— **本条对大边这一维判别力为零**（silent-risks.md 77）。
func TestMarginAgainstCTP(t *testing.T) {
	fx := loadCTP(t)
	checked, history := 0, 0
	for name, f := range fx {
		for sym, p := range f.Positions {
			vol := num(t, p, "Position")
			if vol.IsZero() {
				continue
			}
			id, err := types.ParseSymbol(sym[:len(sym)-len(filepath.Ext(sym))], types.TradingDay(20260910))
			if err != nil {
				// ⚠️ 键形如 `SHFE.rb2701/1`（旧）或 `SHFE.rb2701/2/1`（20260910 夜盘起，
				// 先方向后今昨）—— **截到第一个 `/`**，兼容两种。
				//
				// ⚠️ 上一版截的是**最后一个** `/`，那在旧格式上对、在新格式上
				// 会留下 `SHFE.rb2701/2` —— 而它解析失败会 Fatal，不会静默。
				// 那算走运：一个「多截一段」的解析错，更常见的下场是解出**另一个合约**。
				base := sym
				for i := 0; i < len(base); i++ {
					if base[i] == '/' {
						base = base[:i]
						break
					}
				}
				if id, err = types.ParseSymbol(base, types.TradingDay(20260910)); err != nil {
					t.Fatalf("⚠️ %s：解析不出合约 %q：%v", name, sym, err)
				}
			}
			bm, bv := num(t, p, "MarginRateByMoney"), num(t, p, "MarginRateByVolume")
			// ⚠️ **柜台在昨仓上把 `MarginRateByMoney` 报成 0。**
			//
			// 20260910 夜盘第一份昨仓截面（ctp-status-20260911.json）实测：
			// 今仓那几份都给 0.16，昨仓那份给 **0**。
			// ⇒ 拿它去算会得到保证金 0，而 0 与「这个合约免保证金」在数上一样。
			//
			//	⚠️ **「没有」不是「零」** —— 这正是本库到处在防的那件事，
			//	而这一次是**柜台自己**用零值表达了「没有」。
			//
			// ⇒ 跳过并**大声说**，不拿 0 顶替。要把这一格接上，
			// 得让夹具带上费率的独立来源（refdata 的合约规格），那是另一件事。
			if bm.IsZero() && bv.IsZero() {
				t.Logf("⚠️ %s / %s：柜台没给保证金率（两个都是 0）—— **跳过**。"+
					"昨仓截面上实测如此；拿 0 去算会得到「免保证金」，"+
					"而那与「柜台没告诉我费率」在数上分不开", name, sym)
				continue
			}
			cost := num(t, p, "PositionCost")
			openCost := num(t, p, "OpenCost")
			dir := types.Buy // ⚠️ 多头持仓在本库用 Buy 表示
			// ⚠️ `PosiDirection` 在夹具里是**字符串**（白名单把那个字节按文本落盘），
			// 不是数。第一版按数读，当场 Fatal —— **而那正是它该做的**：
			// 一个类型读错的字段若被默默当成零值，方向会整体翻成多头而没有任何动静。
			if d, _ := p["PosiDirection"].(string); d == "3" {
				dir = types.Sell
			}
			leg := margin.Leg{
				Instrument: id,
				Direction:  dir,
				Volume:     int(vol.IntPart()),
				// ⚠️ 乘数折进价里，见函数注释「边界一」。
				Multiplier: decimal.NewFromInt(1),
				// ⚠️ **开仓价取自 `OpenCost`，昨结算价取自 `PositionCost`** ——
				// 而判别力全在这一对上：结算**只重置 PositionCost**，OpenCost 不动。
				//
				//	20260911 实测   OpenCost 31480（= 3148×10，开仓价）
				//	                PositionCost 31470（= 3147×10，**已按昨结算价重置**）
				//
				// ⇒ 本库若在昨仓上用开仓价，会算出 5036.80；柜台给 5035.20。**分得开。**
				// ⚠️ 第一版这里两个都取 PositionCost，那样两个价相等 ⇒
				// **这条对拍在昨仓样本上判别力为零，而它照样会绿。**
				OpenPrice: openCost.Div(vol),
				// ⚠️ `PositionDate` 也是**字符串**（"1" 今仓 / "2" 昨仓）。
				// 两个枚举字段都栽在同一处：**白名单把 CTP 的单字节枚举按文本落盘**，
				// 而读的人按数字读。⚠️ 这不是夹具错，是**读的人没看夹具**。
				IsHistory: func() bool { d, _ := p["PositionDate"].(string); return d != "1" }(),
				// ⚠️ 昨仓才有昨结算价这一项；今仓给了也用不到（基准是开仓价）。
				PreSettlement:    cost.Div(vol),
				HasPreSettlement: true,
				Rates: margin.Rates{
					LongByMoney: bm, LongByVolume: bv,
					ShortByMoney: bm, ShortByVolume: bv,
				},
			}
			got, err := margin.Compute([]margin.Leg{leg}, margin.OpenTodayPreSettleHistory, margin.NoNetting)
			if err != nil {
				t.Fatalf("⚠️ %s / %s：Compute 报错：%v", name, sym, err)
			}
			want := num(t, p, "UseMargin")
			// ⚠️ 容差 **1e-6**，而它有一个说得出来的理由，不是把差抹掉。
			//
			// 柜台的数经 CTP 的 `double` 回来：20260911 那份昨仓截面里
			// `UseMargin` 是 **5035.200000000001** —— 尾巴上的 1e-12 是
			// float64 的表示噪声，不是口径差。
			//
			// ⚠️ **而容差要多小才算安全，取决于「最小要分开的差」是多少**：
			//
			//	本实验的三个候选  5035.20 / 5036.80 / 5033.60 —— 两两差 **1.60**
			//	容差             1e-6
			//	⇒ 相差 **1.6 × 10⁶ 倍**：一次真的口径错落不进这个容差里
			//
			// ⇒ 而残差**每次都打印**：容差挡掉的是噪声，不该顺手挡掉可见性。
			// 哪天残差从 1e-12 变成 1e-7，它仍然会绿，但日志里看得见它在长。
			resid := got.Exchange.Sub(want)
			if resid.Abs().GreaterThan(decimal.RequireFromString("0.000001")) {
				t.Errorf("⚠️ %s / %s：本库算 %s，柜台给 %s（差 %s）—— "+
					"这是**第一次**本库的计算与柜台的数正面对上，红了要先查是哪一边。"+
					"⚠️ 差已超出 1e-6 的 float64 噪声容差，那不是表示误差",
					name, sym, got.Exchange, want, resid)
			} else if !resid.IsZero() {
				t.Logf("ⓘ %s / %s：残差 %s（≤1e-6，判为 CTP double 的表示噪声；"+
					"最小要分开的口径差是 1.60）", name, sym, resid)
			}
			if leg.IsHistory {
				history++
			}
			checked++
		}
	}
	if checked < 3 {
		t.Fatalf("⚠️ 只比了 %d 条持仓（下界 3）—— 夹具里有仓的那几份没被读到，本条在空转", checked)
	}
	// ⚠️ **昨仓样本的下界单列。**
	//
	// 20260910 夜盘之前，这条对拍比过的**全是今仓** —— 而
	// `OpenTodayPreSettleHistory` 的两条支路里，昨仓那条**一次都没走到**。
	// 那时它绿，说明的只是今仓那半对。
	//
	//	⚠️ 一条覆盖两条支路的判据，在只走过一条时和全走过时长得一样。
	if history < 1 {
		t.Errorf("⚠️ 比过的 %d 条里**一条昨仓都没有** —— "+
			"OpenTodayPreSettleHistory 的昨仓支路没被走到，"+
			"本条此刻只验了今仓那一半", checked)
	}
	t.Logf("ⓘ 对上了 %d 条持仓，其中昨仓 %d 条（⚠️ 单合约单方向 —— 见函数注释的边界）",
		checked, history)
}

// TestAccountIdentityAgainstCTP 钉住账户侧的恒等式。
//
//	Available = Balance − CurrMargin − FrozenMargin − FrozenCommission
//
// ⚠️ **它与上一条不是同一种东西**：上一条用**本库的算式**去核柜台的数，
// 这一条只核**柜台自己内部**是否自洽 —— 本库没有参与。
// 写在这里是因为它保护上一条：一份自己就不自洽的截面，
// 拿去与本库比毫无意义（同 `kq_facts` 41 那次的教训）。
func TestAccountIdentityAgainstCTP(t *testing.T) {
	fx := loadCTP(t)
	n, positives := 0, 0
	for name, f := range fx {
		if len(f.Account) == 0 {
			continue
		}
		// ⚠️ **必须在 float64 里算，不能用 decimal。**
		//
		// CTP 的 API 返回 `double`，柜台这几个数就是 float64 算出来的。
		// 20260910 第一版用 decimal 精确相等去比，**四份夹具全红**，差约 2e-9 ——
		// 而实测 `bal-cm-fm-fc == av` 在 **float64 里精确为真**。
		//
		//	差的不是柜台，是**我拿一把更准的尺子去量一把不那么准的尺子刻出来的东西**。
		//
		// ⚠️ 这一条对整个 CTP 对拍都成立，写在这里免得下一个人再撞：
		// **本库是 decimal、柜台是 float64，凡是拿本库的算式去核柜台的数，
		// 判据都必须带一个由 float64 精度决定的容差** —— 而那个容差要算出来，不能拍脑袋。
		// （本条不需要容差，因为它复现的是柜台**自己的**那次 float64 运算。）
		// ⚠️⚠️ **减项里那个 `max(浮盈, 0)` 是 20260911 夜盘被一份夹具逼出来的，
		// 而它推翻的是这条断言此前的形状。**
		//
		// 原式是 `Balance − 占用 − 冻结 == Available`，它在**十八份夹具上分毫不差** ——
		// 而那十八份的 `PositionProfit` 无一为正。第十九份（`ctp-slices-20260914-2`，
		// 两片白银今仓）浮盈 +150，原式当场差 150。
		//
		//	⚠️ 柜台的规则是**浮盈不计入可用、浮亏立即扣** ——
		//	而这条不对称**只在赢着的那一侧显形**。
		//	十八份夹具不是「十八次验证」，是**同一侧的十八个样本**。
		//
		// ⚠️ 而这条断言当时的错误信息写的是「**这份截面自己就不自洽**」——
		// 那句话把「我的模型错了」说成了「数据坏了」。
		// 它差一点让我去查那次落盘出了什么毛病，而截面是对的。
		// ⇒ 措辞已改：先说是哪一边不符，再说两种可能。
		bal := flt(t, f.Account, "Balance")
		pp := flt(t, f.Account, "PositionProfit")
		unreal := math.Max(pp, 0)
		calc := bal - flt(t, f.Account, "CurrMargin") -
			flt(t, f.Account, "FrozenMargin") - flt(t, f.Account, "FrozenCommission") - unreal
		if want := flt(t, f.Account, "Available"); calc != want {
			t.Errorf("⚠️ %s：Balance−占用−冻结−max(浮盈,0) = %v，而柜台给的 Available = %v"+
				"（差 %v；PositionProfit=%v）—— 要么**本式还缺一项**，"+
				"要么这份截面真的不自洽。⚠️ 别默认是后者：20260911 夜盘正是本式缺了"+
				"`max(浮盈,0)`，而当时的措辞让人去查落盘",
				name, calc, want, want-calc, pp)
		}
		if pp > 0 {
			positives++
		}
		n++
	}
	if n < 5 {
		t.Fatalf("⚠️ 只核了 %d 份（下界 5）—— 本条在空转", n)
	}
	// ⚠️ **判别力**：`max(浮盈, 0)` 这一项只在浮盈为正时才不等于 0。
	// 一批全是浮亏/零的夹具上，本条与**去掉那一项的旧式**给出完全一样的结果 ——
	// 而旧式正是被推翻的那个。
	//
	//	⚠️ 于是「全绿」在这里有两种读法：**式子对了**，或者**这一项从没被求值过**。
	//	十八份夹具就这样把一条错的恒等式供着，直到第十九份出现。
	//
	// ⇒ 没有一份正浮盈的夹具时，本条**必须报出来**：它此刻守不住它自称在守的东西。
	if positives == 0 {
		t.Fatalf("⚠️ %d 份夹具里**没有一份 PositionProfit > 0** —— "+
			"`max(浮盈,0)` 这一项一次都没被求值，本条与被推翻的旧式等价。"+
			"⇒ 去拍一份赢着的截面（`oracle ctp-slices` 造两片今仓即可），别删这条", n)
	}
}
