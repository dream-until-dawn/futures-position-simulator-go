package replay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"io"
	"strings"
)

type envelope struct {
	Format  int             `json:"format"`
	SHA256  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}

func compact(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	err := json.Compact(&b, raw)
	return b.Bytes(), err
}
func fingerprint(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

// Encode 生成可移动的自包含回放包；允许编码合成规则，但Run默认仍拒绝占位证据。
func Encode(p Payload) ([]byte, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	var checked Payload
	if err = strictDecode(raw, &checked); err != nil {
		return nil, err
	}
	raw, err = compact(raw)
	if err != nil {
		return nil, err
	}
	result, err := json.MarshalIndent(envelope{Format: Format, SHA256: fingerprint(raw), Payload: raw}, "", "  ")
	if len(result) > MaxInputBytes {
		return nil, fmt.Errorf("回放包超过输入上限")
	}
	return append(result, '\n'), err
}

// Decode 检查完整信封、字段与哈希；规则内容和开户前提仍由Run验证。
func Decode(r io.Reader) (Payload, error) {
	var p Payload
	raw, err := io.ReadAll(io.LimitReader(r, MaxInputBytes+1))
	if err != nil {
		return p, err
	}
	var e envelope
	if err = strictDecode(raw, &e); err != nil {
		return p, err
	}
	if e.Format != Format {
		return p, fmt.Errorf("回放格式不支持: %d", e.Format)
	}
	if err = strictDecode(e.Payload, &p); err != nil {
		return p, err
	}
	// 指纹按格式1的类型化JSON重编码；顶层字段重排和金额等值表示不改变解释。
	// Rules是既有线格式的RawMessage，其对象内顺序只压缩空白，不擅改来源数据。
	body, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	if e.SHA256 != fingerprint(body) {
		return p, fmt.Errorf("回放包SHA256不匹配")
	}
	return p, nil
}

// loadRules 先约束所有规则金额，避免指数形式制造无界十进制计算。
func loadRules(raw []byte, opt Options) (*refdata.Snapshot, *refdata.Calendar, refdata.Evidence, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	value, err := readValue(d, 0)
	if err != nil {
		return nil, nil, "", err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, nil, "", fmt.Errorf("规则JSON尾随内容")
	}
	money := map[string]bool{"volume_multiple": true, "price_tick": true, "price_limit_ratio": true, "long_by_money": true, "long_by_volume": true, "short_by_money": true, "short_by_volume": true, "company_add_on": true, "open_by_money": true, "open_by_volume": true, "close_by_money": true, "close_by_volume": true, "close_today_by_money": true, "close_today_by_volume": true}
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				if k != strings.ToLower(k) {
					return fmt.Errorf("规则字段大小写不规范: %s", k)
				}
				if money[k] {
					s, ok := v.(string)
					if !ok || !fixedMoney.MatchString(s) {
						return fmt.Errorf("规则金额必须是有界定点字符串: %s", k)
					}
				}
				if err := walk(v); err != nil {
					return err
				}
			}
		case []any:
			for _, v := range x {
				if err := walk(v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err = walk(value); err != nil {
		return nil, nil, "", err
	}
	return refdata.Load(bytes.NewReader(raw), refdata.LoadOptions{AllowPlaceholder: opt.AllowPlaceholder})
}
