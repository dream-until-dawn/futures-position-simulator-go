package futsim

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/fee"
	"github.com/dream-until-dawn/futures-position-simulator-go/margin"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

const simDay = types.TradingDay(20260915)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func simInst(t *testing.T, sym string) types.InstrumentID {
	t.Helper()
	id, err := types.ParseSymbol(sym, simDay)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// simRules 是**合成的**规则数据：三档手续费刻意取不同的值，好让「走了哪一档」看得出来。
// ⚠️ 这些数不是任何交易所的真实费率。
func simRules(t *testing.T) refdata.Provider {
	t.Helper()
	m, ag, y, rb := simInst(t, "DCE.m2701"), simInst(t, "SHFE.ag2702"), simInst(t, "DCE.y2701"), simInst(t, "SHFE.rb2701")
	ma := simInst(t, "CZCE.MA2701")
	b := refdata.NewBuilder(1).
		// MA2701（郑商所）：NoUseHistory，平昨 2 / 平今 6 —— 照 ctp-commission-rates-20260915.txt 的声明，
		// 方向与 m 相反（m 平今便宜、MA 平今贵）。给「§13 #21 的 (a) 只认大商所」那一格用：
		// 这里超出今仓、两档不同 ⇒ 要照旧报错不猜。
		AddInstrument(refdata.Instrument{ID: ma, VolumeMultiple: dec("10"), PriceTick: dec("1"),
			PositionDateType: refdata.NoUseHistory, IsTrading: true,
			MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000, PriceLimitRatio: dec("0.07"), HasPriceLimitRatio: true}).
		AddCommissionRates(ma, types.Speculation, refdata.CommissionRates{
			OpenByVolume: dec("2"), CloseByVolume: dec("2"), CloseTodayByVolume: dec("6")}).
		AddMarginRates(ma, types.Speculation, refdata.MarginRates{LongByMoney: dec("0.1"), ShortByMoney: dec("0.1")}).
		// rb2701：UseHistory 且开 / 平昨 / 平今三档按手费率**两两不同**，给「裸 CLOSE 记作平昨」收哪一档用（ag2702 三档同费率，分不开）
		AddInstrument(refdata.Instrument{ID: rb, VolumeMultiple: dec("10"), PriceTick: dec("1"),
			PositionDateType: refdata.UseHistory, IsTrading: true,
			MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000, PriceLimitRatio: dec("0.07"), HasPriceLimitRatio: true}).
		AddCommissionRates(rb, types.Speculation, refdata.CommissionRates{
			OpenByVolume: dec("1"), CloseByVolume: dec("2"), CloseTodayByVolume: dec("5")}).
		AddMarginRates(rb, types.Speculation, refdata.MarginRates{LongByMoney: dec("0.1"), ShortByMoney: dec("0.1")}).
		// 手数上限与涨跌幅比例给报单路径（Submit）用。m2701 的 6% 按大商所四舍五入对齐后是 3587 / 3181，
		// 与交易日 20260915 行情里的涨跌停价一致（ctp-slices-20260915.json 的 quotes）。
		AddInstrument(refdata.Instrument{ID: m, VolumeMultiple: dec("10"), PriceTick: dec("1"),
			PositionDateType: refdata.NoUseHistory, IsTrading: true,
			MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000, PriceLimitRatio: dec("0.06"), HasPriceLimitRatio: true}).
		AddInstrument(refdata.Instrument{ID: ag, VolumeMultiple: dec("15"), PriceTick: dec("1"),
			PositionDateType: refdata.UseHistory, IsTrading: true,
			MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000, PriceLimitRatio: dec("0.09"), HasPriceLimitRatio: true}).
		// y2701：平昨档 = 平今档（两档同费率），给 §13 #21 「超出今仓的部分」那一支用
		AddInstrument(refdata.Instrument{ID: y, VolumeMultiple: dec("10"), PriceTick: dec("2"),
			PositionDateType: refdata.NoUseHistory, IsTrading: true,
			MinLimitOrderVolume: 1, MaxLimitOrderVolume: 1000, PriceLimitRatio: dec("0.06"), HasPriceLimitRatio: true}).
		AddCommissionRates(y, types.Speculation, refdata.CommissionRates{
			OpenByVolume: dec("2.5"), CloseByVolume: dec("1.1"), CloseTodayByVolume: dec("1.1")}).
		AddMarginRates(y, types.Speculation, refdata.MarginRates{LongByMoney: dec("0.1"), ShortByMoney: dec("0.1")}).
		AddCommissionRates(m, types.Speculation, refdata.CommissionRates{
			OpenByVolume: dec("1.5"), CloseByVolume: dec("1.2"), CloseTodayByVolume: dec("0.75")}).
		AddCommissionRates(ag, types.Speculation, refdata.CommissionRates{
			OpenByMoney: dec("0.00001"), CloseByMoney: dec("0.00001"), CloseTodayByMoney: dec("0.00001")}).
		AddMarginRates(m, types.Speculation, refdata.MarginRates{LongByMoney: dec("0.1"), ShortByMoney: dec("0.1")}).
		AddMarginRates(ag, types.Speculation, refdata.MarginRates{LongByMoney: dec("0.19"), ShortByMoney: dec("0.19")})
	snap, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func ctpChoices() Choices {
	c := CTPChoices()
	c.FeeRounding = fee.NoRounding
	return c
}

func newSim(t *testing.T) *Simulator {
	t.Helper()
	s, err := New(Config{Day: simDay, PreBalance: dec("100000"), Rules: simRules(t), Choices: ctpChoices()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func trade(t *testing.T, sym string, dir types.Direction, off types.Offset, px string, vol int) match.Trade {
	return match.Trade{Instrument: simInst(t, sym), Direction: dir, Offset: off,
		Hedge: types.Speculation, Price: dec(px), Volume: vol}
}

func mark(t *testing.T, s *Simulator, sym, last, pre string) {
	t.Helper()
	markOn(t, s, simDay, sym, last, pre)
}

func markOn(t *testing.T, s *Simulator, day types.TradingDay, sym, last, pre string) {
	t.Helper()
	q := Quote{Instrument: simInst(t, sym), Last: dec(last), HasLast: true}
	if pre != "" {
		q.PreSettlement, q.HasPreSettlement = dec(pre), true
	}
	if err := s.Mark(day, q); err != nil {
		t.Fatal(err)
	}
}

// TestNewRequiresEveryChoice 钉住前七项口径缺哪一项都开不了，且一次报全（第八项零值合法，见 TestUndatedCloseChoiceZeroValue）。
func TestNewRequiresEveryChoice(t *testing.T) {
	rules := simRules(t)
	_, err := New(Config{Day: simDay, PreBalance: dec("1"), Rules: rules})
	if err == nil {
		t.Fatal("⚠️ 零值口径开户成功了")
	}
	for _, name := range []string{"FeeBasis", "FeeRounding", "MarginBasis", "SideScope", "Mark", "Algorithm", "FreezeMargin"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("零值口径的报错里没点名 %s —— 要一次报全：%v", name, err)
		}
	}
	if _, err := New(Config{Day: simDay, PreBalance: dec("1"), Rules: rules, Choices: ctpChoices()}); err != nil {
		t.Errorf("CTP 预设填上取整后应当开户成功：%v", err)
	}
	if _, err := New(Config{Day: simDay, PreBalance: dec("1"), Choices: ctpChoices()}); err == nil {
		t.Error("⚠️ 没有规则数据也开户成功了")
	}
}

// TestPresetsLeaveExactlyTheUnmeasuredCellsEmpty 钉住预设**只填实测过的格**。
//
// ⚠️ 两个方向：该空的空着（取整 §13 #5；快期的大边范围测不了），该填的填上 ——
// 否则「全部留空」也能过前一半，而那样预设就没有存在的理由。
func TestPresetsLeaveExactlyTheUnmeasuredCellsEmpty(t *testing.T) {
	for _, c := range []struct {
		name  string
		ch    Choices
		empty []string
	}{
		{"CTPChoices", CTPChoices(), []string{"FeeRounding"}},
		{"KQChoices", KQChoices(), []string{"FeeRounding", "SideScope"}},
	} {
		err := c.ch.validate()
		if err == nil {
			t.Errorf("⚠️ %s 通过了校验 —— 取整没实测，预设不许填它", c.name)
			continue
		}
		got := strings.Count(err.Error(), "未指定")
		if got != len(c.empty) {
			t.Errorf("%s 未指定 %d 项，期望恰好 %d 项 %v：%v", c.name, got, len(c.empty), c.empty, err)
		}
		for _, e := range c.empty {
			if !strings.Contains(err.Error(), e) {
				t.Errorf("%s 应当留空 %s：%v", c.name, e, err)
			}
		}
	}
	// 两个口子量到相反值的三项，两个预设必须不同 —— 抄成一样的预设会让「选口子」失去意义。
	ctp, kq := CTPChoices(), KQChoices()
	if ctp.FeeBasis == kq.FeeBasis || ctp.MarginBasis == kq.MarginBasis || ctp.Algorithm == kq.Algorithm || ctp.FreezeMargin == kq.FreezeMargin {
		t.Errorf("⚠️ 两个预设在实测相反的三项上有相同的：%+v / %+v", ctp, kq)
	}
	if ctp.Algorithm != account.AlgorithmOnlyLost || ctp.MarginBasis != margin.OpenTodayPreSettleHistory ||
		ctp.SideScope != margin.ByProduct || ctp.FeeBasis != fee.TradePrice || ctp.FreezeMargin != order.FreezeAtOrderPrice {
		t.Errorf("CTP 预设与 §13 #1/#3/#17、手续费基准实测不符：%+v", ctp)
	}
	// 第八项零值合法，上面的「未指定」计数数不到它 —— 单独钉：CTP 留空（simnow_pending#1），快期填平昨（kq_facts 32）
	if ctp.UndatedCloseOnUseHistory != UndatedCloseUnmeasured || kq.UndatedCloseOnUseHistory != UndatedCloseAsYesterday {
		t.Errorf("⚠️ UseHistory 上裸 CLOSE 的口径：CTP %v（应留空）/ 快期 %v（应为平昨）", ctp.UndatedCloseOnUseHistory, kq.UndatedCloseOnUseHistory)
	}
}

func wantAccount(t *testing.T, s *Simulator, step, balance, avail, margin, closeProfit, commission, pp string) {
	t.Helper()
	a := s.Account()
	for _, f := range []struct {
		name      string
		got       decimal.Decimal
		wantValue string
	}{
		{"结存", a.Balance, balance}, {"可用", a.Available, avail}, {"占用", a.CurrMargin, margin},
		{"平仓盈亏", a.CloseProfit, closeProfit}, {"手续费", a.Commission, commission}, {"持仓盈亏", a.PositionProfit, pp},
	} {
		if !f.got.Equal(dec(f.wantValue)) {
			t.Errorf("%s 之后%s = %s，期望 %s", step, f.name, f.got, f.wantValue)
		}
	}
}

// TestApplyTradeChain 手算一串成交，逐项核账户。
//
// ⚠️ 用的是 CTP 预设：今仓保证金按开仓价、浮盈不计入可用 —— 第一步浮盈 +20 时可用恰好少 20。
func TestApplyTradeChain(t *testing.T) {
	s := newSim(t)
	mark(t, s, "DCE.m2701", "3361", "3384")

	// 开多 2 @3360：手续费 1.5×2；占用 3360×10×2×0.1；持仓盈亏 (3361−3360)×10×2
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3360", 2)); err != nil {
		t.Fatal(err)
	}
	wantAccount(t, s, "开多 2", "100017", "93277", "6720", "0", "3", "20")

	mark(t, s, "DCE.m2701", "3350", "")
	wantAccount(t, s, "最新价到 3350", "99797", "93077", "6720", "0", "3", "-200")

	// 平今 1 @3355：平今档 0.75；平仓盈亏 (3355−3360)×10
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Sell, types.CloseToday, "3355", 1)); err != nil {
		t.Fatal(err)
	}
	wantAccount(t, s, "平今 1", "99846.25", "96486.25", "3360", "-50", "3.75", "-100")

	// 裸 CLOSE 1 @3352（NoUseHistory，按先平昨；账上只有今仓）：
	// ⚠️ 手续费按实际消耗拆档 ⇒ 消耗的是今仓 ⇒ 平今档 0.75，不是平仓档 1.2
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Sell, types.Close, "3352", 1)); err != nil {
		t.Fatal(err)
	}
	wantAccount(t, s, "裸平 1", "99865.5", "99865.5", "0", "-130", "4.5", "0")
}

