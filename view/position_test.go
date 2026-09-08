package view

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// measuredPositionFields 从**夹具**里算出柜台实际给的持仓字段集。
//
// ⚠️ 与 measuredAccountFields 同一条原理：手抄的名单会和视图一起被同一个人改错。
func measuredPositionFields(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "testdata", "probes", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	union := map[string]bool{}
	files, sections := 0, 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Positions map[string]map[string]any `json:"positions"`
		}
		if json.Unmarshal(b, &f) != nil || len(f.Positions) == 0 {
			continue
		}
		files++
		for _, pos := range f.Positions {
			sections++
			for k := range pos {
				union[k] = true
			}
		}
	}
	// ⚠️ 迭代次数下界，理由同账户侧：夹具一份没读到时会得到空集，
	// 而空集与视图比对必然以「本库多出全部字段」的形式失败 —— 那是错的原因。
	if files < 10 || sections < 50 {
		t.Fatalf("只从 %d 份夹具、%d 个持仓截面里读到字段 —— 太少，"+
			"字段集会不完整而本条会以「本库多出字段」的形式失败，那是错的原因",
			files, sections)
	}
	out := make([]string, 0, len(union))
	for k := range union {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func rb2701(t *testing.T) types.InstrumentID {
	t.Helper()
	return types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
}

// newPos 建一个持仓，按 legs 开仓。legs 是 方向→(价, 手数) 的列表。
func newPos(t *testing.T, legs ...struct {
	Dir    types.Direction
	Price  string
	Volume int
}) *position.Position {
	t.Helper()
	day := types.NewTradingDay(2026, 9, 8)
	p, err := position.New(rb2701(t), types.Speculation, day, refdata.UseHistory)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range legs {
		if err := p.Open(l.Dir, day, decimal.RequireFromString(l.Price), l.Volume); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

type leg = struct {
	Dir    types.Direction
	Price  string
	Volume int
}

func mustPositionOf(t *testing.T, p *position.Position, in PositionInput) Position {
	t.Helper()
	v, err := PositionOf(p, in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestPositionViewCoversMeasuredFields 断言视图的字段集与**实测的**完全一致。
func TestPositionViewCoversMeasuredFields(t *testing.T) {
	want := measuredPositionFields(t)
	if len(want) < 40 {
		t.Fatalf("从夹具里只算出 %d 个持仓字段 —— 太少，本条可能在空转", len(want))
	}
	v := mustPositionOf(t, newPos(t), PositionInput{Multiplier: decimal.NewFromInt(10)})
	if err := v.CoverExactly(want); err != nil {
		t.Errorf("⚠️ %v", err)
	}
	t.Logf("实测持仓字段 %d 个，视图逐个覆盖", len(want))
}

// TestPositionNotImplementedNeverRendersAsZero 是本包存在的核心断言，持仓侧。
func TestPositionNotImplementedNeverRendersAsZero(t *testing.T) {
	v := mustPositionOf(t, newPos(t), PositionInput{Multiplier: decimal.NewFromInt(10)})
	n := 0
	for name, val := range v {
		switch val.Presence {
		case NotImplemented, NotModeled, Absent:
			n++
			if val.Presence == Present {
				t.Fatalf("不可达")
			}
			if !val.Number.IsZero() {
				t.Errorf("字段 %s 声明为%s，却带着数值 %s", name, val.Presence, val.Number)
			}
		}
	}
	// ⚠️ 下界：全都变成 Present 时上面一条也全绿，而那正是本条要抓的退化。
	if n < 10 {
		t.Fatalf("只有 %d 个字段声明为非「有值」—— 本条可能在空转", n)
	}
	t.Logf("非「有值」字段 %d 个，无一携带数值", n)
}

// TestFlatSideIsAbsentNotZero 是「明确无值」这一档存在的理由。
//
// ⚠️ 实测：188 份持仓截面里，open_price / position_price / margin
// 在空仓方向上**永远**是字符串 "-"，188/188 无反例。
// 若本库把这些渲染成 0，对拍侧无论把 "-" 读成 0 还是当成缺失，
// 都能得到全绿，而那两种读法互相矛盾。
func TestFlatSideIsAbsentNotZero(t *testing.T) {
	// 只开多头，空头这一边是空的。
	v := mustPositionOf(t, newPos(t, leg{types.Buy, "3149", 1}),
		PositionInput{
			Multiplier: decimal.NewFromInt(10),
			LastPrice:  decimal.NewFromInt(3149), HasLast: true,
			MarginLong: decimal.RequireFromString("2210.6"), HasMargin: true,
		})

	absent := []string{
		"open_price_short", "position_price_short", "margin_short",
		"float_profit_short", "position_profit_short",
	}
	for _, name := range absent {
		if got := v[name].Presence; got != Absent {
			t.Errorf("⚠️ 空仓方向的 %s 应当是「明确无值」，得到「%s」—— "+
				"渲染成 0 会与柜台的 \"-\" 在对拍时碰巧一致", name, got)
		}
	}
	// 反向：有持仓的那一边必须是有值，否则上面一条可以靠「全都无值」通过。
	for _, name := range []string{
		"open_price_long", "position_price_long", "margin_long",
		"float_profit_long", "position_profit_long",
	} {
		if got := v[name].Presence; got != Present {
			t.Errorf("有持仓方向的 %s 应当有值，得到「%s」", name, got)
		}
	}
}

// TestTotalDoesNotDowngradeAbsent 断言合计不会把「无值」悄悄当成 0。
func TestTotalDoesNotDowngradeAbsent(t *testing.T) {
	mult := decimal.NewFromInt(10)

	// ① 两边都空 —— 合计仍然是「无值」，不是 0。
	flat := mustPositionOf(t, newPos(t), PositionInput{
		Multiplier: mult, LastPrice: decimal.NewFromInt(3149), HasLast: true, HasMargin: true,
	})
	for _, name := range []string{"float_profit", "position_profit", "margin"} {
		if got := flat[name].Presence; got != Absent {
			t.Errorf("⚠️ 两边都空仓时 %s 应当是「明确无值」，得到「%s」", name, got)
		}
	}

	// ② 一边有一边空 —— 合计等于有值的那边，且是「有值」。
	oneSide := mustPositionOf(t, newPos(t, leg{types.Sell, "3148", 1}), PositionInput{
		Multiplier: mult, LastPrice: decimal.NewFromInt(3149), HasLast: true,
		MarginShort: decimal.RequireFromString("2210.6"), HasMargin: true,
	})
	if got := oneSide["float_profit"]; got.Presence != Present ||
		!got.Number.Equal(decimal.NewFromInt(-10)) {
		t.Errorf("⚠️ 单边持仓的 float_profit 合计应为 -10（有值），得到 %s/%s",
			got.Presence, got.Number)
	}
	if got := oneSide["margin"]; got.Presence != Present ||
		!got.Number.Equal(decimal.RequireFromString("2210.6")) {
		t.Errorf("⚠️ 单边持仓的 margin 合计应为 2210.6（有值），得到 %s/%s",
			got.Presence, got.Number)
	}

	// ③ 某一边「还没实现」时，合计也必须是「还没实现」——不是把它按 0 计入。
	noMargin := mustPositionOf(t, newPos(t, leg{types.Buy, "3149", 1}, leg{types.Sell, "3148", 1}),
		PositionInput{Multiplier: mult, LastPrice: decimal.NewFromInt(3149), HasLast: true})
	if got := noMargin["margin"].Presence; got != NotImplemented {
		t.Errorf("⚠️ 保证金未提供时 margin 合计应当是「还没实现」，得到「%s」—— "+
			"按 0 计入会给出一个看起来正常的错数", got)
	}
}

// TestPositionArithmeticMatchesFixture 用**实测截面**校验数值映射。
//
// 样本：exp3b-max-margin-lock-20260908.json 里的 SHFE.rb2701
// —— 多 1 手 @3149、空 1 手 @3148、最新价 3149、乘数 10。
// 这些数不是编的，是柜台给的；下面每一条都能回溯到那份夹具的一行。
func TestPositionArithmeticMatchesFixture(t *testing.T) {
	v := mustPositionOf(t,
		newPos(t, leg{types.Buy, "3149", 1}, leg{types.Sell, "3148", 1}),
		PositionInput{
			Multiplier: decimal.NewFromInt(10),
			LastPrice:  decimal.NewFromInt(3149), HasLast: true,
			MarginLong:  decimal.RequireFromString("2210.6"),
			MarginShort: decimal.RequireFromString("2210.6"),
			HasMargin:   true,
		})
	want := map[string]string{
		"volume_long": "1", "volume_short": "1",
		"volume_long_today": "1", "volume_short_today": "1",
		"volume_long_his": "0", "volume_short_his": "0",
		"pos_long_today": "1", "pos_short_today": "1",
		"open_price_long": "3149", "open_price_short": "3148",
		"position_price_long": "3149", "position_price_short": "3148",
		"open_cost_long": "31490", "open_cost_short": "31480",
		"position_cost_long": "31490", "position_cost_short": "31480",
		"float_profit_long": "0", "float_profit_short": "-10",
		"position_profit_long": "0", "position_profit_short": "-10",
		"float_profit": "-10", "position_profit": "-10",
		"margin_long": "2210.6", "margin_short": "2210.6", "margin": "4421.2",
		"last_price": "3149",
	}
	for name, w := range want {
		got := v[name]
		if got.Presence != Present {
			t.Errorf("⚠️ 字段 %s 应当有值，得到「%s」", name, got.Presence)
			continue
		}
		if !got.Number.Equal(decimal.RequireFromString(w)) {
			t.Errorf("⚠️ 字段 %s：本库 %s，夹具 %s", name, got.Number, w)
		}
	}
	t.Logf("与 exp3b 夹具逐字段一致的字段 %d 个", len(want))
}

// TestOracleTodayHisSplit 把今昨拆分这条**实测行为**钉在测试里。
//
// ⚠️ 它断言的不是本库，是**夹具**。而它已经被自己抓到过一次修正：
//
// 上一版叫 `TestOracleLeavesTodayHisCostAtZero`，断言「今昨拆分柜台一律填 0」，
// 依据是交易日 20260908 的 188 份截面。20260909 的结算一发生它就红了 ——
// **那正是它写下来的理由**：「哪天柜台开始填这些字段了，会有动静，
// 而不是被当成一直如此」。
//
// 修正后的事实分两条，各有各的形状：
//
//	open_cost_* / margin_* 的今昨拆分   **从未**是一个真实数字
//	                                    有量的那侧是 0，空的那侧是 "-"
//	position_cost_* 的今昨拆分          盘中新开的仓上是 0；
//	                                    **经历过结算之后被填上**
//
// 一致的解释：这个拆分是在**日终结算时**算出来的。
func TestOracleTodayHisSplit(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "testdata", "probes", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	// 这两类**任何时候**都不该是真实数字。
	neverFilled := []string{
		"open_cost_%s_today", "open_cost_%s_his",
		"margin_%s_today", "margin_%s_his",
	}
	withVolume, checked := 0, 0
	costFilled, costZero := 0, 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Positions map[string]map[string]any `json:"positions"`
		}
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		for inst, pos := range f.Positions {
			for _, side := range []string{"long", "short"} {
				today, _ := pos["volume_"+side+"_today"].(float64)
				his, _ := pos["volume_"+side+"_his"].(float64)
				if today+his <= 0 {
					continue
				}
				withVolume++
				for _, pat := range neverFilled {
					name := fmtSide(pat, side)
					checked++
					got, ok := pos[name].(float64)
					if ok && got != 0 {
						t.Errorf("⚠️ %s 的 %s.%s = %v —— "+
							"open_cost / margin 的今昨拆分**第一次**出现真实数字。"+
							"那是新行为，去重新量并改这条测试与 probes.md §9/§13",
							filepath.Base(p), inst, name, pos[name])
					}
				}
				// position_cost 的拆分：**结算时写的哪一侧**才有值。
				//
				// ⚠️ 上一版按「有量的那一侧」找，20260909 夜盘当场红了 7 处：
				// 那晚在 rb2701 上开了一手今仓（空头也开了一手），
				// 而 `position_cost_short_today` 给的是 `"-"` —— 有量却没值。
				//
				// 一致的解释：拆分是**日终结算时**算出来的，盘中不更新。
				// 于是它写的是哪一侧由 PositionDateType 定，与现在的手数无关：
				//
				//	UseHistory（SHFE）    结算时全变昨仓 → 写 _his，_today 恒 "-"
				//	NoUseHistory（DCE）   结算时仍是今仓 → 写 _today，_his 恒 "-"
				//	结算之后新开的仓        两侧都不写 —— 在拆分里**完全看不见**
				//
				// ⚠️ 后果：有盘中成交时，`_today + _his ≠ position_cost`。
				// 实测 rb2701：合计 126970 = 95310（昨 3 手 × 3177）
				// + 31660（今 1 手 × 开仓价 3166），而拆分只给得出前一项。
				// 拿拆分去凑合计的人会差一整块，且不报错。
				for _, w := range []string{"today", "his"} {
					name := "position_cost_" + side + "_" + w
					v, ok := pos[name].(float64)
					if !ok {
						continue // "-"：这一侧结算没写，合法
					}
					if v == 0 {
						costZero++
					} else {
						costFilled++
					}
				}
			}
		}
	}
	t.Logf("有量的方向 %d 个；open_cost/margin 拆分检查 %d 个字段，"+
		"无一是真实数字", withVolume, checked)
	t.Logf("position_cost 拆分：填了 %d 个、为零 %d 个", costFilled, costZero)

	// ⚠️ 下界必须卡在**有量的方向数**上，不是夹具数：
	// 一个有量的方向都没有时，上面的循环一次都不进，而本条照样绿 ——
	// 那时它断言的是「没有反例」，而没有样本时那句话恒真。
	if withVolume < 20 {
		t.Fatalf("只有 %d 个有量的方向 —— 太少，本条可能在空转", withVolume)
	}
	// ⚠️ 两侧都要有样本，否则「结算前为零、结算后被填」这句话只被验了一半。
	if costFilled == 0 {
		t.Errorf("⚠️ position_cost 的今昨拆分一个都没被填过 —— " +
			"「结算之后被填」这半句没有样本支持")
	}
	if costZero == 0 {
		t.Errorf("⚠️ position_cost 的今昨拆分没有一个是零 —— " +
			"「结算之前是零」这半句没有样本支持")
	}
}

func fmtSide(pattern, side string) string {
	out := make([]byte, 0, len(pattern)+len(side))
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '%' && i+1 < len(pattern) && pattern[i+1] == 's' {
			out = append(out, side...)
			i++
			continue
		}
		out = append(out, pattern[i])
	}
	return string(out)
}

// TestPositionMultiplierMustBePositive 断言乘数缺失会报错而不是静默出零。
func TestPositionMultiplierMustBePositive(t *testing.T) {
	for _, m := range []decimal.Decimal{decimal.Zero, decimal.NewFromInt(-1)} {
		if _, err := PositionOf(newPos(t, leg{types.Buy, "3149", 1}),
			PositionInput{Multiplier: m}); err == nil {
			t.Errorf("⚠️ 乘数 %s 应当报错 —— 乘数漏乘会得到一个"+
				"量级正确到肉眼看不出的错值", m)
		}
	}
}
