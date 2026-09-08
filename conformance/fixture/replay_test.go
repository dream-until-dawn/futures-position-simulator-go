package fixture

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

var (
	testInst = types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
	dayD     = types.NewTradingDay(2026, 9, 8)
	dayD1    = types.NewTradingDay(2026, 9, 9)
)

func dd(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// seeded 造一个「昨仓 2 手 + 今仓 1 手」的起始持仓。
//
// 昨仓由**真的走一遍结算**得来，不是手工塞 Settled=true：
// 手工塞会绕过 Settle，而 Settle 正是唯一把今变昨的地方，
// 绕过它的测试测的是一个本库里不存在的状态。
func seeded(t *testing.T) *position.Position {
	t.Helper()
	p, err := position.New(testInst, types.Speculation, dayD)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Open(types.Buy, dayD, dd("3100"), 1); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(types.Buy, dayD, dd("3200"), 1); err != nil {
		t.Fatal(err)
	}
	// 结算：两笔都变昨仓，基线统一成结算价 3150。
	if err := p.Settle(dayD, dd("3150"), dayD1); err != nil {
		t.Fatal(err)
	}
	// D+1 日再开一手今仓。
	if err := p.Open(types.Buy, dayD1, dd("3300"), 1); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestReplayDetectsAmbiguity 断言歧义检查**真的会触发**。
//
// ⚠️ 这条测试的存在本身是一次发现：在 start 为 nil 时，
// 那个「三种消耗顺序各跑一遍」的检查**结构上不可能触发** ——
// 从成交重放出来的全是今仓，三种顺序消耗的是同一批。
// 也就是说它在整个夹具批上都是装饰，跑三遍得到三个相同的结果。
//
// 它只有在起始持仓带着昨仓时才开始工作，而那正是今晚的形状：
// 今晚的持仓截面里有昨仓，今晚的成交里没有一笔能解释它。
func TestReplayDetectsAmbiguity(t *testing.T) {
	// 一笔 CLOSE（不分今昨），2 手 —— 顺序不同则消耗不同：
	//   先平昨 → 吃掉两手昨仓，剩今仓 3300
	//   先平今 → 吃掉今仓 3300 与一手昨仓，剩一手昨仓
	trades := []Trade{{
		TradeID: "t1", Instrument: testInst,
		Direction: types.Sell, Offset: types.Close,
		Hedge: types.Speculation, Price: dd("3250"), Volume: 2, At: 1,
	}}
	_, err := ReplayFrom(seeded(t), testInst, types.Speculation, dayD1, trades)
	if err == nil {
		t.Fatal("⚠️ 三种消耗顺序会给出不同的持仓，重放却没报歧义 —— " +
			"此时返回的持仓是三个候选里随手挑的一个，而它看起来完全正常")
	}
	if !strings.Contains(err.Error(), "重放有歧义") {
		t.Errorf("报错了但不是歧义：%v", err)
	}
	t.Logf("歧义被抓住：%v", err)
}

// TestReplayIsFineWhenOrderCannotMatter 是上一条的对照组。
//
// ⚠️ 没有它，上一条可以靠「一律报歧义」通过 ——
// 而一个一律报错的检查与没有检查是一回事，只是更吵。
func TestReplayIsFineWhenOrderCannotMatter(t *testing.T) {
	// 平今 1 手：CLOSETODAY 不看消耗顺序，三种候选必然一致。
	trades := []Trade{{
		TradeID: "t1", Instrument: testInst,
		Direction: types.Sell, Offset: types.CloseToday,
		Hedge: types.Speculation, Price: dd("3250"), Volume: 1, At: 1,
	}}
	p, err := ReplayFrom(seeded(t), testInst, types.Speculation, dayD1, trades)
	if err != nil {
		t.Fatalf("平今不依赖消耗顺序，不该报歧义：%v", err)
	}
	s, err := p.Side(types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.VolumeToday(); got != 0 {
		t.Errorf("今仓应剩 0 手，得到 %d", got)
	}
	if got := s.VolumeHistory(); got != 2 {
		t.Errorf("昨仓应仍是 2 手，得到 %d", got)
	}
}

// TestReplayFromCarriesHistoryLots 断言起始持仓的**昨仓身份**被带过来了。
//
// ⚠️ 用 Side.Append 而不是重新 Open 是刻意的：Open 会把这一笔当成今仓
// （Basis = 开仓价、Settled = false）。走 Open 会把昨仓悄悄变成今仓，
// 而变完之后平今平昨的判定、手续费、保证金基线全部错位且**不报错**
// —— silent-risks.md 的第 1 条。
func TestReplayFromCarriesHistoryLots(t *testing.T) {
	p, err := ReplayFrom(seeded(t), testInst, types.Speculation, dayD1, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Side(types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.VolumeHistory(); got != 2 {
		t.Errorf("⚠️ 昨仓 2 手没带过来，得到 %d —— "+
			"昨仓变今仓之后平今平昨、手续费、保证金基线全部错位且不报错", got)
	}
	if got := s.VolumeToday(); got != 1 {
		t.Errorf("今仓应为 1 手，得到 %d", got)
	}
	// ⚠️ 两条基线必须**分别**保住：
	// 昨仓的 OpenPrice 停在原始成交价（逐笔对冲），Basis 是结算价（逐日盯市）。
	// 这正是本项目最核心的那对区分，而拷贝是最容易把它们弄平的地方。
	var openPrices, bases []string
	for _, l := range s.Lots() {
		if !l.Settled {
			continue
		}
		openPrices = append(openPrices, l.OpenPrice.String())
		bases = append(bases, l.Basis.String())
	}
	if len(openPrices) != 2 || openPrices[0] == openPrices[1] {
		t.Errorf("⚠️ 昨仓的开仓价应当是两个不同的数（3100/3200），得到 %v —— "+
			"被压成均价了？那就再也算不回逐笔对冲口径", openPrices)
	}
	for _, b := range bases {
		if b != "3150" {
			t.Errorf("⚠️ 昨仓的逐日盯市基线应为结算价 3150，得到 %s", b)
		}
	}
}

// TestSignatureSeesLotLevelDifference 断言摘要不是只看均价。
//
// ⚠️ 用均价做摘要会让「(2手@100,1手@130) 与 (3手@110)」判成相同 ——
// 而它们平掉 1 手 @120 时逐笔对冲一个 +20 一个 +10，两个结果都不会报错。
func TestSignatureSeesLotLevelDifference(t *testing.T) {
	mk := func(lots [][2]string) *position.Position {
		p, err := position.New(testInst, types.Speculation, dayD)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range lots {
			v := 0
			for _, c := range l[1] {
				v = v*10 + int(c-'0')
			}
			if err := p.Open(types.Buy, dayD, dd(l[0]), v); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	a := mk([][2]string{{"100", "2"}, {"130", "1"}})
	b := mk([][2]string{{"110", "3"}})
	// 均价相同：(100×2+130)/3 = 110
	sa, _ := mustSide(t, a).AvgOpenPrice()
	sb, _ := mustSide(t, b).AvgOpenPrice()
	if !sa.Equal(sb) {
		t.Fatalf("前提不成立：两者均价应相同，得到 %s 与 %s —— "+
			"⚠️ 前提垮了的话下面那条断言证明的是别的东西", sa, sb)
	}
	if signature(a) == signature(b) {
		t.Errorf("⚠️ 摘要把明细不同、均价相同的两个持仓判成了相同 —— "+
			"用均价做摘要，等于把「暂时看不出来」当成「相同」：\n  %s", signature(a))
	}
}

func mustSide(t *testing.T, p *position.Position) *position.Side {
	t.Helper()
	s, err := p.Side(types.Buy)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