// withHistory 开一个模拟器，在 simDay 开多 1 @3399、按 3384 结算，停在 simNext、最新价 3360。
//
// 形状照 #4 夹具：DCE.m2701 交易日 20260914 开 @3399、结算 3384，次日最新价 3360。
func withHistory(t *testing.T) *Simulator {
	t.Helper()
	s := newSim(t)
	mark(t, s, "DCE.m2701", "3400", "3422")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3399", 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(simDay, settlePx(t, "DCE.m2701", "3384"), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, "DCE.m2701", "3360", "3384")
	return s
}

// withHistoryOn 在 simDay 开多 1 @px、按 px 结算，停在 simNext、计价价 px —— 账上只有 1 手昨仓。
func withHistoryOn(t *testing.T, sym, px string) *Simulator {
	t.Helper()
	s := newSim(t)
	mark(t, s, sym, px, px)
	if err := s.ApplyTrade(simDay, trade(t, sym, types.Buy, types.Open, px, 1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Settle(simDay, settlePx(t, sym, px), simNext); err != nil {
		t.Fatal(err)
	}
	markOn(t, s, simNext, sym, px, px)
	return s
}

// TestUndatedCloseFeeTier 钉住 §13 #21：裸 CLOSE 消耗昨仓时的手续费档。
//
// 合成费率与 DCE.m2701 的声明同形（平昨 1.2 / 平今 0.75，两档不同），读数照 ctp-slices-20260915-{2,3}：
// 今 1 + 昨 1、裸平 1 手 ⇒ 按先平昨消耗了昨仓，而收**平今档**。
// ⚠️ 它钉的是本库在收敛前的做法（a、b 一致的那一段），不是规则本身：候选 c（行为平昨费率 ≠ 声明）未排除。
// 昨仓由真结算造出（F2 之前是白盒塞进去的）。
func TestUndatedCloseFeeTier(t *testing.T) {
	s := withHistory(t)
	if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	before := s.Account().Commission // 开仓 1.5
	if err := s.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Sell, types.Close, "3360", 1)); err != nil {
		t.Fatalf("今1昨1裸平1：%v", err)
	}
	if got := s.Account().Commission.Sub(before); !got.Equal(dec("0.75")) {
		t.Errorf("⚠️ 今1昨1裸平1 收了 %s，期望平今档 0.75（按消耗拆档会收平昨档 1.2 —— 已被 #4 夹具否掉）", got)
	}
	if got := s.Account().CloseProfit; !got.Equal(dec("-240")) {
		t.Errorf("前提：先平昨消耗昨仓，平仓盈亏 (3360−3384)×10 = −240，得到 %s", got)
	}
	p, _ := s.Position(simInst(t, "DCE.m2701"), types.Speculation)
	if p.VolumeHistory(types.Buy) != 0 || p.VolumeToday(types.Buy) != 1 {
		t.Errorf("前提：先平昨应消耗昨仓，剩今 1，得到 今 %d / 昨 %d", p.VolumeToday(types.Buy), p.VolumeHistory(types.Buy))
	}

	// 只有昨仓、两档费率不同 ⇒ §13 #21 当时收敛到 (a)：平仓前今仓 0 手 ⇒ **全走平昨档**。
	// ⚠️ (a) 已被 §13 #23 推翻为 (f)；这一格 (f) 同值（当日开仓 0 ⇒ 额度 0 ⇒ 平昨档），期望不变。
	// 实测：20260917 夜盘 E1，DCE.m2701 今0昨2 裸平1，柜台收 0.2 = 平昨档（ctp-slices-20260917{,-2}）。
	// ⚠️ 收敛之前这一格钉的是「报 #21、状态不动」—— 那是「不猜」，不是答案。
	s2 := withHistory(t)
	before2 := s2.Account().Commission
	if err := s2.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Sell, types.Close, "3360", 1)); err != nil {
		t.Fatalf("⚠️ 只有昨仓时裸平 1 手，§13 #21 收敛之后应当照收平昨档而不是报错：%v", err)
	}
	if got := s2.Account().Commission.Sub(before2); !got.Equal(dec("1.2")) {
		t.Errorf("⚠️ 只有昨仓裸平 1 收了 %s，期望平昨档 1.2 —— 候选 (b)「一律平今」会收 0.75，已被 E1 否掉", got)
	}
	if p, _ := s2.Position(simInst(t, "DCE.m2701"), types.Speculation); p.VolumeHistory(types.Buy) != 0 {
		t.Error("⚠️ 裸平之后昨仓没被消耗")
	}

	// ⚠️ 范围的另一个方向：(a) **只认大商所**（使用者裁决，确认在评审方一侧取得）。
	// 郑商所 MA2701 两档不同（平昨 2 / 平今 6），只有昨仓时裸平 ⇒ 这一段没有观测 ⇒ 照旧**报错不猜**、状态不动。
	// ⚠️ 这一格若被「顺手」按 (a) 收了平昨 2，所有测试照样可以绿 —— 而那是把一手 m2701 的观测外推到了另一个交易所。
	sc := withHistoryOn(t, "CZCE.MA2701", "3000")
	beforeC := sc.Account()
	errC := sc.ApplyTrade(simNext, trade(t, "CZCE.MA2701", types.Sell, types.Close, "3000", 1))
	if errC == nil || !strings.Contains(errC.Error(), "只在大商所有观测") {
		t.Errorf("⚠️ 郑商所只有昨仓时裸平、两档费率不同，要照旧报错不猜（§13 #21 的 (a) 只认大商所）：%v", errC)
	}
	if !sameSnapshot(sc.Account(), beforeC) {
		t.Error("⚠️ 郑商所那一笔报错之后账户变了")
	}
	if p, _ := sc.Position(simInst(t, "CZCE.MA2701"), types.Speculation); p.VolumeHistory(types.Buy) != 1 {
		t.Error("⚠️ 郑商所那一笔报错之后昨仓被消耗了")
	}

	// ⚠️⚠️ 郑商所 X0 的形状（今1昨0 裸平1，平的是今仓）：柜台实收**平昨档**（20260918 实测，§13 #23），
	// 而「min(平仓量, 平仓前今仓量) 走平今」会收平今档 6。此前本库就是这么收的 —— 既不报错也不对。
	// ⇒ 没有观测的交易所上两档不同时**整笔**报错不猜，不只是超出今仓那一段。
	sz := newSim(t)
	markOn(t, sz, simDay, "CZCE.MA2701", "3000", "3000")
	if err := sz.ApplyTrade(simDay, trade(t, "CZCE.MA2701", types.Buy, types.Open, "3000", 1)); err != nil {
		t.Fatal(err)
	}
	beforeZ := sz.Account()
	errZ := sz.ApplyTrade(simDay, trade(t, "CZCE.MA2701", types.Sell, types.Close, "3000", 1))
	if errZ == nil || !strings.Contains(errZ.Error(), "§13 #23") {
		t.Errorf("⚠️ 郑商所今1昨0 裸平1、两档不同：要整笔报错不猜（实测收平昨、按平仓前今仓会收平今），得到 %v", errZ)
	}
	if !sameSnapshot(sz.Account(), beforeZ) {
		t.Error("⚠️ 郑商所 X0 形状报错之后账户变了 —— 多半是先按平今收了一段")
	}

	// 反向：显式平昨照走平昨档
	s3 := withHistory(t)
	if err := s3.ApplyTrade(simNext, trade(t, "DCE.m2701", types.Sell, types.CloseYesterday, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	if got := s3.Account().Commission; !got.Equal(dec("1.2")) {
		t.Errorf("显式平昨应收平昨档 1.2，得到 %s", got)
	}

	// 两档声明费率相同（y2701，平昨 = 平今 = 1.1）⇒ 超出今仓的部分 a、b 同值 ⇒ 放行并**照收**平昨档（评审 20260915：这一支此前没人守，改成不收费全绿）
	// 只有昨仓、裸平 1 手 ⇒ 1 手平昨档 1.1
	sy := withHistoryOn(t, "DCE.y2701", "8000")
	if err := sy.ApplyTrade(simNext, trade(t, "DCE.y2701", types.Sell, types.Close, "8000", 1)); err != nil {
		t.Fatalf("两档同费率、只有昨仓裸平1：%v", err)
	}
	if got := sy.Account().Commission; !got.Equal(dec("1.1")) || !got.IsPositive() {
		t.Errorf("⚠️ 两档同费率、只有昨仓裸平1 收了 %s，期望 1.1（少收不报错是这一支坏掉的样子）", got)
	}
	// 今 1 昨 1、裸平 2 手 ⇒ 平今 1 手 + 平昨 1 手 = 2.2（开今那一手另收 2.5）
	sm := withHistoryOn(t, "DCE.y2701", "8000")
	if err := sm.ApplyTrade(simNext, trade(t, "DCE.y2701", types.Buy, types.Open, "8000", 1)); err != nil {
		t.Fatal(err)
	}
	if err := sm.ApplyTrade(simNext, trade(t, "DCE.y2701", types.Sell, types.Close, "8000", 2)); err != nil {
		t.Fatalf("两档同费率、今1昨1裸平2：%v", err)
	}
	if got := sm.Account().Commission.Sub(dec("2.5")); !got.Equal(dec("2.2")) || !got.IsPositive() {
		t.Errorf("⚠️ 两档同费率、今1昨1裸平2 收了 %s，期望 平今 1.1 + 平昨 1.1 = 2.2", got)
	}
}

// sameSnapshot 按值比两份账户快照。⚠️ 不能用 ==：decimal 内部带指针，== 比的是指针，同值也不等。
func sameSnapshot(a, b account.Snapshot) bool { return fmt.Sprintf("%+v", a) == fmt.Sprintf("%+v", b) }

// TestFailedApplyTradeLeavesNoTrace 钉住一笔失败的成交之后，持仓与账户逐字段与之前相同。
func TestFailedApplyTradeLeavesNoTrace(t *testing.T) {
	s := newSim(t)
	mark(t, s, "DCE.m2701", "3361", "3384")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)); err != nil {
		t.Fatal(err)
	}
	before := s.Account()
	m := simInst(t, "DCE.m2701")
	pBefore, _ := s.Position(m, types.Speculation)

	cases := []struct {
		name string
		tr   match.Trade
		want string
	}{
		// ag2702 从没 Mark 过：开仓本身能做，而重算截面缺计价价 —— 那一步在换进去之前
		{"缺计价价", trade(t, "SHFE.ag2702", types.Buy, types.Open, "15457", 1), "先 Mark"},
		{"超量平今", trade(t, "DCE.m2701", types.Sell, types.CloseToday, "3360", 2), "超过"},
		{"不存在的合约", trade(t, "DCE.i2701", types.Buy, types.Open, "700", 1), ""},
	}
	for _, c := range cases {
		err := s.ApplyTrade(simDay, c.tr)
		if err == nil {
			t.Errorf("%s：本该失败", c.name)
			continue
		}
		if c.want != "" && !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：报错要含「%s」：%v", c.name, c.want, err)
		}
		if after := s.Account(); !sameSnapshot(after, before) {
			t.Errorf("⚠️ %s 失败之后账户变了：\n前 %+v\n后 %+v", c.name, before, after)
		}
		p, _ := s.Position(m, types.Speculation)
		if p.VolumeToday(types.Buy) != pBefore.VolumeToday(types.Buy) {
			t.Errorf("⚠️ %s 失败之后 m2701 持仓变了", c.name)
		}
		if _, ok := s.Position(simInst(t, "SHFE.ag2702"), types.Speculation); ok {
			t.Errorf("⚠️ %s 失败之后多出一个 ag2702 持仓", c.name)
		}
	}

	// UseHistory 上的裸 CLOSE：没有实测的消耗顺序（simnow_pending#1）⇒ 报错，不猜
	mark(t, s, "SHFE.ag2702", "15457", "15785")
	if err := s.ApplyTrade(simDay, trade(t, "SHFE.ag2702", types.Buy, types.Open, "15457", 1)); err != nil {
		t.Fatal(err)
	}
	before = s.Account()
	err := s.ApplyTrade(simDay, trade(t, "SHFE.ag2702", types.Sell, types.Close, "15460", 1))
	if err == nil || !strings.Contains(err.Error(), "没有实测的消耗顺序") {
		t.Errorf("⚠️ UseHistory 上裸 CLOSE 要报「没有实测的消耗顺序」：%v", err)
	}
	if !sameSnapshot(s.Account(), before) {
		t.Error("⚠️ 裸 CLOSE 被拒之后账户变了")
	}
}

