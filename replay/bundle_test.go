package replay

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestBundleBindingAndStrictJSON(t *testing.T) {
	p := demoPayload(t)
	raw, err := Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !same(execute(t, p), execute(t, decoded)) {
		t.Fatal("自包含包往返改变结果")
	}
	var packet map[string]json.RawMessage
	_ = json.Unmarshal(raw, &packet)
	var rearranged map[string]json.RawMessage
	_ = json.Unmarshal(packet["payload"], &rearranged)
	packet["payload"], _ = json.Marshal(rearranged)
	reordered, _ := json.Marshal(packet)
	normalized, err := Decode(bytes.NewReader(reordered))
	if err != nil {
		t.Fatal(err)
	}
	var header envelope
	_ = json.Unmarshal(raw, &header)
	if execute(t, normalized).PayloadSHA256 != header.SHA256 {
		t.Fatal("包与报告输入指纹不一致")
	}
	for _, pair := range [][2]string{{`"pre_balance": "1000000"`, `"pre_balance": "900000"`}, {`"rounding": 1`, `"rounding": 2`}, {`"volume": 100`, `"volume": 99`}, {`20261027`, `20261028`}, {`"version": 1`, `"version": 2`}} {
		if !bytes.Contains(raw, []byte(pair[0])) {
			t.Fatalf("篡改前提缺失: %s", pair[0])
		}
		bad := bytes.Replace(raw, []byte(pair[0]), []byte(pair[1]), 1)
		if _, err := Decode(bytes.NewReader(bad)); err == nil {
			t.Fatal("篡改包未被哈希拒绝")
		}
	}
	cases := [][]byte{
		bytes.Replace(raw, []byte(`"format": 1`), []byte(`"format": 2`), 1),
		bytes.Replace(raw, []byte(`"format": 1`), []byte(`"format": 1,"format": 1`), 1),
		bytes.Replace(raw, []byte(`"format": 1`), []byte(`"Format": 1`), 1),
		bytes.Replace(raw, []byte(`"format": 1`), []byte(`"format": 1,"Format": 1`), 1),
		append(append([]byte(nil), raw...), []byte(` {}`)...),
		[]byte(`{"format":1,"format":1,"sha256":"x","payload":{}}`),
		[]byte(`{"format":1,"Format":1,"sha256":"x","payload":{}}`),
		raw[:len(raw)/2],
		[]byte(strings.Repeat("[", 66) + strings.Repeat("]", 66)),
		bytes.Repeat([]byte(" "), MaxInputBytes+1),
	}
	for _, badAmount := range []string{`"pre_balance": 1000000`, `"pre_balance": "1e6"`} {
		bad := bytes.Replace(raw, []byte(`"pre_balance": "1000000"`), []byte(badAmount), 1)
		if _, err := Decode(bytes.NewReader(bad)); err == nil {
			t.Fatal("金额类型或指数绕过规范指纹")
		}
	}
	if !bytes.Contains(raw, []byte(`"UndatedCloseOnUseHistory": 0`)) {
		t.Fatal("非空零值反例前提缺失")
	}
	nullConfig := bytes.Replace(raw, []byte(`"UndatedCloseOnUseHistory": 0`), []byte(`"UndatedCloseOnUseHistory": null`), 1)
	if _, err := Decode(bytes.NewReader(nullConfig)); err == nil {
		t.Fatal("非空字段null被解释成零")
	}
	for i, bad := range cases {
		if _, err := Decode(bytes.NewReader(bad)); err == nil {
			t.Fatalf("严格解码未拒绝坏输入%d", i)
		}
	}
	// 即使重新生成匹配哈希，金额类型、指数、未知字段及缺少显式配置仍不能绕过格式检查。
	for _, replacement := range []string{`"pre_balance":1000000`, `"pre_balance":"1e1000000"`, `"pre_balance":"0","typo":1`, `"unused_balance":"0"`} {
		payload, _ := json.Marshal(p)
		payload = bytes.Replace(payload, []byte(`"pre_balance":"1000000"`), []byte(replacement), 1)
		body, _ := compact(payload)
		packet, _ := json.Marshal(envelope{Format: Format, SHA256: fingerprint(body), Payload: body})
		if _, err := Decode(bytes.NewReader(packet)); err == nil {
			t.Fatal("金额或字段格式失效")
		}
	}
	// 规则的指数金额也须在传入decimal计算前拒绝，而不是把它当零或占位。
	p.Rules = bytes.Replace(p.Rules, []byte(`"price_tick": "1"`), []byte(`"price_tick": "1e1"`), 1)
	p.Rules = bytes.Replace(p.Rules, []byte(`"price_tick"`), []byte(`"PRICE_TICK"`), 1)
	if _, err := Run(p, Options{AllowPlaceholder: true}); err == nil {
		t.Fatal("规则指数金额未拒绝")
	}
}

func FuzzDecodeBundle(f *testing.F) {
	p, err := Demo()
	if err != nil {
		f.Fatal(err)
	}
	raw, err := Encode(p)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(raw)
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			return
		}
		p, err := Decode(bytes.NewReader(raw))
		if err == nil {
			encoded, err := Encode(p)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = Decode(bytes.NewReader(encoded)); err != nil {
				t.Fatal(err)
			}
		}
	})
}
