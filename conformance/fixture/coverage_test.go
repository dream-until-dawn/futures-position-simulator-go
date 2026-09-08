package fixture

import (
	"sort"
	"testing"
)

// observation 是一个持仓字段在**全部夹具**里被看见过的形态。
type observation struct {
	nonZero int // 见过非零数值
	zero    int // 见过 0
	absent  int // 见过 "-"（柜台声明此处没有值）
	text    int // 见过非数值字符串
}

// seen 说这个字段在夹具里出现过没有。
func (o observation) seen() bool { return o.nonZero+o.zero+o.absent+o.text > 0 }

// TestPositionFieldEvidence 数清楚**每个持仓字段有没有过非零观测**。
//
// ⚠️ 这条测试不验任何计算。它回答的是一个更前面的问题：
// **这批夹具里，哪些字段其实什么都没说。**
//
// 一个从未取到过非零值的字段，对拍时本库给 0、柜台给 0，判「一致」——
// 而那个「一致」什么都不说明：把该字段的实现整个删掉，它照样一致。
// 已经踩过一次（NotImplemented 被填成 decimal.Zero，44 个「对得上」
// 里有 12 个是假的），所以这里把它变成一个**能看见的数**。
//
// ⚠️ 三种「不是非零」要分开数，它们的下一步动作完全不同：
//
//	只见过 0    字段在，值恒为零 —— 要造一个让它非零的场景（挂单、开空、平昨）
//	只见过 "-"  柜台声明此处没有值 —— 那是 kq_facts 13，本来就不该有数
//	从没出现过  夹具根本没采到 —— 采集器的白名单要查
//
// 合并成一个「未触发」会让第三种躲在前两种后面，而它是采集器的 bug。
func TestPositionFieldEvidence(t *testing.T) {
	all := loadAll(t)
	if len(all) == 0 {
		t.Fatal("⚠️ 一份夹具都没加载到 —— 下面每个数都是 0，而那看起来像「全都没观测」")
	}

	obs := map[string]*observation{}
	sections := 0
	for _, f := range all {
		for _, sym := range f.Symbols() {
			sections++
			for k, v := range f.Positions[sym] {
				o := obs[k]
				if o == nil {
					o = &observation{}
					obs[k] = o
				}
				switch {
				case v.Absent:
					o.absent++
				case v.IsText:
					o.text++
				case v.Number.IsZero():
					o.zero++
				default:
					o.nonZero++
				}
			}
		}
	}

	var withEvidence, onlyZero, onlyAbsent, textual []string
	for k, o := range obs {
		switch {
		case o.nonZero > 0:
			withEvidence = append(withEvidence, k)
		case o.zero > 0:
			onlyZero = append(onlyZero, k)
		case o.text > 0 && o.absent == 0:
			// ⚠️ 标识性字段（exchange_id / instrument_id）压根不是数 ——
			// 把它们混进「没有非零观测」会虚报一个永远补不上的缺口。
			textual = append(textual, k)
		default:
			onlyAbsent = append(onlyAbsent, k)
		}
	}
	sort.Strings(withEvidence)
	sort.Strings(onlyZero)
	sort.Strings(onlyAbsent)
	sort.Strings(textual)

	t.Logf("%d 份夹具、%d 个持仓截面、%d 个字段：有非零观测 %d、只见过零 %d、"+
		"只见过\"-\" %d、非数值字段 %d",
		len(all), sections, len(obs),
		len(withEvidence), len(onlyZero), len(onlyAbsent), len(textual))
	t.Logf("⚠️ 只见过零的 %d 个（对拍时两边都是 0，判「一致」什么都不说明）：", len(onlyZero))
	for _, k := range onlyZero {
		t.Logf("     %-30s 零 ×%d", k, obs[k].zero)
	}
	// ⚠️ 三档分开报。此前手数的「22 个未取到非零值」正是把它们加在一起
	// （17 + 3 + 2）得到的 —— 而三者的下一步动作完全不同：
	// 只见过零的要去造场景，只见过 "-" 的本来就不该有数（kq_facts 13），
	// 标识性字段压根不是数。合起来数会让人去补两个补不上的缺口。
	if len(onlyAbsent) > 0 {
		t.Logf("ⓘ 只见过 \"-\" 的 %d 个（柜台声明此处没有值，本来就不该有数）：%v",
			len(onlyAbsent), onlyAbsent)
	}
	if len(textual) > 0 {
		t.Logf("ⓘ 非数值字段 %d 个（标识用，不参与对拍的数值口径）：%v",
			len(textual), textual)
	}

	// —— 棘轮 ——
	//
	// ⚠️ 钉住的是**只见过零**那一档。它降下来是好消息，
	// 但好消息同样需要有人被通知到：一个悄悄变好的数，
	// 下一次悄悄变坏时也不会有动静。
	const pinnedOnlyZero = 16
	switch {
	case len(onlyZero) > pinnedOnlyZero:
		t.Errorf("⚠️ 只见过零的字段从 %d 涨到 %d —— **退化**："+
			"要么夹具少了，要么采集器不再采某些字段。多出来的：%v",
			pinnedOnlyZero, len(onlyZero), onlyZero)
	case len(onlyZero) < pinnedOnlyZero:
		t.Errorf("⚠️ 只见过零的字段从 %d 降到 %d —— **这是好消息**，"+
			"把 pinnedOnlyZero 改成 %d，并把新拿到证据的那些从 probe 的 "+
			"zeroObserved 表里挪走（否则实验会一直去补已经有的样本）",
			pinnedOnlyZero, len(onlyZero), len(onlyZero))
	}

	// ⚠️ 判别力自查：全都有证据、或全都没有，都说明这条在空转。
	if len(withEvidence) == 0 || len(onlyZero)+len(onlyAbsent) == 0 {
		t.Fatalf("⚠️ 分档退化了（有证据 %d / 无证据 %d）—— 本条什么都没说",
			len(withEvidence), len(onlyZero)+len(onlyAbsent))
	}
}