// TestMarkRefusesChangedPreSettlementAndZero 钉住计价输入的两道关。
func TestMarkRefusesChangedPreSettlementAndZero(t *testing.T) {
	s := newSim(t)
	m := simInst(t, "DCE.m2701")
	mark(t, s, "DCE.m2701", "3361", "3384")
	if err := s.Mark(simDay, Quote{Instrument: m, PreSettlement: dec("3385"), HasPreSettlement: true}); err == nil {
		t.Error("⚠️ 同一交易日里昨结算价从 3384 变成 3385，没有报错")
	}
	if err := s.Mark(simDay, Quote{Instrument: m, Last: decimal.Zero, HasLast: true}); err == nil {
		t.Error("⚠️ 最新价 0 被接受了 —— 零只可能是缺失的伪装")
	}
	// 反向：同值重复给昨结算价放行
	if err := s.Mark(simDay, Quote{Instrument: m, PreSettlement: dec("3384"), HasPreSettlement: true}); err != nil {
		t.Errorf("同值重复给昨结算价应放行：%v", err)
	}
}

// TestOtherDayAndBrokenAreRefused 钉住交易日守卫与失效态。
//
// ⚠️ 失效态在有效输入下**触发不了**（写账户那几步的前置条件都在换进去之前查过），
// 所以这里直接置位 —— 验的是「失效之后每个动词都拒绝」，不是「什么情况会失效」。
func TestOtherDayAndBrokenAreRefused(t *testing.T) {
	s := newSim(t)
	if err := s.Deposit(simDay+1, dec("1")); err == nil {
		t.Error("⚠️ 别的交易日的入金被接受了")
	}
	s.broken = errors.New("合成的写入失败")
	for name, err := range map[string]error{
		"入金":   s.Deposit(simDay, dec("1")),
		"出金":   s.Withdraw(simDay, dec("1")),
		"Mark": s.Mark(simDay, Quote{Instrument: simInst(t, "DCE.m2701"), Last: dec("1"), HasLast: true}),
		"成交":   s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3360", 1)),
	} {
		if err == nil || !strings.Contains(err.Error(), "失效") {
			t.Errorf("⚠️ 失效之后%s没有拒绝：%v", name, err)
		}
	}
}

