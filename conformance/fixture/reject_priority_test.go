package fixture

import (
	"testing"
)

// TestRejectPriorityFromFixtures 从**入库的夹具**里把拒绝优先级读出来。
//
// # 判据
//
// 一笔单同时违反两项时，柜台只回一个 `last_msg`。于是：
//
//	价格偏离整数倍、但在涨跌停之内   ⇒ 只违反「最小变动价位」
//	价格偏离整数倍、且越过涨跌停     ⇒ 两项都违反
//
// 两组的 `last_msg` **不相同**，就证明越界这一项被优先报了出来。
// ⚠️ 这里不认任何关键字：判据是「两组原话不相等」，不是「原话里有『涨停』二字」。
// 关键字匹配会把断言变成「我猜柜台怎么措辞」，措辞一改，
// 一条错误的结论会继续绿着。
//
// # 这条测试盖不住什么
//
// ⚠️ 它证明「越界优先于整数倍」，**不能**证明报出来的那句话就是越界的拒因 ——
// 那要一笔**只**越界（价格仍是整数倍）的单做标尺，而 20260909 的那批
// 一笔都没有：当时的构造用 `涨停价 × 1.05`，19514×1.05 = 20489.7
// 本身就不是整数倍。补上之后这条测试会自动变强，见下面的 t.Logf。
func TestRejectPriorityFromFixtures(t *testing.T) {
	specs := loadSpecsForTest(t)
	all := loadAll(t)

	type bucket struct {
		msgs  map[string][]string // last_msg -> 出处
		count int
	}
	tickOnly := bucket{msgs: map[string][]string{}}
	both := bucket{msgs: map[string][]string{}}
	limitOnly := bucket{msgs: map[string][]string{}}

	for _, f := range all {
		for id, o := range f.Orders {
			sym, ok := textOf(o, "exchange_id", "instrument_id")
			if !ok {
				continue
			}
			sp, ok := specs[sym]
			if !ok || !sp.PriceTick.IsPositive() {
				continue // 没有规格就判不了「整数倍」——不猜
			}
			price, ok := numberOf(o, "limit_price")
			if !ok {
				continue
			}
			up, okU := numberOf(f.Quotes[sym], "upper_limit")
			low, okL := numberOf(f.Quotes[sym], "lower_limit")
			if !okU || !okL {
				continue // 同一份夹具里没有这个合约的涨跌停，判不了越界
			}
			msg, ok := o["last_msg"]
			if !ok || !msg.IsText || msg.Text == "" {
				continue
			}
			offTick := !price.Mod(sp.PriceTick).IsZero()
			over := price.GreaterThan(up) || price.LessThan(low)
			where := f.Path + " " + id
			switch {
			case offTick && over:
				both.count++
				both.msgs[msg.Text] = append(both.msgs[msg.Text], where)
			case offTick:
				tickOnly.count++
				tickOnly.msgs[msg.Text] = append(tickOnly.msgs[msg.Text], where)
			case over:
				limitOnly.count++
				limitOnly.msgs[msg.Text] = append(limitOnly.msgs[msg.Text], where)
			}
		}
	}

	t.Logf("只违反整数倍 %d 笔（%d 种原话）；两项都违反 %d 笔（%d 种）；只越界 %d 笔（%d 种）",
		tickOnly.count, len(tickOnly.msgs), both.count, len(both.msgs),
		limitOnly.count, len(limitOnly.msgs))

	// ⚠️ 两个桶都必须有样本。少了任何一个，下面的「不相等」是空真。
	if tickOnly.count == 0 || both.count == 0 {
		t.Skipf("⚠️ 样本不全（只违反整数倍 %d 笔、两项都违反 %d 笔）—— "+
			"这条判据要两组都有才成立，现在什么都不说明",
			tickOnly.count, both.count)
	}
	// 每一组内部原话必须一致：不一致说明这一组混进了别的拒因。
	if len(tickOnly.msgs) != 1 {
		t.Errorf("⚠️ 「只违反整数倍」这一组有 %d 种原话 %v —— "+
			"混进了别的拒因，这一组不能当标尺", len(tickOnly.msgs), keysOf(tickOnly.msgs))
	}
	if len(both.msgs) != 1 {
		t.Errorf("⚠️ 「两项都违反」这一组有 %d 种原话 %v",
			len(both.msgs), keysOf(both.msgs))
	}
	for m, where := range both.msgs {
		if _, same := tickOnly.msgs[m]; same {
			t.Errorf("⚠️ 两项都违反的单子（%v）与只违反整数倍的单子报了**同一句**原话 %q —— "+
				"那就意味着柜台先报整数倍，与 order.CheckPriceLimit 排在 "+
				"order.CheckPriceTick 前面**相反**", where, m)
		}
	}

	// 有了「只越界」的样本，这条判据就能从「不相等」升级成「等于越界那句」。
	if limitOnly.count == 0 {
		t.Logf("ⓘ 还没有「只越界且仍是整数倍」的样本 —— " +
			"因此只证到「越界优先」，没证到「报出来的就是越界那句」。" +
			"reject-priority 实验已改成用 涨停价 + 10×tick 构造，下次跑会补上")
	} else {
		for m := range both.msgs {
			if _, ok := limitOnly.msgs[m]; !ok {
				t.Errorf("⚠️ 两项都违反时报的原话 %q，不在「只越界」那一组 %v 里 —— "+
					"报出来的既不是越界也不是整数倍，是第三种东西", m, keysOf(limitOnly.msgs))
			}
		}
	}
}

func keysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
