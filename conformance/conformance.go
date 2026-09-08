// Package conformance 是双口子对拍的判定机制。
//
// 它不做对拍本身——那是各包的测试的事——它提供**判定**：
// 逐字段的结果落进哪一档，以及一批字段合起来算不算通过。
//
// ⚠️ 这个包存在的理由是 design.md §5 里那句：
// 「100% 模拟」若不能被自动验证就只是一句口号。
// 而验证的难点从来不是「算得对不对」，是**「对得上」有几种不同的含义**。
package conformance

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Verdict 是逐字段比对的判定档位。
//
// ⚠️ 零值是 VerdictUnclassified，使用即报错。
// 一个默认落进「通过」的判定档位，会让忘记分类的字段全部变成通过。
type Verdict uint8

const (
	// VerdictUnclassified 是零值：没分类。使用即报错。
	VerdictUnclassified Verdict = iota

	// Matched 值对得上，且本次样本内**被触发过** —— 唯一算通过的一档。
	Matched

	// NotModeled 已声明不建模，且带到期版本。
	NotModeled

	// Untriggered 值对得上，但本次样本内**从未被触发**。
	//
	// ⚠️ 单独计数，**不算通过**。这一档堵的是零值假通过：
	// 字段两边都是零、样本从未触发它，对拍照样判「一致」。
	// 本仓库第一次连上快期就撞见了它的样子——空仓时
	// balance == ctp_balance == 1000000.0，看起来完全一致，实际什么都没证明。
	Untriggered

	// KnownDeviation 已知的**口子差异**：两边各自都对，值却不同。
	//
	// ⚠️ 这是唯一可能被滥用的一档。「对不上就说它是口子差异」会让整套验收失效，
	// 所以它有三条硬门槛（见 Deviation），缺一不成立。
	KnownDeviation

	// Failed 三档之外的一律判失败。
	Failed
)

func (v Verdict) String() string {
	switch v {
	case Matched:
		return "对得上且被触发过"
	case NotModeled:
		return "已声明不建模"
	case Untriggered:
		return "对得上但未触发"
	case KnownDeviation:
		return "已知口子差异"
	case Failed:
		return "失败"
	}
	return "未分类"
}

// Deviation 声明一处已知的口子差异。
//
// ⚠️ 三个字段**都必须非空**，这是第四档不被滥用的全部保障：
// 一个可以随口声明的豁免档，比没有这一档更坏——它会把真实的不一致洗成「已知」。
type Deviation struct {
	// Fixture 是出处：哪一份夹具、哪一次实测量到了这个差异。
	Fixture string
	// Arbiter 是裁决者：state.md 的 simnow_pending 里对应的那一条。
	Arbiter string
	// Chose 写明本库选了哪一边，以及选它的理由。
	Chose string
}

// Validate 检查这处声明够不够格。
func (d Deviation) Validate(field string) error {
	var missing []string
	if strings.TrimSpace(d.Fixture) == "" {
		missing = append(missing, "出处（哪份夹具量到的）")
	}
	if strings.TrimSpace(d.Arbiter) == "" {
		missing = append(missing, "裁决者（simnow_pending 的哪一条）")
	}
	if strings.TrimSpace(d.Chose) == "" {
		missing = append(missing, "本库选了哪一边及理由")
	}
	if len(missing) > 0 {
		return fmt.Errorf("字段 %s 声明为「已知口子差异」，但缺：%s —— "+
			"⚠️ 没有这三样的差异一律判失败。一个可以随口声明的豁免档，"+
			"比没有这一档更坏：它会把真实的不一致洗成「已知」",
			field, strings.Join(missing, "、"))
	}
	return nil
}

// Field 是一次逐字段比对。
type Field struct {
	// Name 是字段名，用口子那边的叫法，便于回溯到夹具。
	Name string

	// Library 是本库算出来的值；Oracle 是口子给的值。
	Library decimal.Decimal
	Oracle  decimal.Decimal

	// LibraryAbsent / OracleAbsent 表示该侧**声明此处没有值**，而不是值为零。
	//
	// ⚠️ 这一对不是可有可无的精细化，它堵的是一个能双向全绿的洞：
	// 快期在空仓方向上对 open_price / position_price / margin 返回字符串 "-"
	// 而不是 0（实测 188/188，probes.md §9）。
	// 对拍侧若把 "-" 解析成 0，那些字段会**碰巧一致**；
	// 若把 "-" 当成缺失整个跳过，它们会全部落进「未触发」。
	// **两条路都能让测试全绿，而它们互相矛盾** —— 所以「无值」必须有自己的表示。
	//
	// 判定规则（见 classifyOne）：
	//
	//	两侧都无值    按一致处理，但仍要求 Triggered 才算 Matched
	//	一侧无值      **一律 Failed** —— 这正是要抓的那一类
	LibraryAbsent bool
	OracleAbsent  bool

	// Triggered 报告这个字段在本次样本里**是否被真正触发过**。
	//
	// ⚠️ 它必须由**造样本的人**判定，不能由「值是不是零」推。
	// 一个非零值也可能没被触发（比如它是上一次实验留下的余额），
	// 而一个零值也可能是被触发之后的正确结果。
	Triggered bool

	// NotModeledUntil 非空表示已声明不建模，值是到期版本。
	NotModeledUntil string

	// Deviation 非 nil 表示这是一处已知口子差异。
	Deviation *Deviation
}

