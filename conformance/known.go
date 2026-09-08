package conformance

import (
	"fmt"
	"sort"
	"strings"
)

// KnownClass 是一处**已登记的口子差异**属于哪一类。
//
// ⚠️ 它与 `Deviation` 是两件事，别混：
//
//	Deviation   一个**字段**的豁免声明 —— 要出处、裁决者、选边理由三样齐全
//	KnownClass  一批字段**为什么**对不上的归类 —— 用来把「已知」与「第三类」分开
//
// 一个字段可以属于某一类而**没有**豁免（那时它仍判失败，只是失败被归了类）。
// 这是刻意的：归类是为了让**新出现的**失败显眼，不是为了让已知的消失。
type KnownClass struct {
	// Class 是类别标签。⚠️ 带前缀，见 Registry 的说明。
	Class string
	// Why 是这一类的机制说明。空串不许 —— 一个没有说明的类别，
	// 与「随手贴的标签」分不开，而那正是本仓库栽过的地方。
	Why string
	// Evidence 指向实测出处（state.md 的 kq_facts 编号等）。
	// ⚠️ 类别以 "-?" 结尾时可以为空：那正是「样本区分不了」的意思。
	Evidence string
}

// Registry 是一批字段的已知差异归类。
//
// # ⚠️ 为什么它是**非测试**代码
//
// 这张表原来只活在 `crossday_test.go` 里。而**两条路要用它**：
// 离线对拍（测试）与实时对拍（`oracle conformance`）。
//
// 只有测试有它的话，实时那条路会把每一处已知差异都报成失败 ——
// 于是那个工具**永远红**。⚠️ 一个永远红的对拍工具，
// 和一个永远绿的一样会被无视，只是被无视的理由听起来更正当。
//
// # ⚠️ 类别标签必须带前缀
//
// 曾经同一个包里有两套 A/B：一套的类 A 是另一套的类 D，
// 而两处都出现在失败消息里。读到「类 B 出现了 1 次」的人去对另一张表，
// 会得出完全相反的结论。所以标签形如 `跨日-A` / `字段-B`，
// 让两套词汇在**字面上**就分得开，而不是靠读的人记得自己在哪个文件里。
type Registry map[string]KnownClass

// Split 把失败字段分成「已登记」与「第三类」。
//
// ⚠️ 第三类才是要人**立刻去查**的：它是新出现的、还没有解释的不一致。
// 把两者混在一个「失败 N 个」里，新东西会被淹在一堆已知里。
func (r Registry) Split(failed []string) (known map[string][]string, novel []string) {
	known = map[string][]string{}
	for _, name := range failed {
		k, ok := r[name]
		if !ok {
			novel = append(novel, name)
			continue
		}
		known[k.Class] = append(known[k.Class], name)
	}
	for _, v := range known {
		sort.Strings(v)
	}
	sort.Strings(novel)
	return known, novel
}

// Validate 检查这张表本身够不够格。
//
// ⚠️ 两条，都栽过：
//
//	每一类都要有机制说明        没有说明的标签与随手贴的分不开
//	带因果的类别要有实测出处    而 "-?" 结尾的类别恰恰**不许**有 ——
//	                            它的含义就是「样本区分不了」，给它一个出处是自相矛盾
func (r Registry) Validate() error {
	var errs []string
	for name, k := range r {
		if strings.TrimSpace(k.Class) == "" {
			errs = append(errs, fmt.Sprintf("%s 的类别是空串", name))
			continue
		}
		if !strings.Contains(k.Class, "-") {
			errs = append(errs, fmt.Sprintf(
				"%s 的类别 %q **没有前缀** —— 同一个仓库里曾有两套 A/B 互相冲突，"+
					"标签必须形如「跨日-A」让两套词汇字面上就分得开", name, k.Class))
		}
		if strings.TrimSpace(k.Why) == "" {
			errs = append(errs, fmt.Sprintf(
				"%s（%s）没有机制说明 —— 一个没有说明的类别与随手贴的标签分不开",
				name, k.Class))
		}
		unresolved := strings.HasSuffix(k.Class, "-?")
		if !unresolved && strings.TrimSpace(k.Evidence) == "" {
			errs = append(errs, fmt.Sprintf(
				"%s（%s）是**因果**类别却没有实测出处 —— "+
					"要么补出处，要么把它降成「-?」（样本区分不了）", name, k.Class))
		}
		if unresolved && strings.TrimSpace(k.Evidence) != "" {
			errs = append(errs, fmt.Sprintf(
				"%s 的类别以「-?」结尾（样本区分不了），却带着出处 %q —— "+
					"⚠️ 自相矛盾：分不开就是分不开，有出处就该给它一个因果类别",
				name, k.Evidence))
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("已知差异登记表不合格：\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
