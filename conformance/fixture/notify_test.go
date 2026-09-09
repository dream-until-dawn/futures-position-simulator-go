package fixture

import (
	"sort"
	"testing"
)

// notifyCodes 是**见过的**柜台通知码，逐条写明它是什么。
//
// ⚠️ 文案不在夹具里 —— 落盘时刻意不收自由文本（见 Fixture.Notifies）。
// 所以这张表是**人工看过日志之后写下来的**，出处一并记在这里。
// 它因此是一份需要人来维护的表，而不是从数据反推出来的。
var notifyCodes = map[int]string{
	401: "登录/连接类的 INFO 通知。20260909 每次连上柜台都会来一条。",
	311: "报单的合约不存在。⚠️ 20260909 实测：这类拒绝**一个字都不写进委托记录**，" +
		"只从 notify 回。原话形如「您下单的合约SHFE.ag9912不存在」。",
	412: "报单被服务器拒绝。原话形如「下单,已被服务器拒绝, 原因:<拒因>」——" +
		"拒因文案与委托记录里的 last_msg 相同，见 cn-futures-rules.md §9 的表。",
}

// notifyCodesPendingID 是**见过、但至今说不出它是什么**的码。
//
// ⚠️ 它不是 notifyCodes 的一个宽松版本，它是一笔**记名的欠账**：
// 每一条都写清哪天见的、为什么当时没认出来、以及**怎样才能认出来**。
//
// 20260909 09:02 的日盘实验里一次冒出四个新码。而它们说了什么
// **已经无从查起** —— 夹具刻意不收 content（那是使用者的待裁决），
// 而当时的客户端把 Content 解析出来就地丢掉了，**日志里也没有**。
//
//	于是那条「去日志里看它是什么」的补救指示，在当时是做不到的。
//	⚠️ 一条守卫红了、而它要求的动作**无法执行**，比它不红更糟：
//	人会去把码加进白名单，因为那是唯一做得到的动作。
//
// 已修：kq.Client.logNotifyOnce 现在把每条通知的文案打进本次运行的日志
// （只进本地日志，不落盘、不推送 —— 与「content 该不该进夹具」是两件事）。
// **下一次这四个码再出现时就能认出来**，那时把它们移进 notifyCodes 并删掉这里。
var notifyCodesPendingID = map[int]string{
	404: "20260909 09:02 首见（日盘 reject-tick-vs-limit，WARNING）。" +
		"出现 7 次，与被拒的报单同批 —— 但**没有文案**，说不出是哪一类拒绝。",
	417: "20260909 09:02 首见（INFO）。出现 3 次。",
	419: "20260909 09:02 首见（INFO）。出现 3 次。",
	420: "20260909 09:02 首见（INFO）。出现 1 次。",
}

// TestNotifyCodesAreKnown 断言夹具里出现的每一个通知码都被写下来过。
//
// # 为什么值得有
//
// 20260909 之前 notify 整条通道**不进夹具**，于是一整类拒绝在存档证据上
// 根本不存在：报单到不存在的合约上时，柜台不写委托记录，只回一个码。
// ⚠️ 只读委托记录的实验会把它读成「柜台没反应」——
// 而那与「单子还挂着」长得一模一样。
//
// 现在码进了夹具。这条守卫的作用是：**出现一个没见过的码就红**，
// 逼人去看那一条是什么。一个没人看过的码与一个看过并判定为无关的码，
// 在数据里长得一模一样。
func TestNotifyCodesAreKnown(t *testing.T) {
	seen := map[int][]string{}
	withNotifies := 0
	for _, f := range loadAll(t) {
		if !f.HasNotifies {
			continue
		}
		withNotifies++
		for _, n := range f.Notifies {
			seen[n.Code] = append(seen[n.Code], f.Path)
		}
	}
	codes := make([]int, 0, len(seen))
	for c := range seen {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	t.Logf("记了通知的夹具 %d 份，出现过的码 %v", withNotifies, codes)

	// ⚠️ 不用 t.Skip。skip 是绿的，而「一份都没有」正是这条守卫
	// 最该报警的时候 —— 落盘漏了、加载漏了、通道断了，三种情形下
	// 夹具里都是零条通知，而 skip 让它们全都静悄悄地过去。
	if withNotifies == 0 {
		t.Fatal("⚠️ 一份记了通知的夹具都没有 —— " +
			"20260909 起 notify 应当进夹具。先查落盘（kq.Sanitize）" +
			"还是加载（fixture.Load）漏了那一段")
	}
	pending := 0
	for _, c := range codes {
		if _, ok := notifyCodes[c]; ok {
			continue
		}
		if why, ok := notifyCodesPendingID[c]; ok {
			pending++
			t.Logf("ⓘ 通知码 %d **见过但没认出来**：%s", c, why)
			continue
		}
		t.Errorf("⚠️ 通知码 %d 没有登记（出现在 %v）—— "+
			"跑一次能复现它的实验，日志里现在会打印文案（kq 的 logNotifyOnce），"+
			"看清之后写进 notifyCodes。"+
			"⚠️ 一个没人看过的码与一个看过并判定为无关的码，在数据里一模一样",
			c, seen[c])
	}
	if pending > 0 {
		// ⚠️ 这笔欠账要**每次跑都出声**。一个安静的待办与一个不存在的待办，
		// 在测试输出里长得一样 —— 而这几个码正是「安静」了才拖到现在。
		t.Logf("⚠️ 有 %d 个码只登记了「见过」、没登记「是什么」（notifyCodesPendingID）。"+
			"它们的文案在首见那次没被记下来，要靠**复现**才能认出来", pending)
	}
	// ⚠️ 反向也要查：登记了却再没出现过的码，说明那条观测已经没有样本支撑。
	for c, why := range notifyCodes {
		if _, ok := seen[c]; !ok {
			t.Logf("ⓘ 登记了 %d（%s）但本批夹具里没有它 —— 那条结论现在没有语料支撑", c, why)
		}
	}
	// ⚠️ 判别力：至少要见过两个**不同**的码，否则「码能区分拒因」这句话没被验过。
	if len(codes) < 2 {
		t.Errorf("⚠️ 只见过 %d 个不同的码 —— "+
			"「不同的拒因给不同的码」这句话在这么点样本上没有判别力", len(codes))
	}
}
