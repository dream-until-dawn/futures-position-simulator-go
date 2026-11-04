package replay

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
)

// TestTickOutwardBundle 保持整数线格式，验证往返后仍能经过真实配置入口。
func TestTickOutwardBundle(t *testing.T) {
	for _, rounding := range []refdata.TickRounding{1, 2, 3, 4, 5, 6} {
		p := demoPayload(t)
		p.Settings.TickRounding[0].Rounding = rounding
		p.Events = nil
		raw, err := Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Settings.TickRounding[0].Rounding != rounding {
			t.Fatal("取整往返改变")
		}
		wire, err := json.Marshal(decoded.Settings.TickRounding[0])
		if err != nil {
			t.Fatal(err)
		}
		var number struct {
			Rounding int `json:"rounding"`
		}
		if err = json.Unmarshal(wire, &number); err != nil || number.Rounding != int(rounding) {
			t.Fatal("取整整数线格式改变")
		}
		if _, _, err := configuration(decoded, Options{AllowPlaceholder: true}); err != nil {
			t.Fatalf("取整配置未接受 %d: %v", rounding, err)
		}
	}
	p := demoPayload(t)
	p.Settings.TickRounding[0].Rounding = 7
	if _, _, err := configuration(p, Options{AllowPlaceholder: true}); err == nil {
		t.Fatal("配置接受未知取整")
	}
}