// TestFrozenFieldsAreTheThinnestEvidence 单独盯住六个冻结字段。
//
// ⚠️ 它们是 oracle 那边 position-frozen 实验的目标，
// 而那个实验的候选排序**写死了一张 zeroObserved 表**。
// 两处各记一份「哪个还没观测到」，迟早会对不上 ——
// 对不上的后果是实验一直去补一份已经有的样本，且不报错。
//
// 这里从夹具算出真相，与那张表的内容一起打出来，让人能对。
// ⚠️ 刻意**不做成断言**：两个模块，主模块不该 import oracle。
// 断言不了就说清楚断言不了，别假装它被守住了。
func TestFrozenFieldsAreTheThinnestEvidence(t *testing.T) {
	frozen := []string{
		"volume_long_frozen", "volume_long_frozen_today", "volume_long_frozen_his",
		"volume_short_frozen", "volume_short_frozen_today", "volume_short_frozen_his",
	}
	all := loadAll(t)
	counts := map[string]*observation{}
	for _, k := range frozen {
		counts[k] = &observation{}
	}
	for _, f := range all {
		for _, sym := range f.Symbols() {
			for _, k := range frozen {
				v, ok := f.Positions[sym][k]
				if !ok {
					continue
				}
				o := counts[k]
				switch {
				case v.Absent:
					o.absent++
				case v.IsText:
					o.text++
				case v.Number.IsZero():
					o.zero++
				default:
					o.nonZero++
				}
			}
		}
	}
	var never []string
	for _, k := range frozen {
		o := counts[k]
		if !o.seen() {
			t.Errorf("⚠️ %s 在 %d 份夹具里**一次都没出现过** —— "+
				"那不是「恒为零」，是采集器没采它。白名单要查", k, len(all))
			continue
		}
		if o.nonZero == 0 {
			never = append(never, k)
		}
		t.Logf("  %-30s 非零 ×%d  零 ×%d  \"-\" ×%d", k, o.nonZero, o.zero, o.absent)
	}
	t.Logf("⚠️ 至今零非零观测的冻结字段共 %d 个：%v", len(never), never)
	t.Log("⚠️ oracle 的 probe.zeroObserved 应当**正好是这几个**。" +
		"两处各记一份，对不上的后果是实验一直去补已经有的样本，且不报错。" +
		"主模块不 import oracle，所以这里只能打出来给人对，断言不了。")

	// ⚠️ 全都有观测时本条该被重写而不是留着 —— 那时它在空转。
	if len(never) == 0 {
		t.Error("⚠️ 六个冻结字段都拿到非零观测了 —— " +
			"position-frozen 实验的目的已达成，去把 zeroObserved 表和本条一起重写")
	}
}
