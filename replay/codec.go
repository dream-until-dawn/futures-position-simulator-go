package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/shopspring/decimal"
	"io"
	"reflect"
	"regexp"
	"strings"
)

var fixedMoney = regexp.MustCompile(`^-?[0-9]{1,24}(\.[0-9]{1,12})?$`)
var reportMoney = regexp.MustCompile(`^-?[0-9]{1,128}(\.[0-9]{1,128})?$`)
var decimalType = reflect.TypeOf(decimal.Decimal{})
var rawType = reflect.TypeOf(json.RawMessage{})

// strictDecode 同时防重复键、尾随文档、大小/深度失控、大小写别名与浮点金额。
func strictDecode(raw []byte, target any) error {
	return strictDecodeSized(raw, target, MaxInputBytes)
}

func strictDecodeSized(raw []byte, target any, limit int) error {
	if len(raw) > limit {
		return fmt.Errorf("JSON超过输入上限")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	value, err := readValue(dec, 0)
	if err != nil {
		return err
	}
	if _, err = dec.Token(); err != io.EOF {
		return fmt.Errorf("JSON尾随内容或不完整: %v", err)
	}
	if err = checkShape(value, reflect.TypeOf(target).Elem(), limit == MaxReportBytes); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return fmt.Errorf("JSON类型无效: %w", err)
	}
	return nil
}

func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("JSON嵌套超过上限")
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	if s, ok := token.(string); ok && len(s) > 1024 {
		return nil, fmt.Errorf("JSON标量过长")
	}
	switch token {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok || len(name) > 1024 {
				return nil, fmt.Errorf("JSON键无效")
			}
			if _, ok = m[name]; ok {
				return nil, fmt.Errorf("JSON重复键: %s", name)
			}
			v, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			m[name] = v
		}
		_, err = d.Token()
		return m, err
	case json.Delim('['):
		var list []any
		for d.More() {
			v, err := readValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		_, err = d.Token()
		return list, err
	}
	return token, nil
}

func checkShape(value any, t reflect.Type, wide bool) error {
	if t == rawType {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		if value == nil {
			return nil
		}
		return checkShape(value, t.Elem(), wide)
	}
	if t == decimalType {
		s, ok := value.(string)
		pattern := fixedMoney
		if wide {
			pattern = reportMoney
		}
		if !ok || !pattern.MatchString(s) {
			return fmt.Errorf("金额必须是有界定点字符串")
		}
		return nil
	}
	// encoding/json会把null解到整数/布尔/字符串的零值，不能把未知洗成显式配置。
	if value == nil && t.Kind() != reflect.Slice {
		return fmt.Errorf("非可空字段收到null")
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("应为JSON对象: %s", t)
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			name := strings.Split(tag, ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
			if _, ok := m[name]; !ok && !strings.Contains(tag, ",omitempty") {
				return fmt.Errorf("缺少字段: %s", name)
			}
		}
		for name, v := range m {
			ft, ok := fields[name]
			if !ok {
				return fmt.Errorf("未知或大小写不符字段: %s", name)
			}
			if err := checkShape(v, ft, wide); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	case reflect.Slice, reflect.Array:
		if value == nil {
			return nil
		}
		a, ok := value.([]any)
		if !ok {
			return fmt.Errorf("应为JSON数组")
		}
		for _, v := range a {
			if err := checkShape(v, t.Elem(), wide); err != nil {
				return err
			}
		}
	}
	return nil
}
