package fixture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// exchangeDaily 是**交易所**给的一天行情，与柜台无关。
type exchangeDaily struct {
	Settlement    decimal.Decimal
	PreSettlement decimal.Decimal
	Close         decimal.Decimal
	HasClose      bool
}

func loadExchangeDaily(t *testing.T, file string) map[string]exchangeDaily {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "refdata", file))
	if err != nil {
		t.Skipf("没有 %s，跳过：%v", file, err)
	}
	var raw struct {
		Settlements []struct {
			Instrument    string `json:"instrument"`
			Settlement    string `json:"settlement"`
			PreSettlement string `json:"pre_settlement"`
			Close         string `json:"close"`
		} `json:"settlements"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	out := map[string]exchangeDaily{}
	for _, r := range raw.Settlements {
		d := exchangeDaily{Settlement: decimal.RequireFromString(r.Settlement)}
		if r.PreSettlement != "" {
			d.PreSettlement = decimal.RequireFromString(r.PreSettlement)
		}
		if r.Close != "" {
			d.Close, d.HasClose = decimal.RequireFromString(r.Close), true
		}
		out[r.Instrument] = d
	}
	return out
}

// TestPositionPriceIsExchangeClose 用**交易所**的数核对柜台的逐日盯市基线。
//
// ⚠️ 这条是本仓库第二条有**独立来源**的核对（第一条是 kq_facts 20）。
// 在它之前，「柜台的 position_price 3177 是收盘价」这句话
// 只有柜台自己的数据支撑 —— 拿柜台的数去解释柜台自己的基线是同义反复。
// 上期所的日行情里一直有 CLOSEPRICE，只是 ParseSHFE 把它丢了。
//
// # 结论（20260908 → 20260909，SHFE.rb2701）
//
//	交易所：收盘 3177、结算 3163、昨结算 3158 —— 三个数各不相同，样本有判别力
//	柜台：  纯昨仓时 position_price == 3177 == **收盘价**
//	本库：  逐日盯市基线取 3163 == **结算价**（cn-futures-rules.md §5）
//
// ⚠️ 所以这是一条**已知口子差异**，而且现在它的两端各有独立出处。
// 本库不改：中国期货的资金结算走结算价，那是规则；
// 快期模拟用收盘价，那是这个口子的实现。
func TestPositionPriceIsExchangeClose(t *testing.T) {
	ex := loadExchangeDaily(t, "shfe-kx20260908.json")
	const sym = "SHFE.rb2701"
	// ⚠️ 文件在而合约不在，是**失败**不是跳过。
	// 第一版写的是 t.Skip，于是键名写错（用了 "rb2701" 而实际是 "SHFE.rb2701"）
	// 的时候它静默跳过、整条测试报 PASS —— 一条跳过的测试与一条不存在的测试
	// 在汇总行里长得一模一样。文件本身缺席才跳过，那是另一回事。
	d, ok := ex[sym]
	if !ok {
		keys := make([]string, 0, 4)
		for k := range ex {
			keys = append(keys, k)
			if len(keys) == 4 {
				break
			}
		}
		t.Fatalf("⚠️ 20260908 的日行情里没有 %s（共 %d 个合约，例如 %v）—— "+
			"文件在而合约不在，多半是键名对不上；**不跳过**", sym, len(ex), keys)
	}
	if !d.HasClose {
		t.Fatal("⚠️ 交易所日行情里没有收盘价 —— " +
			"本条的整个理由就是「有个独立来源」，没有它就退回同义反复了。" +
			"重新跑 cmd/settlement 生成夹具")
	}
	// ⚠️ 判别力：三个价必须互不相同。相等的那一天，
	// 「用的是收盘价」与「用的是结算价」给出同一个数，本条什么都没证明。
	if d.Close.Equal(d.Settlement) {
		t.Fatalf("⚠️ 收盘价与结算价都是 %s —— 这一天分不开它们，本条在空转", d.Close)
	}

	all := loadAll(t)
	checked, mixed, skippedClosed := 0, 0, 0
	for _, f := range all {
		if f.TradingDay.String() != "20260909" {
			continue
		}
		pos, ok := f.Positions[sym]
		if !ok {
			continue
		}
		today := numOr(pos, "volume_long_today")
		his := numOr(pos, "volume_long_his")
		if his.IsZero() {
			continue
		}
		pp, ok := numberOf(pos, "position_price_long")
		if !ok {
			continue
		}
		if today.IsZero() {
			// —— 纯昨仓：基线应当**就是**交易所的收盘价 ——
			checked++
			if !pp.Equal(d.Close) {
				t.Errorf("⚠️ %s：纯昨仓的 position_price = %s，"+
					"而交易所收盘价 = %s（结算价 %s）—— "+
					"「柜台用收盘价」这条结论要重查", f.Path, pp, d.Close, d.Settlement)
			}
			if pp.Equal(d.Settlement) {
				t.Errorf("⚠️ %s：position_price 等于**结算价** %s —— "+
					"那与已登记的口子差异相反，去查是不是柜台改了口径",
					f.Path, d.Settlement)
			}
			continue
		}

		// —— 今昨并存：基线是**加权平均**，昨仓用收盘价、今仓用开仓价 ——
		//
		// ⚠️ 这一支才是真正把「基线怎么组合」钉死的：纯昨仓时
		// 「整体用收盘价」与「昨仓那部分用收盘价」给出同一个数，分不开。
		op, ok := numberOf(pos, "open_price_long")
		if !ok {
			continue
		}
		// ⚠️ 多头这边发生过平仓就不查组合式了，**并且记数**。
		//
		// 理由是实测出来的：平仓**按持仓均价冲减成本**，于是 position_price
		// 一动不动，而「昨仓×收盘 + 今仓×开仓」那个式子算的是**建仓时**的组合，
		// 两者在平过仓之后必然对不上。
		// 实测 fee-close-history：平昨 1 手后手数 4→3，而 position_price
		// 仍是 3174.25、position_cost = 3174.25×3×10 = 95227.5。
		// 那条性质由下面的 TestCloseReducesCostAtAverage 单独断言，不混进这里。
		if longClosed(f, sym) {
			skippedClosed++
			continue
		}
		total := today.Add(his)
		// ⚠️ open_price 是**全部**持仓的加权开仓均价，不是今仓那部分的。
		// 今仓那部分的开仓价要从成交里来，这里用夹具的成交反推。
		todayOpen, ok := todayOpenPrice(f, sym)
		if !ok {
			t.Logf("ⓘ %s：今昨并存但取不到今仓的开仓价，跳过加权那一支", f.Path)
			continue
		}
		mixed++
		want := his.Mul(d.Close).Add(today.Mul(todayOpen)).Div(total)
		if pp.Sub(want).Abs().GreaterThan(decimal.RequireFromString("0.0000001")) {
			t.Errorf("⚠️ %s：今%s/昨%s 的 position_price = %s，"+
				"而「昨仓×收盘 %s + 今仓×开仓 %s」÷ 总手数 = %s —— "+
				"⚠️ 基线的组合方式与已登记的不符（open_price_long=%s）",
				f.Path, today, his, pp, d.Close, todayOpen, want, op)
		}
		checked++
	}

	if checked == 0 {
		t.Fatal("⚠️ 一个样本都没比到 —— 本条在空转。" +
			"要一份 20260909 的、rb2701 有昨仓的夹具")
	}
	if mixed == 0 {
		t.Error("⚠️ 没有一个**今昨并存**的样本 —— " +
			"纯昨仓时「整体用收盘价」与「昨仓那部分用收盘价」给出同一个数，" +
			"基线**怎么组合**这件事此时一点没被验到")
	}
	t.Logf("交易所 20260908 rb2701：收盘 %s / 结算 %s / 昨结算 %s（三者互不相同）",
		d.Close, d.Settlement, d.PreSettlement)
	t.Logf("比了 %d 个截面，其中今昨并存的 %d 个；因多头平过仓而跳过组合式的 %d 个",
		checked, mixed, skippedClosed)
	t.Log("ⓘ 已登记的口子差异：柜台的逐日盯市基线用**收盘价**，" +
		"本库用**结算价**（cn-futures-rules.md §5）。本库不改 —— " +
		"两端现在各有独立出处")
}

// todayOpenPrice 从夹具的成交里反推**多头今仓那部分**的加权开仓价。
//
// 只数「买开」，其余成交（卖开的空头腿、平仓）与多头今仓无关。
//
// ⚠️ 判据落在最后那一句：买开的手数**必须正好等于** volume_long_today。
// 不等就不给值 —— 那说明有一笔平仓消耗过今仓（或者夹具不自足），
// 而消耗了哪几手要靠重放，重放在有昨仓的合约上本来就凑不齐。
// 与其给一个凑出来的数，不如让上层报「这一支没验到」。
//
// ⚠️ 第一版要求**全部**成交都是买开，于是今晚那批夹具
// （里面还有卖开的空头腿与一笔平昨）一份都没通过，
// 「今昨并存」那一支一个样本都没有 —— 而它才是真正把基线组合钉死的那一支。
func todayOpenPrice(f *Fixture, sym string) (decimal.Decimal, bool) {
	sum, lots := decimal.Zero, 0
	for _, tr := range f.TradesOf(sym) {
		if tr.Offset != types.Open || tr.Direction != types.Buy {
			continue
		}
		sum = sum.Add(tr.Price.Mul(decimal.NewFromInt(int64(tr.Volume))))
		lots += tr.Volume
	}
	if lots == 0 {
		return decimal.Zero, false
	}
	if !numOr(f.Positions[sym], "volume_long_today").
		Equal(decimal.NewFromInt(int64(lots))) {
		return decimal.Zero, false
	}
	return sum.Div(decimal.NewFromInt(int64(lots))), true
}

func numOr(m map[string]Value, key string) decimal.Decimal {
	v, ok := numberOf(m, key)
	if !ok {
		return decimal.Zero
	}
	return v
}

// longClosed 报告这份夹具里多头方向有没有发生过平仓。
func longClosed(f *Fixture, sym string) bool {
	for _, tr := range f.TradesOf(sym) {
		if tr.Direction == types.Sell && tr.Offset != types.Open {
			return true
		}
	}
	return false
}

// TestCloseReducesCostAtAverage 断言**平仓按持仓均价冲减成本**，
// 于是 `position_price` 一动不动。
//
// ⚠️ 这条是 20260909 夜盘从一次「对不上」里挖出来的：
// 拿「昨仓×收盘 + 今仓×开仓」的组合式去核对平过仓的截面，差了 0.9166…。
// 差值不是噪声 —— 平仓之后柜台没有重算基线，而是按**当时的持仓均价**
// 把成本减掉了一手的量。
//
// # 为什么这条要紧
//
//	3174.25 × 3 × 10 = 95227.5   实测的 position_cost
//	(2×3177 + 1×3166) / 3 = 3173.3333…   若重算会得到的均价
//
// 两者差 0.9166…，在四位打印精度下**看得见**，但在只有一手的样本上会消失。
// ⚠️ 而本库的 `position` 若在平仓时用别的价冲减成本，
// 权益曲线会整条错开，且每一步的数看起来都完全正常。
func TestCloseReducesCostAtAverage(t *testing.T) {
	all := loadAll(t)
	const sym = "SHFE.rb2701"
	type snap struct {
		path  string
		vol   decimal.Decimal
		price decimal.Decimal
		cost  decimal.Decimal
	}
	var snaps []snap
	for _, f := range all {
		if f.TradingDay.String() != "20260909" {
			continue
		}
		pos, ok := f.Positions[sym]
		if !ok || !longClosed(f, sym) {
			continue
		}
		v := numOr(pos, "volume_long_today").Add(numOr(pos, "volume_long_his"))
		pp, ok1 := numberOf(pos, "position_price_long")
		pc, ok2 := numberOf(pos, "position_cost_long")
		if !ok1 || !ok2 || v.IsZero() {
			continue
		}
		snaps = append(snaps, snap{f.Path, v, pp, pc})
	}
	if len(snaps) == 0 {
		// ⚠️⚠️ **这里原来是 `t.Skip`，20260912 评审提出来的。**
		//
		// 它与同一批刚改成双向断言的那个情形（INE 拒单语料「待拍」）**同形** ——
		// 都是「等样本」，而同一批里给了两种处置。
		//
		//	⚠️ 而 skip 的问题不是不一致，是**它看不见**：
		//	`go test ./...` 对有 skip 的包照样打 `ok`
		//	（评审 20260909 因此把一次「17 个包全绿」报少了一条没跑的测试）。
		//
		// ⇒ 改成与那边同一条处置：**没样本时，要求清单里写着它在等**。
		// 于是「这条在等样本」有一处可见的对应物，而不是一条沉默的绿。
		//
		// ⚠️ 而它等的样本**恰好是 `rules_pending` #13 要的形状**
		//（多头平过仓且仍有持仓），且它断言的是
		// `position_cost = position_price × 手数 × 乘数` —— **均价形状**，
		// 而这是**快期**那一侧。⇒ 样本到了之后它可能与 CTP 侧的结论相关，
		// **甚至相反**：一边按均价冲减、一边逐片，两个柜台各自成立是完全可能的。
		// ⚠️ 那时要改的是**本库能不能只有一种实现**，不是把两个观测凑成一个。
		if !declaresPendingSample(t) {
			t.Fatalf("⚠️ 没有「多头平过仓且仍有持仓」的截面，"+
				"**而 state.md 的清单里也没写它在等** —— 两头都不说，"+
				"这条待办就没有任何可见的对应物了。⇒ 在 `simnow_pending` 里写上 %q",
				pendingSampleMarker)
		}
		t.Logf("ⓘ 无样本（本条此刻只在核对清单里那句声明，**没有在守那条恒等式**）；" +
			"⚠️ 它等的样本正是 #13 要的形状，而本条断言的是**均价**形状、" +
			"且在**快期**那一侧 —— 到时要防的是把两个柜台的观测凑成一个")
		return
	}

	mult := decimal.NewFromInt(10) // SHFE.rb 的乘数
	for _, s := range snaps {
		want := s.price.Mul(s.vol).Mul(mult)
		if !s.cost.Equal(want) {
			t.Errorf("⚠️ %s：position_cost = %s，而 position_price × 手数 × 乘数 "+
				"= %s × %s × %s = %s —— "+
				"「平仓按持仓均价冲减」这条结论要重查",
				s.path, s.cost, s.price, s.vol, mult, want)
		}
	}

	// ⚠️ 判别力：还得有一份**平仓之前**的截面，且均价与之相同。
	// 只看平仓后那一张，「cost = price × vol × mult」是个恒等式，恒真。
	var before *snap
	for _, f := range all {
		if f.TradingDay.String() != "20260909" || longClosed(f, sym) {
			continue
		}
		pos, ok := f.Positions[sym]
		if !ok {
			continue
		}
		v := numOr(pos, "volume_long_today").Add(numOr(pos, "volume_long_his"))
		pp, ok1 := numberOf(pos, "position_price_long")
		if !ok1 || v.LessThanOrEqual(snaps[0].vol) {
			continue // 要一张**手数更多**的，才说明中间确实平掉过
		}
		before = &snap{f.Path, v, pp, decimal.Zero}
		break
	}
	if before == nil {
		t.Fatal("⚠️ 找不到一份「平仓之前、手数更多」的截面 —— " +
			"只看平仓后那一张的话，cost = price × vol × mult 是恒等式，本条恒真")
	}
	if !before.price.Equal(snaps[0].price) {
		t.Errorf("⚠️ 平仓前均价 %s（%s手）→ 平仓后 %s（%s手）—— **均价变了**，"+
			"那说明不是按持仓均价冲减的，去重新量",
			before.price, before.vol, snaps[0].price, snaps[0].vol)
	}
	t.Logf("平仓前 %s：%s 手 @ %s；平仓后 %s：%s 手 @ %s —— 均价不变，"+
		"成本按均价冲减", before.path, before.vol, before.price,
		snaps[0].path, snaps[0].vol, snaps[0].price)
}

// pendingSampleMarker 是 `state.md` 里那句声明的锚。
//
// ⚠️ 用**一句话**当锚而不是一个小节标题：标题会被重排，
// 而这句话的**措辞本身**就是那条待办的内容。
const pendingSampleMarker = "多头平过仓且仍有持仓"

// declaresPendingSample 报告清单里有没有写着「这条在等样本」。
//
// ⚠️ 它读的是 `docs/state.md` —— 与 `TestRejectCorpusIsProbeWrittenOnly`
// 读 `cn-futures-rules.md` 同一条路子：**一个「在等」的状态，
// 必须在某一份人会读的清单里有对应物**，否则它只活在一条 skip 里。
func declaresPendingSample(t *testing.T) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "state.md"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b), pendingSampleMarker)
}