// TestPositionReturnsACopy 钉住查询拿到的是副本。
func TestPositionReturnsACopy(t *testing.T) {
	s := newSim(t)
	mark(t, s, "DCE.m2701", "3361", "3384")
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.Open, "3360", 2)); err != nil {
		t.Fatal(err)
	}
	m := simInst(t, "DCE.m2701")
	p, _ := s.Position(m, types.Speculation)
	if _, err := p.Close(types.Buy, types.CloseToday, simDay, 2, 0); err != nil {
		t.Fatal(err)
	}
	if q, _ := s.Position(m, types.Speculation); q.VolumeToday(types.Buy) != 2 {
		t.Errorf("⚠️ 改了查询拿到的持仓，模拟器里的变成 %d 手", q.VolumeToday(types.Buy))
	}
}

// TestChoicesReachTheLedger 钉住按金额收费、保证金基准、浮盈算法三项口径真的传到了账上。
//
// ⚠️ 链条测试用的是按手数收费的合约 —— 手续费基准换成什么都不影响它。
// 这里用按金额收费的 ag2702，在同一笔成交上比 CTP 与快期两套口径，三项都要分开。
func TestChoicesReachTheLedger(t *testing.T) {
	run := func(ch Choices) account.Snapshot {
		t.Helper()
		ch.FeeRounding = fee.NoRounding
		ch.SideScope = margin.ByInstrument
		s, err := New(Config{Day: simDay, PreBalance: dec("1000000"), Rules: simRules(t), Choices: ch})
		if err != nil {
			t.Fatal(err)
		}
		mark(t, s, "SHFE.ag2702", "15460", "15785")
		if err := s.ApplyTrade(simDay, trade(t, "SHFE.ag2702", types.Buy, types.Open, "15457", 1)); err != nil {
			t.Fatal(err)
		}
		return s.Account()
	}
	ctp, kq := run(CTPChoices()), run(KQChoices())
	// 手续费：CTP 按成交价 15457×15×0.00001；快期按昨结算 15785×15×0.00001
	if !ctp.Commission.Equal(dec("2.31855")) || !kq.Commission.Equal(dec("2.36775")) {
		t.Errorf("⚠️ 手续费基准没传到：CTP %s（期望 2.31855）/ 快期 %s（期望 2.36775）", ctp.Commission, kq.Commission)
	}
	// 保证金：CTP 今仓按开仓价 15457×15×0.19；快期按昨结算 15785×15×0.19
	if !ctp.CurrMargin.Equal(dec("44052.45")) || !kq.CurrMargin.Equal(dec("44987.25")) {
		t.Errorf("⚠️ 保证金基准没传到：CTP %s（期望 44052.45）/ 快期 %s（期望 44987.25）", ctp.CurrMargin, kq.CurrMargin)
	}
	// 浮盈 (15460−15457)×15 = 45：CTP 不计入可用，快期计入
	ctpGap := ctp.Balance.Sub(ctp.CurrMargin).Sub(ctp.Available)
	kqGap := kq.Balance.Sub(kq.CurrMargin).Sub(kq.Available)
	if !ctpGap.Equal(dec("45")) || !kqGap.IsZero() {
		t.Errorf("⚠️ 浮盈算法没传到：结存−占用−可用 CTP %s（期望 45）/ 快期 %s（期望 0）", ctpGap, kqGap)
	}
}

