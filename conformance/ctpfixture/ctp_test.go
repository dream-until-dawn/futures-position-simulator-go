// Package ctpfixture 把 **CTP/SimNow 侧**的夹具接进对拍。
//
// ⚠️ 与 `conformance/fixture` 分开成包，理由不是洁癖：那一侧的夹具来自天勤 DIFF，
// 字段名、结构、可得的量**没有一个相同**。共用一个包会诱使人复用那边的加载器，
// 而复用出来的东西会「跑得通、但一个键都对不上」——
// 空对拍在下游是「跳过」，不是「报错」（同 `cmd/oracle/ctp/whitelist.go` 开头那段）。
package ctpfixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

type ctpFixture struct {
	Source     string                            `json:"source"`
	TradingDay string                            `json:"trading_day"`
	CapturedAt string                            `json:"captured_at"`
	Account    map[string]any                    `json:"account"`
	Positions  map[string]map[string]any         `json:"positions"`
	Quotes     map[string]map[string]any         `json:"quotes"`
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
	checked := 0
	for name, f := range fx {
		for sym, p := range f.Positions {
			vol := num(t, p, "Position")
			if vol.IsZero() {
				continue
			}
			id, err := types.ParseSymbol(sym[:len(sym)-len(filepath.Ext(sym))], types.TradingDay(20260910))
			if err != nil {
				// 键形如 "SHFE.rb2701/1"，末尾带方向后缀。
				base := sym
				for i := len(base) - 1; i >= 0; i-- {
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
			cost := num(t, p, "PositionCost")
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
				OpenPrice:  cost.Div(vol),
				// ⚠️ `PositionDate` 也是**字符串**（"1" 今仓 / "2" 昨仓）。
				// 两个枚举字段都栽在同一处：**白名单把 CTP 的单字节枚举按文本落盘**，
				// 而读的人按数字读。⚠️ 这不是夹具错，是**读的人没看夹具**。
				IsHistory: func() bool { d, _ := p["PositionDate"].(string); return d != "1" }(),
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
			if !got.Exchange.Equal(want) {
				t.Errorf("⚠️ %s / %s：本库算 %s，柜台给 %s（差 %s）—— "+
					"这是**第一次**本库的计算与柜台的数正面对上，红了要先查是哪一边",
					name, sym, got.Exchange, want, got.Exchange.Sub(want))
			}
			checked++
		}
	}
	if checked < 3 {
		t.Fatalf("⚠️ 只比了 %d 条持仓（下界 3）—— 夹具里有仓的那几份没被读到，本条在空转", checked)
	}
	t.Logf("ⓘ 对上了 %d 条持仓（⚠️ 全是今仓、单合约单方向 —— 见函数注释的三个边界）", checked)
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
	n := 0
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
		bal := flt(t, f.Account, "Balance")
		calc := bal - flt(t, f.Account, "CurrMargin") -
			flt(t, f.Account, "FrozenMargin") - flt(t, f.Account, "FrozenCommission")
		if want := flt(t, f.Account, "Available"); calc != want {
			t.Errorf("⚠️ %s：Balance−占用−冻结 = %v，而 Available = %v（差 %v）—— "+
				"**这份截面自己就不自洽**，拿去与本库比毫无意义", name, calc, want, want-calc)
		}
		n++
	}
	if n < 5 {
		t.Fatalf("⚠️ 只核了 %d 份（下界 5）—— 本条在空转", n)
	}
}
