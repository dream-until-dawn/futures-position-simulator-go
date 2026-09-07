package futsim

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

// ⚠️ 这条测试把 §8 的三条资金恒等式**对着每一份记录过的柜台截面**跑一遍。
//
// 起因是评审的一次重新框定牵出来的：CTP 的 `CThostFtdcBrokerTradingParamsField`
// 上有 `Algorithm`（盈亏算法，四个取值）与 `AvailIncludeCloseProfit`（两个取值），
// 它们直接决定浮盈亏与平仓盈利算不算进权益和可用。
//
// 我此前是**手算一个截面**得出「三条恒等式精确成立」的。手算一个截面的问题不在算错，
// 在于**没有人在统计判别力**：那个截面的 position_profit 恰好是 0，
// 而四个 Algorithm 取值在浮盈亏为 0 时给出同一个数。
//
// 所以本测试有两半，第二半才是重点：
//
//	① 恒等式在每一份夹具上都成立
//	② **报出这批夹具的判别力缺口**，并在缺口出现时让对应的断言明确「未覆盖」
//
// ⚠️ 顺带更正我自己写过的一句话：此前记的是「三条恒等式**精确**成立」。
// 那是在小数点后 4 位的打印精度下看到的。全精度下夹具里存着
// `commission: 126.95459999999999` 这种 float64 往返噪声，
// 恒等式成立于**表示误差之内**，不是字面精确。后者是更弱、也更准确的陈述。
// 三个常量把「容差为什么是这个数」写死，并由 TestToleranceOrderingIsAsserted 守住。
//
// ⚠️ 定容差不是「差不多就行」，它要同时卡两头：
//
//	下界：夹具里的数来自 float64 往返，1e6 量级上的表示误差约 1e-10，容差必须盖住它
//	上界：一笔真实的记账偏差最小是 minMeaningfulMoney，容差必须远小于它
//
// 全部以 minMeaningfulMoney 为基准表述，**不要再用「差几个数量级」的散文**：
// 此处曾同时写着「相隔五个数量级」（对 1e-4 说的）与「差七个数量级」（对 0.01 说的），
// 两句各自都对，并排读却像自相矛盾——而这段是整个容差论证的承重墙。
var (
	// minMeaningfulMoney 是一笔真实记账偏差的下限：一分钱的百分之一。
	minMeaningfulMoney = decimal.RequireFromString("0.0001")

	// moneyEps 是恒等式的判定容差。
	moneyEps = decimal.RequireFromString("0.000000001")

	// residualBackstop 是**第二道**闸：即便有人把 moneyEps 调松，
	// 落进容差内的残差仍会在这里被拦下。它不随 moneyEps 移动。
	residualBackstop = decimal.RequireFromString("0.000001")
)

// TestToleranceOrderingIsAsserted 把「不要把容差往上调」从**忠告**变成**断言**。
//
// ⚠️ 这条是评审提的，而它补上的正是我一次陈述失误暴露出来的空当：
// 我报告过「把容差调松一千万倍，残差守卫仍然会红」。评审照做，**全绿**。
// 差别在于我当时跑的夹具里**还留着上一次破坏注入的 0.001 偏差**而我没复位——
// 那句话在「存在真实偏差」的前提下为真，我却没把前提写出来。
//
// 更要紧的是那句话依赖**夹具状态**才成立，于是它不可复现。
// 现在这条断言不依赖任何夹具：谁把 moneyEps 调到接近真实金额，
// 干净仓库下就会红。**一个不依赖状态的断言，胜过一句需要前提的描述。**
func TestToleranceOrderingIsAsserted(t *testing.T) {
	// 容差与兜底阈值都必须比「最小有意义金额」小两个数量级以上。
	limit := minMeaningfulMoney.Div(decimal.NewFromInt(100))
	for _, c := range []struct {
		name string
		v    decimal.Decimal
	}{
		{"moneyEps", moneyEps},
		{"residualBackstop", residualBackstop},
	} {
		if c.v.GreaterThan(limit) {
			t.Errorf("⚠️ %s = %s，已经不比最小有意义金额 %s 小两个数量级（上限 %s）——"+
				"这个容差会开始吞掉真实的记账偏差。**该查账，不该调容差**",
				c.name, c.v, minMeaningfulMoney, limit)
		}
		if !c.v.IsPositive() {
			t.Errorf("⚠️ %s = %s 非正 —— 恒等式会变成字面相等，浮点噪声会让它永远红", c.name, c.v)
		}
	}

	// ⚠️ 两道闸必须来自不同的数：若 residualBackstop 跟着 moneyEps 走，
	// 调松容差就会同时调松兜底，第二道闸形同虚设。
	if residualBackstop.Equal(moneyEps) {
		t.Error("⚠️ 兜底阈值与判定容差同值 —— 那就只有一道闸了，" +
			"「两套不同原理的机制不会一起失效」这条在这里不成立")
	}
}

