package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMsgCodeOnlyTakesTheLeadingInteger 钉住「码」与「文本里别处的数字」分得开。
//
// ⚠️ 交易所把码塞在 `StatusMsg` 的前缀里（`48:价格不是最小变动价位的倍数`），
// 而同一段文本里常常还有价格、手数。一个「找第一个数字」的实现
// 在有前缀时**恰好也对**，于是它与正确实现在现有样本上分不开。
func TestMsgCodeOnlyTakesTheLeadingInteger(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"48:INE:价格不是最小变动价位的倍数", 48, true},
		{"50:价格跌破跌停板", 50, true},
		{" 51 : 平昨仓位不足", 51, true}, // 前后空格要容
		{"CTP:平昨仓位不足", 0, false},   // ⚠️ 有冒号但前面不是整数
		{"平仓量超过持仓量", 0, false},     // 没有冒号
		{"", 0, false},
		{":48", 0, false}, // ⚠️ 冒号在最前 ⇒ 前缀是空串，不是 48
		// ⚠️ 关键一格：文本里**别处**有数字，而前缀不是码。
		// 一个「找第一个数字」的实现会在这里返回 3272，且**看起来很合理**。
		{"CTP:报价 3272 超出涨跌停", 0, false},
	} {
		got, ok := msgCode(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("msgCode(%q) = (%d,%v)，要的是 (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
	// ⚠️ 反空转：上表里必须**既有**认得出的、**也有**认不出的。
	// 一张全是正例的表，让「恒返回 true」也能全绿。
	var pos, neg int
	for _, s := range []string{"48:x", "CTP:x"} {
		if _, ok := msgCode(s); ok {
			pos++
		} else {
			neg++
		}
	}
	if pos == 0 || neg == 0 {
		t.Fatalf("⚠️ 判别力为零：正例 %d、反例 %d —— 本条在单侧样本上跑", pos, neg)
	}
}

// TestRejectCorpusIsProbeWrittenOnly 断言语料里**每一条都是探针写的**。
//
// # ⚠️ 它堵的是一条很省事的歪路
//
// 20260912 评审判「拒单码只活在提交正文里」为**必修**。而最快的「修法」是
// **照着提交正文手打一份语料** —— 那会造出一份带机器可读外形的转录，
// **而它与真正拍下来的语料在文件里长得一模一样**。
//
//	⚠️ 这正是同一天 §13 #13 被打回的那个毛病的翻版：
//	文档里的六个数没有任何落盘支撑，而它们看起来像证据。
//
// ⇒ 谁要手填，得先把本条改掉 —— **而那一改会出现在 diff 里。**
//
// ⚠️ 顺带钉住语料**不含柜台原话**：`StatusMsg` 是自由文本，
// 按纪律「可以进本地日志，不进入库」。
func TestRejectCorpusIsProbeWrittenOnly(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "refdata", "ctp-reject-codes.json")
	const debtPhrase = "语料待拍"
	rules, err := os.ReadFile(filepath.Join("..", "..", "docs", "cn-futures-rules.md"))
	if err != nil {
		t.Fatal(err)
	}
	declaredPending := strings.Contains(string(rules), debtPhrase)

	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// ⚠️ **这一支不许 `t.Skip`。**本仓自己的教训：一条 skip 在
		// `go test ./...` 的汇总里**看不见** —— 有 skip 的包照样打 `ok`
		// （评审 20260909 因此把一次「17 个包全绿」报少了一条没跑的测试）。
		//
		// ⇒ 改成**双向断言**：语料不在时，文档必须自己说出这件事。
		// 于是「语料还没拍」这个状态**有一处可见的对应物**，
		// 而不是一条沉默的绿。
		if !declaredPending {
			t.Fatalf("⚠️⚠️ 语料不存在（%s），**而 §13 里也没写 %q** —— "+
				"两头都不说，这个缺口就没有任何可见的对应物了。"+
				"⇒ 要么去拍语料，要么在 §13 #6 里写明它还没拍", path, debtPhrase)
		}
		t.Logf("ⓘ 语料尚未拍（%s），而 §13 已声明 %q —— 本条此刻只在核对那个声明，"+
			"**没有在守语料本身**。工具已就绪：下一个夜盘 `ctp-reject -out`", path, debtPhrase)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	// ⚠️ 反过来也要拦：语料拍到了而文档还写着「待拍」，
	// 那是一句**过期的低报** —— 评审卡的四件事里第一条就是它。
	if declaredPending {
		t.Errorf("⚠️ 语料已存在（%s），而 §13 里仍写着 %q —— "+
			"**过期陈述**，把那句话改掉", path, debtPhrase)
	}
	var all []rejectObservation
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("⚠️ 语料是空数组 —— 一份空语料与一份没生成的语料在下游同形")
	}
	for i, o := range all {
		if o.Source != "probe" {
			t.Errorf("⚠️ 第 %d 条的 source = %q（只许 \"probe\"）—— "+
				"**手打的转录带着机器可读的外形，而它不是观测**", i, o.Source)
		}
		if o.TradingDay == "" || o.Exchange == "" || o.Case == "" {
			t.Errorf("⚠️ 第 %d 条缺 trading_day / exchange / case —— 无法定位它是哪一次观测", i)
		}
		if o.ErrorID == nil && o.ExchangeCode == nil && o.Outcome == "rejected" {
			t.Errorf("⚠️ 第 %d 条报 rejected 却两个码位都空 —— "+
				"那是「被拒了但没拿到码」，与「没被拒」在下游同形，必须说清", i)
		}
	}
	// ⚠️ 原话不得入库：整份文件里不许出现柜台文案的痕迹。
	if s := string(b); strings.Contains(s, "status_msg") || strings.Contains(s, "StatusMsg") {
		t.Error("⚠️⚠️ 语料里出现了 StatusMsg —— 柜台自由文本不进库")
	}
}