// Report 是一批字段的判定结果。
type Report struct {
	Sample   string // 样本来源，通常是夹具文件名
	Fields   []Field
	Verdicts map[string]Verdict
	Counts   map[Verdict]int
	Errs     []error
}

// Classify 逐字段判定。tol 是值比较的容差。
//
// ⚠️ 容差由调用方给，本包不设默认值：不同字段的最小有意义单位不同
// （金额是分的百分之一，费率是万分之一的几分之一），
// 一个包级默认值会在某些字段上过松而没有任何动静。
func Classify(sample string, fields []Field, tol decimal.Decimal) *Report {
	r := &Report{Sample: sample, Fields: fields,
		Verdicts: map[string]Verdict{}, Counts: map[Verdict]int{}}
	if !tol.IsPositive() {
		r.Errs = append(r.Errs, fmt.Errorf("比较容差必须为正，得到 %s —— "+
			"零容差会让浮点噪声变成失败，负容差会让一切通过", tol))
		return r
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if f.Name == "" {
			r.Errs = append(r.Errs, fmt.Errorf("有字段没有名字 —— 判定结果将无法回溯到夹具"))
			continue
		}
		if seen[f.Name] {
			r.Errs = append(r.Errs, fmt.Errorf("字段 %s 重复出现 —— "+
				"两次判定谁覆盖谁是未定义的", f.Name))
			continue
		}
		seen[f.Name] = true

		v := classifyOne(f, tol, r)
		r.Verdicts[f.Name] = v
		r.Counts[v]++
	}
	return r
}

func classifyOne(f Field, tol decimal.Decimal, r *Report) Verdict {
	// ⚠️ 顺序有意义：不建模与已知差异是**声明**，先于值比较；
	// 否则一个声明了不建模的字段还会因为值对不上而被判失败。
	if f.NotModeledUntil != "" {
		return NotModeled
	}
	if f.Deviation != nil {
		if err := f.Deviation.Validate(f.Name); err != nil {
			r.Errs = append(r.Errs, err)
			return Failed
		}
		// ⚠️ 已知差异仍然要求「本次样本内被触发过」。
		// 一个从未被触发的差异，声明它等于什么都没说。
		if !f.Triggered {
			r.Errs = append(r.Errs, fmt.Errorf("字段 %s 声明为已知口子差异，"+
				"但本次样本内**从未被触发** —— 没被触发的差异是一句空话", f.Name))
			return Failed
		}
		return KnownDeviation
	}
	// ⚠️ 「无值」先于数值比较判定，且**不许**退化成与零比较。
	switch {
	case f.LibraryAbsent && f.OracleAbsent:
		// 两侧都说没有 —— 一致。但这恰恰是最容易碰巧一致的形状
		// （空仓时几乎每个字段两边都「没有」），所以下面的 Triggered 照查。
		if !f.Library.IsZero() || !f.Oracle.IsZero() {
			r.Errs = append(r.Errs, fmt.Errorf("字段 %s 声明为无值，却带着数值 "+
				"(本库 %s / 口子 %s) —— 读它的人会各按各的理解取用",
				f.Name, f.Library, f.Oracle))
			return Failed
		}
	case f.LibraryAbsent != f.OracleAbsent:
		return Failed
	default:
		if f.Library.Sub(f.Oracle).Abs().GreaterThan(tol) {
			return Failed
		}
	}
	if !f.Triggered {
		return Untriggered
	}
	return Matched
}

// Passed 报告这批字段算不算通过。
//
// ⚠️ **只有 Matched 与 NotModeled 算通过。**
// Untriggered 单独计数且不算通过——那正是它存在的全部意义；
// KnownDeviation 也不算通过：它是一笔**记在账上的欠款**，不是一次验收。
func (r *Report) Passed() bool {
	if len(r.Errs) > 0 {
		return false
	}
	if len(r.Fields) == 0 {
		return false // 空样本不算通过
	}
	for v, n := range r.Counts {
		if n == 0 {
			continue
		}
		switch v {
		case Matched, NotModeled:
		default:
			return false
		}
	}
	return true
}

// Summary 打印可读的判定摘要。
func (r *Report) Summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "样本 %s：%d 个字段\n", r.Sample, len(r.Fields))
	order := []Verdict{Matched, NotModeled, Untriggered, KnownDeviation, Failed, VerdictUnclassified}
	for _, v := range order {
		if n := r.Counts[v]; n > 0 {
			fmt.Fprintf(&sb, "  %-16s %d\n", v.String(), n)
		}
	}
	names := make([]string, 0, len(r.Verdicts))
	for n := range r.Verdicts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		switch r.Verdicts[n] {
		case Untriggered:
			fmt.Fprintf(&sb, "  ⚠️ %s：值对得上，但本次样本内从未触发 —— 不算通过\n", n)
		case KnownDeviation:
			fmt.Fprintf(&sb, "  ⚠️ %s：已知口子差异 —— 记在账上的欠款，不是一次验收\n", n)
		case Failed:
			fmt.Fprintf(&sb, "  ❌ %s：失败\n", n)
		}
	}
	for _, e := range r.Errs {
		fmt.Fprintf(&sb, "  ❌ %v\n", e)
	}
	return sb.String()
}
