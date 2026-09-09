package fixture

import (
	"sort"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// TestDCEFillsCloseTodayRoutinely 钉住 kq_facts 39 里**承重的那一半**。
//
// # 承重的是哪一半
//
// 第 39 条的后果落在**分层**上：`position.Close` 因此**不能**对
// `NoUseHistory` 拒绝 `CLOSETODAY` —— 否则夹具里那一大批平仓一律重放不了。
// 撑着这个后果的，是「`DCE/CLOSETODAY` 确实成交过，而且是常态」。
//
// ⚠️ 所以这里钉的是**下界**，不是那个具体的数：数会随语料长，
// 而「常态」这件事只要求它足够多。
//
// # 顺带更正一处过期计数
//
// state.md 第 39 条原文写着「`SHFE/CLOSE` 1 笔」。⚠️ 那是写下它那天的数，
// 今天是十几笔（frozen / close 那几条实验加进来的）。
// 它不影响结论 —— 更正它是因为**一个没人复核的数会一直看起来像刚数过**。
func TestDCEFillsCloseTodayRoutinely(t *testing.T) {
	byKey := map[string]int{}
	for _, f := range loadAll(t) {
		for _, tr := range f.Trades {
			ex := string(tr.Instrument.Exchange)
			if ex == "" {
				continue
			}
			// ⚠️ 这里**不能**写 string(tr.Offset)。Offset 的底层类型是 uint8，
			// 那个转换在语法上合法（byte → 一个字符），产出的是**不可见的控制字符**，
			// 而 go vet 的 stringintconv **不报**这一种（底层是 byte 时它放行）。
			// ⚠️ 更阴的是它不会看起来像坏了：分组照样正确（不同取值仍是不同的键），
			// 表格照样打出来，只是那一列**空着** —— 像排版问题，不像类型错误。
			byKey[ex+"/"+tr.Offset.String()]++
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("  %-18s %d 笔", k, byKey[k])
	}

	const floor = 50
	if n := byKey["DCE/"+types.CloseToday.String()]; n < floor {
		t.Fatalf("⚠️ DCE/CLOSETODAY 只成交了 %d 笔（下界 %d）—— "+
			"kq_facts 39 的后果（position.Close **不能**对 NoUseHistory 拒绝 "+
			"CLOSETODAY）就没有语料撑着了。⚠️ 这不是「放宽下界」的时候："+
			"下界掉下来说明语料里那一类平仓消失了，先去看它为什么消失", n, floor)
	}
}

// TestDCEHasNeverUsedBareClose 是一条**零观测的绊线**。
//
// 全语料至今没有一笔 `DCE/CLOSE` 成交。⚠️ 这是一个**关于缺席**的观测，
// 而关于缺席的观测在新语料到来时会安静地失效 ——
// 没有守卫的话，第一笔 `DCE/CLOSE` 不会有任何动静。
//
// ⚠️ 它红了**不表示本库有 bug**：那说明快期在 DCE 上也收裸 `CLOSE`，
// 是一条**新事实**。该做的是把它写进 kq_facts、重读第 32/39 条
// （「`NoUseHistory` 上没有消耗顺序这回事」那一串推论都建立在
// 「DCE 只走 CLOSETODAY」上），然后**删掉这条绊线**。
func TestDCEHasNeverUsedBareClose(t *testing.T) {
	var hits []string
	for _, f := range loadAll(t) {
		for _, tr := range f.Trades {
			if string(tr.Instrument.Exchange) == "DCE" && tr.Offset == types.Close {
				hits = append(hits, f.Path+" "+tr.TradeID+" "+tr.Instrument.Native())
			}
		}
	}
	if len(hits) > 0 {
		sort.Strings(hits)
		t.Fatalf("⚠️ **第一笔 DCE/CLOSE 成交出现了**：%v\n"+
			"这不是 bug，是一条新事实：快期在 DCE 上也收裸 CLOSE。"+
			"该做的是写进 kq_facts、重读第 32/39 条那一串推论，然后删掉这条绊线", hits)
	}
	t.Log("DCE/CLOSE 仍是零观测 —— kq_facts 39「DCE 只走 CLOSETODAY」暂无反例")
}
