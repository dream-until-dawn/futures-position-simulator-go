package lifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

var decimalKind = reflect.TypeOf(decimal.Decimal{})
var rawKind = reflect.TypeOf(json.RawMessage{})

// readJSON 保留数字类型并拒绝重复键；不能让后来的值覆盖先前的规则声明。
func readJSON(d *json.Decoder, depth int) (any, error) {
	if depth > 32 {
		return nil, fmt.Errorf("规则JSON嵌套超限")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	if s, ok := token.(string); ok && len(s) > 1024 {
		return nil, fmt.Errorf("规则JSON标量超限")
	}
	switch token {
	case json.Delim('{'):
		out := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok || len(name) > 1024 || len(out) >= 4096 {
				return nil, fmt.Errorf("规则JSON对象超限")
			}
			if _, duplicate := out[name]; duplicate {
				return nil, fmt.Errorf("规则JSON重复字段: %s", name)
			}
			v, err := readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			out[name] = v
		}
		_, err = d.Token()
		return out, err
	case json.Delim('['):
		out := []any{}
		for d.More() {
			if len(out) >= 4096 {
				return nil, fmt.Errorf("规则JSON列表超限")
			}
			v, err := readJSON(d, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		_, err = d.Token()
		return out, err
	}
	return token, nil
}

// parseJSON 在分配业务对象之前落实字节、深度、重复字段与尾随文档边界。
func parseJSON(raw []byte) (any, error) {
	if len(raw) > MaxSnapshotBytes {
		return nil, fmt.Errorf("生命周期快照超过字节上限")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := readJSON(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, fmt.Errorf("规则JSON尾随内容或未完整结束")
	}
	return v, nil
}

// checkJSON 检查声明字段的精确大小写、必填项和null；金额禁止浮点/指数文本。
func checkJSON(v any, t reflect.Type) error {
	if t == rawKind {
		return nil
	}
	if t == decimalKind {
		s, ok := v.(string)
		if !ok || len(s) > 256 || strings.ContainsAny(s, "eE+") {
			return fmt.Errorf("金额必须是有限定点字符串")
		}
		d, err := decimal.NewFromString(s)
		if err != nil || !finite(d) {
			return fmt.Errorf("金额超出有限精度")
		}
		return nil
	}
	if v == nil {
		return fmt.Errorf("生命周期声明字段不可为null")
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("生命周期声明需要对象")
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
			if _, ok := m[name]; !ok {
				return fmt.Errorf("生命周期缺少字段: %s", name)
			}
		}
		for k, value := range m {
			ft, ok := fields[k]
			if !ok {
				return fmt.Errorf("未知或大小写不符字段: %s", k)
			}
			if err := checkJSON(value, ft); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	case reflect.Slice:
		a, ok := v.([]any)
		if !ok {
			return fmt.Errorf("生命周期声明需要数组")
		}
		for _, x := range a {
			if err := checkJSON(x, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// jsonShape 抽取完整结构，不比较金额文案；数组保留允许的条目结构集合。
// 原始基准由refdata加载并重新保存后比较结构，堵住旧加载器的大小写/null/缺字段宽容。
func jsonShape(v any) string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			quoted, _ := json.Marshal(key)
			parts = append(parts, string(quoted)+":"+jsonShape(x[key]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		set := map[string]bool{}
		for _, item := range x {
			set[jsonShape(item)] = true
		}
		parts := make([]string, 0, len(set))
		for item := range set {
			parts = append(parts, item)
		}
		sort.Strings(parts)
		return "[" + strings.Join(parts, ",") + "]"
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	}
	return "invalid"
}

// checkBaseMoney 先限制原始基准金额，不能先交给旧十进制加载器构造无界数。
func checkBaseMoney(v any) error {
	// 一个规范合约/投保档位只能声明一次；旧Builder的覆盖语义不能洗掉输入歧义。
	if root, ok := v.(map[string]any); ok {
		if entries, ok := root["rates"].([]any); ok {
			seen := map[string]bool{}
			for _, entry := range entries {
				row, ok := entry.(map[string]any)
				if !ok {
					return fmt.Errorf("基准费率记录格式未知")
				}
				key, _ := json.Marshal([]any{row["exchange"], row["product"], row["year"], row["month"], row["hedge_flag"]})
				if seen[string(key)] {
					return fmt.Errorf("重复基准费率声明")
				}
				seen[string(key)] = true
			}
		}
	}
	money := map[string]bool{"volume_multiple": true, "price_tick": true, "price_limit_ratio": true, "long_by_money": true, "long_by_volume": true, "short_by_money": true, "short_by_volume": true, "company_add_on": true, "open_by_money": true, "open_by_volume": true, "close_by_money": true, "close_by_volume": true, "close_today_by_money": true, "close_today_by_volume": true}
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, value := range x {
				if k != strings.ToLower(k) {
					return fmt.Errorf("基准字段大小写不符: %s", k)
				}
				if money[k] {
					if err := checkJSON(value, decimalKind); err != nil {
						return err
					}
				}
				if err := walk(value); err != nil {
					return err
				}
			}
		case []any:
			for _, value := range x {
				if err := walk(value); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(v)
}