type acctSnap struct {
	Account map[string]any `json:"account"`
}

// num 取一个数值字段。
//
// ⚠️ 用 json.Number 而不是 float64：夹具里 12.07125 这类五位小数
// 一旦过一遍二进制浮点，恒等式就只能「约等于」了，而**约等于会把真实的
// 小额偏差和浮点噪声混在一起**——那正是本测试要分开的两样东西。
//
// ⚠️ 账户截面里并非每个字段都是数（`currency_id` 是 "CNY"），
// 所以按需取值、按需转换，不整块 unmarshal 成数字表。
func num(t *testing.T, a map[string]any, k string) decimal.Decimal {
	t.Helper()
	v, ok := a[k]
	if !ok {
		return decimal.Zero
	}
	n, ok := v.(json.Number)
	if !ok {
		t.Fatalf("字段 %s 的值 %#v 不是数", k, v)
	}
	d, err := decimal.NewFromString(n.String())
	if err != nil {
		t.Fatalf("字段 %s 的值 %q 转 decimal 失败：%v", k, n.String(), err)
	}
	return d
}

// TestAccountIdentitiesHoldOnEveryFixture 是 §8 三条恒等式的机械核对。
func TestAccountIdentitiesHoldOnEveryFixture(t *testing.T) {
	paths := fixturePaths(t)
	checked := 0
	maxResidual := decimal.Zero
	// 判别力计数：这批夹具**分别覆盖到了什么**。
	var withGain, withLoss, withPosClose, withFrozen, withDeposit, withWithdraw int

	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var s acctSnap
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber() // ⚠️ 不走 float64，见 num 的说明
		if err := dec.Decode(&s); err != nil {
			t.Errorf("%s 解析失败：%v", p, err)
			continue
		}
		if len(s.Account) == 0 {
			continue
		}
		checked++

		pre := num(t, s.Account, "pre_balance")
		static := num(t, s.Account, "static_balance")
		closeP := num(t, s.Account, "close_profit")
		posP := num(t, s.Account, "position_profit")
		floatP := num(t, s.Account, "float_profit")
		comm := num(t, s.Account, "commission")
		deposit := num(t, s.Account, "deposit")
		withdraw := num(t, s.Account, "withdraw")
		bal := num(t, s.Account, "balance")
		avail := num(t, s.Account, "available")
		margin := num(t, s.Account, "margin")
		fMargin := num(t, s.Account, "frozen_margin")
		fComm := num(t, s.Account, "frozen_commission")
		fPrem := num(t, s.Account, "frozen_premium")

		if posP.IsPositive() {
			withGain++
		}
		if posP.IsNegative() {
			withLoss++
		}
		if closeP.IsPositive() {
			withPosClose++
		}
		if !fMargin.IsZero() || !fComm.IsZero() {
			withFrozen++
		}
		if !deposit.IsZero() {
			withDeposit++
		}
		if !withdraw.IsZero() {
			withWithdraw++
		}

		// ①a 静态权益：入金出金**已经被 static_balance 吸收**。
		//
		// ⚠️ 这一条是踩出来的：我原先写成
		// `balance = static_balance + deposit − withdraw + ...`，
		// 于是 2026-09-07 那两份夹具（当天入金 100 万）整整差了 100 万——
		// **入金被算了两遍**。§8 里那条 CTP 形式的公式用的是 `PreBalance`，是对的；
		// 错的是我把 `PreBalance` 版和 `StaticBalance` 版的项混在了一起。
		//
		// ⚠️ 这条关系只在**入金非零**的那天测得出。整批夹具里只有两份满足，
		// 而那两份是 09-07 的旧夹具——**如果当初嫌旧就删了它们，这一条今天无从验证。**
		wantStatic := pre.Add(deposit).Sub(withdraw)
		if r := wantStatic.Sub(static).Abs(); r.GreaterThan(moneyEps) {
			t.Errorf("%s 静态权益对不上：pre %s + deposit %s − withdraw %s = %s，实为 %s",
				p, pre, deposit, withdraw, wantStatic, static)
		}

		// ①b 结存链条
		wantBal := static.Add(closeP).Add(posP).Sub(comm)
		if r := wantBal.Sub(bal).Abs(); r.GreaterThan(moneyEps) {
			t.Errorf("%s 结存对不上：static %s + close %s + pos %s − comm %s = %s，实为 %s（差 %s）",
				p, static, closeP, posP, comm, wantBal, bal, r)
		} else if r.GreaterThan(maxResidual) {
			maxResidual = r
		}

		// ② 可用资金
		wantAvail := bal.Sub(margin).Sub(fMargin).Sub(fComm).Sub(fPrem)
		if r := wantAvail.Sub(avail).Abs(); r.GreaterThan(moneyEps) {
			t.Errorf("%s 可用对不上：balance %s − margin %s − 冻结(%s+%s+%s) = %s，实为 %s（差 %s）",
				p, bal, margin, fMargin, fComm, fPrem, wantAvail, avail, r)
		} else if r.GreaterThan(maxResidual) {
			maxResidual = r
		}

		// ③ 今仓下 position_profit 与 float_profit 必须相等。
		// ⚠️ 本批夹具**全是今仓**（种子还没过结算），这一条在有昨仓之后会分岔。
		if !posP.Equal(floatP) {
			t.Errorf("%s 今仓下 position_profit(%s) != float_profit(%s) —— "+
				"若此时账上仍无昨仓，本项目对 ByDate/ByTrade 的理解要改", p, posP, floatP)
		}
	}

	if checked < 10 {
		t.Fatalf("只核到 %d 份带账户截面的夹具 —— 太少，本条可能在空转", checked)
	}

	// ——— ② 判别力报告：这批证据**分不开**哪些柜台参数 ———
	// ⚠️ 把最大残差打出来，别让容差把真实漂移藏起来。
	// 三者的关系由 TestToleranceOrderingIsAsserted 守住，这里只报数。
	t.Logf("恒等式最大残差 %s（容差 %s，兜底阈值 %s，最小有意义金额 %s）",
		maxResidual, moneyEps, residualBackstop, minMeaningfulMoney)
	if maxResidual.GreaterThan(residualBackstop) {
		t.Errorf("⚠️ 最大残差 %s 已经比 float64 表示误差大得多 —— "+
			"这不像噪声，像真的对不上。别再往上调容差，去查账", maxResidual)
	}

	t.Logf("判别力：浮盈 %d / 浮亏 %d / 平仓盈利为正 %d / 有冻结 %d / 有入金 %d / 有出金 %d（共 %d 份）",
		withGain, withLoss, withPosClose, withFrozen, withDeposit, withWithdraw, checked)

	if withDeposit == 0 {
		t.Error("⚠️ 没有一份夹具有入金 —— `static_balance = pre + deposit − withdraw` 这条恒等式空转")
	}
	if withWithdraw == 0 {
		t.Log("⚠️ 没有一份夹具有出金 —— 上面那条恒等式的 `− withdraw` 项**从未参与过运算**。" +
			"一个恒为 0 的减项不会暴露自己被漏掉，见 silent-risks.md。模拟账户无法出金，此项只能等真实柜台。")
	}

	// `Algorithm`（盈亏算法）四取值：全计 / 只计浮亏 / 只计浮盈 / 都不计。
	// 要把它们分开，**浮盈与浮亏都得出现过**。
	if withGain == 0 || withLoss == 0 {
		t.Errorf("⚠️ 浮盈 %d 份、浮亏 %d 份 —— 缺一边就分不开 CTP `Algorithm` 的四个取值。"+
			"结存恒等式在这批证据上「成立」，不等于它对四个取值都成立", withGain, withLoss)
	}

	// `AvailIncludeCloseProfit` 两取值只在**平仓盈利为正**时分得开。
	if withPosClose == 0 {
		t.Log("⚠️ 没有任何一份夹具的 close_profit 为正 —— " +
			"CTP `AvailIncludeCloseProfit`（包含/不包含平仓盈利）在这批证据上**完全没有判别力**。" +
			"这不是失败，是必须写下来的缺口：见 cn-futures-rules.md §13。")
	} else {
		t.Errorf("⚠️ 已经出现平仓盈利为正的夹具（%d 份）—— "+
			"上面那句「没有判别力」的注释已经过期，请改掉并把该项从 §13 移出", withPosClose)
	}

	if withFrozen == 0 {
		t.Error("⚠️ 没有任何一份夹具带非零冻结 —— 可用资金那条恒等式只验证了 balance − margin 一半")
	}
}