// TestApplyTradeShortSide 手算一段空头：开空、行情上涨、买入平今。
//
// ⚠️ 两条对拍（快期 Rebuild、CTP ag2702）里的持仓都只有多头 —— 方向写反在那里一格都不会红。
func TestApplyTradeShortSide(t *testing.T) {
	s := newSim(t)
	mark(t, s, "DCE.m2701", "3361", "3384")

	// 开空 2 @3360：手续费 1.5×2；占用 3360×10×2×0.1；持仓盈亏 (3360−3361)×10×2 = −20（浮亏，两种算法都扣）
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Sell, types.Open, "3360", 2)); err != nil {
		t.Fatal(err)
	}
	wantAccount(t, s, "开空 2", "99977", "93257", "6720", "0", "3", "-20")

	// 最新价跌到 3340：空头浮盈 (3360−3340)×10×2 = +400 —— CTP 预设下不计入可用
	mark(t, s, "DCE.m2701", "3340", "")
	wantAccount(t, s, "最新价到 3340", "100397", "93277", "6720", "0", "3", "400")

	// 买入平今 1 @3345：平今档 0.75；平仓盈亏 (3360−3345)×10 = +150；剩 1 手浮盈 (3360−3340)×10 = 200
	if err := s.ApplyTrade(simDay, trade(t, "DCE.m2701", types.Buy, types.CloseToday, "3345", 1)); err != nil {
		t.Fatalf("买平今 1：%v", err)
	}
	wantAccount(t, s, "买平今 1", "100346.25", "96786.25", "3360", "150", "3.75", "200")

	// 买入平仓平的是空头：多头一直是 0
	p, _ := s.Position(simInst(t, "DCE.m2701"), types.Speculation)
	if p.VolumeToday(types.Buy) != 0 || p.VolumeToday(types.Sell) != 1 {
		t.Errorf("⚠️ 买平今之后多 %d / 空 %d，期望 0 / 1", p.VolumeToday(types.Buy), p.VolumeToday(types.Sell))
	}
}
