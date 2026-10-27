package lifecycle

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
)

// FuzzLifecycleRules 对完整快照与未知输入施压；成功加载必须能重新保存并保持日规则报告。
func FuzzLifecycleRules(f *testing.F) {
	f.Add(lifecyclePacket(f, nil))
	f.Add([]byte(`{}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{"format":1,"format":1}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			return
		}
		rules, cal, ev, err := Load(bytes.NewReader(raw), refdata.LoadOptions{AllowPlaceholder: true})
		if err != nil {
			return
		}
		before, err := rules.Assess(Holdings{Known: true})
		if err != nil {
			t.Fatal(err)
		}
		value, err := parseJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		base := value.(map[string]any)["base"].(map[string]any)
		source := base["source"].(string)
		stamp, err := time.Parse(time.RFC3339, base["generated_at"].(string))
		if err != nil {
			t.Fatal(err)
		}
		var again bytes.Buffer
		if err = rules.Save(&again, ev, source, stamp, cal); err != nil {
			t.Fatalf("成功加载的规则无法再保存: %v", err)
		}
		loaded, _, _, err := Load(&again, refdata.LoadOptions{AllowPlaceholder: true})
		if err != nil {
			t.Fatal(err)
		}
		after, err := loaded.Assess(Holdings{Known: true})
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(before)
		b, _ := json.Marshal(after)
		if !bytes.Equal(a, b) || loaded.Version() != rules.Version() {
			t.Fatal("生命周期快照往返改变日规则报告")
		}
	})
}
