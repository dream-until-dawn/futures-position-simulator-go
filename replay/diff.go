package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Value 保留是否缺失；Present=false与Value为已知零不同。
type Value struct {
	Present bool `json:"present"`
	Value   any  `json:"value"`
}

// Difference 给出最早的业务结果差异；Step=-1表示初始状态/来源不同。
type Difference struct {
	Equal                     bool `json:"equal"`
	Step                      int  `json:"step"`
	LeftEvent, RightEvent     string
	Path                      string `json:"path"`
	Left, Right               Value
	LeftChanges, RightChanges *Changes
	Explanation               string `json:"explanation"`
}

func asJSON(v any) any {
	raw, _ := json.Marshal(v)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var result any
	_ = d.Decode(&result)
	return result
}
func firstDifference(a, b any, path string) (string, Value, Value, bool) {
	if reflect.DeepEqual(a, b) {
		return "", Value{}, Value{}, false
	}
	am, aok := a.(map[string]any)
	bm, bok := b.(map[string]any)
	if aok && bok {
		set := map[string]bool{}
		for k := range am {
			set[k] = true
		}
		for k := range bm {
			set[k] = true
		}
		keys := make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			av, ap := am[k]
			bv, bp := bm[k]
			next := path + "/" + strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
			if !ap || !bp {
				return next, Value{ap, av}, Value{bp, bv}, true
			}
			if p, l, r, diff := firstDifference(av, bv, next); diff {
				return p, l, r, true
			}
		}
	}
	aa, aok := a.([]any)
	ba, bok := b.([]any)
	if aok && bok {
		n := len(aa)
		if len(ba) < n {
			n = len(ba)
		}
		for i := 0; i < n; i++ {
			if p, l, r, diff := firstDifference(aa[i], ba[i], fmt.Sprintf("%s/%d", path, i)); diff {
				return p, l, r, true
			}
		}
		if len(aa) != len(ba) {
			var av, bv any
			if n < len(aa) {
				av = aa[n]
			}
			if n < len(ba) {
				bv = ba[n]
			}
			return fmt.Sprintf("%s/%d", path, n), Value{n < len(aa), av}, Value{n < len(ba), bv}, true
		}
	}
	return path, Value{true, a}, Value{true, b}, true
}

// Compare 先验报告内部一致，再按初始状态与事件顺序找首个差异，不自动豁免任何偏离。
func Compare(a, b Report) (Difference, error) {
	for _, r := range []Report{a, b} {
		if err := validateReport(r); err != nil {
			return Difference{}, err
		}
		if r.Initial == nil {
			return Difference{}, fmt.Errorf("初始化失败报告没有可比较状态")
		}
	}
	result := Difference{Equal: true, Step: -1}
	for _, item := range []struct {
		name string
		a, b any
	}{{"initial", a.Initial, b.Initial}, {"initial_account", a.InitialAccount, b.InitialAccount}, {"evidence", a.Evidence, b.Evidence}, {"initial_rules", a.InitialRules, b.InitialRules}} {
		if path, l, r, diff := firstDifference(asJSON(item.a), asJSON(item.b), "/"+item.name); diff {
			result.Equal = false
			result.Path, result.Left, result.Right = path, l, r
			result.Explanation = "初始状态或证据等级不同"
			return result, nil
		}
	}
	n := len(a.Frames)
	if len(b.Frames) < n {
		n = len(b.Frames)
	}
	for i := 0; i < n; i++ {
		af, bf := a.Frames[i], b.Frames[i]
		// 优先说明账户结果，再展示导致差异的输入和存档；变化字段保留跨日清零语义。
		for _, item := range []struct {
			name string
			a, b any
		}{{"account", af.Account, bf.Account}, {"statement", af.Statement, bf.Statement}, {"event", af.Event, bf.Event}, {"state", af.State, bf.State}, {"rules", af.Rules, bf.Rules}, {"lifecycle", af.Lifecycle, bf.Lifecycle}, {"risk", af.Risk, bf.Risk}} {
			if path, l, r, diff := firstDifference(asJSON(item.a), asJSON(item.b), fmt.Sprintf("/frames/%d/%s", i, item.name)); diff {
				result.Equal = false
				result.Step = i
				result.LeftEvent, result.RightEvent = af.Event.ID, bf.Event.ID
				result.Path, result.Left, result.Right = path, l, r
				result.LeftChanges, result.RightChanges = &af.Changes, &bf.Changes
				result.Explanation = "首次事件差异；变化为累计字段差额，结算清零不是负手续费"
				return result, nil
			}
		}
	}
	if len(a.Frames) != len(b.Frames) {
		result.Equal = false
		result.Step = n
		result.Path = fmt.Sprintf("/frames/%d", n)
		result.Left.Present = n < len(a.Frames)
		result.Right.Present = n < len(b.Frames)
		if result.Left.Present {
			result.Left.Value = asJSON(a.Frames[n])
		}
		if result.Right.Present {
			result.Right.Value = asJSON(b.Frames[n])
		}
		result.Explanation = "一侧有效事件前缀缺失"
		return result, nil
	}
	if a.Status != b.Status || a.Failure != b.Failure {
		result.Equal = false
		result.Step = n
		result.Path = "/status"
		result.Left = Value{true, map[string]string{"status": a.Status, "failure": a.Failure}}
		result.Right = Value{true, map[string]string{"status": b.Status, "failure": b.Failure}}
		result.Explanation = "执行状态或失败原因不同"
	}
	if result.Equal && a.PayloadSHA256 != b.PayloadSHA256 {
		result.Equal = false
		result.Path = "/payload_sha256"
		result.Left = Value{true, a.PayloadSHA256}
		result.Right = Value{true, b.PayloadSHA256}
		result.Explanation = "结果相同但输入指纹不同"
	}
	return result, nil
}
