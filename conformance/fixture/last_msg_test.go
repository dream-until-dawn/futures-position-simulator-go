package fixture

import (
	"sort"
	"testing"
)

// knownLastMsg 是**见过的**柜台拒因原话，逐条写明它是什么。
//
// ⚠️ 这张表就是「取值白名单」：字段白名单管**哪个字段**能进夹具，
// 这张表管**那个字段里能出现什么**。`last_msg` 是柜台写的自由文本，
// 而自由文本上唯一站得住的保护就是这一层。
var knownLastMsg = map[string]string{
	"下单价格不是价格单位的整倍数": "价格零头 < 半个 tick（kq_facts 45）",
	"已撤单报单被拒绝价格超出涨停板": "买价越涨停",
	"平今手数超过今仓持仓量":     "今仓不足",
	"平昨手数超过昨仓持仓量": "昨仓不足。⚠️ 顺带钉住裸 CLOSE 在 SHFE 上被解释为**平昨**" +
		"（kq_facts 32）—— 柜台自己用「平昨」这个词回的",
}

// TestLastMsgValuesAreKnown 把 `last_msg` 的**取值**也纳入白名单。
//
// # 它补的是一处我自己写错的原则
//
// 决定 notify 的 content 不进夹具时，写下的理由是
// 「白名单靠逐个字段点名保护，而自由文本从原理上不在保护范围内」。
// ⚠️ **那条理由按字面同样会否掉 `last_msg`** —— 而 `last_msg` 是柜台写的
// 自由文本，且是拒因那一整批实验的全部产出（评审方 20260909 指出）。
//
// > **原则与实际做法对不上，说明真正起作用的判据不是写下来的那一条。**
//
// 真正起作用的有两层，现在写清楚：
//
//	一 `last_msg` 是**我们自己的动作招来的**回话，词表被我们下的单界定；
//	  notify 是另一条通道，设计上**可以**承载与我们的动作无关的东西。
//	  ⚠️ 要说准：我们至今**没有观测到**一条非我们招致的 notify
//	  （见过的 311/401/412 全是登录与报单引出的）——
//	  所以这一层说的是「通道能承载什么」，不是「我们见过什么」。
//	二 更硬的一层：`last_msg` 的取值从这条测试起**有守卫**，
//	  出现没见过的原话就红；而 content 没有，因为它还不进夹具。
//
// ⚠️ 于是选项不是两个而是三个：不收 / 全收 / **把白名单从字段下推到取值**。
// 那是一个「往公开仓库里写什么」的决定，留给使用者。
func TestLastMsgValuesAreKnown(t *testing.T) {
	seen := map[string][]string{}
	for _, f := range loadAll(t) {
		for id, o := range f.Orders {
			v, ok := o["last_msg"]
			if !ok || !v.IsText || v.Text == "" {
				continue
			}
			seen[v.Text] = append(seen[v.Text], f.Path+" "+id)
		}
	}
	texts := make([]string, 0, len(seen))
	for s := range seen {
		texts = append(texts, s)
	}
	sort.Strings(texts)
	t.Logf("夹具里出现过 %d 种拒因原话", len(texts))

	// ⚠️ 判别力用**确切下界**：语料里现在就有 4 种，
	// 少于它说明读夹具那一步坏了，而那时这条测试会对空集返回「干净」。
	if len(texts) < 4 {
		t.Fatalf("⚠️ 只找到 %d 种拒因原话 —— 少于 4 种，读夹具那一步很可能坏了", len(texts))
	}
	for _, s := range texts {
		if _, ok := knownLastMsg[s]; !ok {
			// ⚠️ 只举前三处。列全的话一条错误能刷几十行，
			// 而**一条要滚屏才能读完的错误信息，与一条没人读的错误信息同效**。
			where := seen[s]
			if len(where) > 3 {
				where = append(append([]string{}, where[:3]...),
					"…… 另有 "+itoa(len(seen[s])-3)+" 处")
			}
			t.Errorf("⚠️ 没登记过的拒因原话 %q（出现在 %v）—— "+
				"⚠️ 先**看一眼它有没有带标识信息**，再决定登记还是把它挡在夹具外。"+
				"这一层是自由文本字段唯一站得住的保护：字段白名单管哪个字段能进，"+
				"这张表管那个字段里能出现什么", s, where)
		}
	}
	// 反向只记不判：登记了却没再出现，多半是语料换了而不是错。
	for s := range knownLastMsg {
		if _, ok := seen[s]; !ok {
			t.Logf("ⓘ 登记了 %q 但本批语料里没有它 —— 那条观测现在没有样本支撑", s)
		}
	}
}

// itoa 是 strconv.Itoa 的本地别名，免得为一处调用引一个 import。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
