package lifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// lifecyclePacket 的证据等级固定placeholder；不是市场规则或实测日期。
func lifecyclePacket(t testing.TB, cal *refdata.Calendar) []byte {
	t.Helper()
	base, plan, _ := scheduleFixture(t)
	rules, err := NewDailyRules(base, plan, 20261028, 2)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = rules.Save(&out, refdata.EvidencePlaceholder, "synthetic-base", time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC), cal); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// TestLifecyclePersistenceRoundTrip 保留原始基准、完整未来台阶、绑定日、资格、来源和未知夜盘。
func TestLifecyclePersistenceRoundTrip(t *testing.T) {
	cal, err := refdata.NewCalendar([]types.TradingDay{20261027, 20261028, 20261029}, []refdata.SessionTable{{Exchange: types.SHFE, Product: "rb", Day: []refdata.Session{{Start: refdata.MustClockTime(9, 0, 0), End: refdata.MustClockTime(15, 0, 0)}}, Night: []refdata.Session{{Start: refdata.MustClockTime(21, 0, 0), End: refdata.MustClockTime(23, 0, 0)}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := lifecyclePacket(t, cal)
	if _, _, _, err = Load(bytes.NewReader(raw), refdata.LoadOptions{}); err == nil {
		t.Fatal("占位生命周期默认被允许")
	}
	rules, loaded, ev, err := Load(bytes.NewReader(raw), refdata.LoadOptions{AllowPlaceholder: true})
	if err != nil {
		t.Fatal(err)
	}
	_, _, id := scheduleFixture(t)
	if ev != refdata.EvidencePlaceholder || rules.Version() != 2 || rules.LifecycleDay() != 20261028 || rules.Investor() != Individual || loaded == nil || !loaded.IsTradingDay(20261029) {
		t.Fatal("生命周期快照元数据或日历丢失")
	}
	// 昨日基准是.1，当日选中.2；只保存daily会把原始基准悄悄改掉。
	baseRates, _ := rules.base.MarginRates(id, types.Speculation)
	activeRates, _ := rules.MarginRates(id, types.Speculation)
	if !baseRates.LongByMoney.Equal(money("0.1")) || !activeRates.LongByMoney.Equal(money("0.2")) || rules.schedule.Source != "synthetic-policy" || len(rules.schedule.Contracts[0].MarginSteps) != 2 {
		t.Fatal("生命周期快照丢失原始基准或完整声明")
	}
	at := time.Date(2026, 10, 28, 21, 30, 0, 0, refdata.CNZone())
	if _, err = loaded.TradingDayAt(at, id.Exchange, id.Product); err == nil {
		t.Fatal("未知夜盘经生命周期存取后变成已知")
	}
	var again bytes.Buffer
	if err = rules.Save(&again, ev, "synthetic-base", time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC), loaded); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, again.Bytes()) {
		t.Fatal("生命周期快照往返不确定")
	}
	// 返回报告是副本；外部修改不能影响已绑定规则。
	report, err := rules.Assess(Holdings{Known: true})
	if err != nil {
		t.Fatal(err)
	}
	report.Contracts[0].LastHoldingDay = 20261027
	if err = rules.CheckOpening(id); err != nil {
		t.Fatal("报告暴露内部期限")
	}
}

// TestLifecyclePersistenceRejectsMalformed 用结构化坏输入逐项检出，不能只靠任意报错充数。
func TestLifecyclePersistenceRejectsMalformed(t *testing.T) {
	raw := lifecyclePacket(t, nil)
	for _, kind := range []string{"format", "day", "version", "investor", "missing", "unknown", "case", "null", "decimal-null", "decimal-number", "decimal-exponent", "decimal-budget", "base-case", "base-null", "base-missing", "base-budget", "base-source", "base-time", "base-duplicate"} {
		t.Run(kind, func(t *testing.T) {
			value, err := parseJSON(raw)
			if err != nil {
				t.Fatal(err)
			}
			m := value.(map[string]any)
			schedule := m["schedule"].(map[string]any)
			base := m["base"].(map[string]any)
			step := schedule["Contracts"].([]any)[0].(map[string]any)["MarginSteps"].([]any)[0].(map[string]any)["Rates"].(map[string]any)
			inst := base["instruments"].([]any)[0].(map[string]any)
			want := ""
			switch kind {
			case "format":
				m["format"] = json.Number("9")
				want = "版本不支持"
			case "day":
				m["day"] = json.Number("20261031")
				want = "覆盖"
			case "version":
				m["version"] = json.Number("1")
				want = "版本无效/复用"
			case "investor":
				schedule["Investor"] = json.Number("0")
				want = "资格或来源未知"
			case "missing":
				delete(schedule, "Source")
				want = "缺少字段"
			case "unknown":
				m["allow_placeholder"] = true
				want = "未知或大小写"
			case "case":
				m["Day"] = m["day"]
				delete(m, "day")
				want = "缺少字段"
			case "null":
				schedule["Investor"] = nil
				want = "不可为null"
			case "decimal-null":
				step["LongByMoney"] = nil
				want = "定点字符串"
			case "decimal-number":
				step["LongByMoney"] = json.Number("0.1")
				want = "定点字符串"
			case "decimal-exponent":
				step["LongByMoney"] = "1e1000"
				want = "定点字符串"
			case "decimal-budget":
				step["LongByMoney"] = strings.Repeat("9", 200)
				want = "有限精度"
			case "base-case":
				inst["Is_Trading"] = inst["is_trading"]
				delete(inst, "is_trading")
				want = "大小写不符"
			case "base-null":
				inst["is_trading"] = nil
				want = "基准结构不完整"
			case "base-missing":
				delete(inst, "is_trading")
				want = "基准结构不完整"
			case "base-budget":
				inst["volume_multiple"] = "1e1000"
				want = "定点字符串"
			case "base-duplicate":
				entries := base["rates"].([]any)
				base["rates"] = append(entries, entries[0])
				want = "重复基准费率"
			case "base-source":
				base["source"] = " "
				want = "来源或生成时间未知"
			case "base-time":
				base["generated_at"] = "no-time"
				want = "来源或生成时间未知"
			}
			bad, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			rules, cal, _, err := Load(bytes.NewReader(bad), refdata.LoadOptions{AllowPlaceholder: true})
			if err == nil || rules != nil || cal != nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("生命周期存取反例未由目标拒绝 kind=%s err=%v want=%s", kind, err, want)
			}
		})
	}
	for kind, bad := range map[string][]byte{"duplicate": bytes.Replace(raw, []byte(`"format": 1`), []byte(`"format": 1,"format": 1`), 1), "trailing": append(append([]byte(nil), raw...), []byte("{}")...), "oversize": bytes.Repeat([]byte(" "), MaxSnapshotBytes+1), "depth": []byte(strings.Repeat("[", 34) + strings.Repeat("]", 34)), "list": []byte("[" + strings.Repeat("0,", 4096) + "0]"), "scalar": []byte(`"` + strings.Repeat("a", 1025) + `"`)} {
		want := map[string]string{"duplicate": "重复字段", "trailing": "尾随", "oversize": "字节上限", "depth": "嵌套超限", "list": "列表超限", "scalar": "标量超限"}[kind]
		if rules, _, _, err := Load(bytes.NewReader(bad), refdata.LoadOptions{AllowPlaceholder: true}); err == nil || rules != nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("生命周期JSON边界未拒绝 kind=%s err=%v want=%s", kind, err, want)
		}
	}
}

type brokenRuleReader struct{}

func (brokenRuleReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

type brokenRuleWriter struct{ short bool }

func (w brokenRuleWriter) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return 0, io.ErrClosedPipe
}

// TestLifecyclePersistenceIO 读写错误与保存前错误均明确返回，不产出伪可用规则。
func TestLifecyclePersistenceIO(t *testing.T) {
	base, plan, _ := scheduleFixture(t)
	rules, err := NewDailyRules(base, plan, 20261027, 2)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	for _, input := range []io.Reader{nil, brokenRuleReader{}} {
		if r, _, _, err := Load(input, refdata.LoadOptions{}); err == nil || r != nil {
			t.Fatal("生命周期读取错误被吞掉")
		}
	}
	for _, w := range []brokenRuleWriter{{}, {short: true}} {
		err = rules.Save(w, refdata.EvidencePlaceholder, "synthetic", stamp, nil)
		if (!w.short && !errors.Is(err, io.ErrClosedPipe)) || (w.short && !errors.Is(err, io.ErrShortWrite)) {
			t.Fatalf("生命周期writer错误被吞掉 short=%v err=%v", w.short, err)
		}
	}
	var out bytes.Buffer
	for _, invalid := range []*DailyRules{nil, {}} {
		if err = invalid.Save(&out, refdata.EvidencePlaceholder, "synthetic", stamp, nil); err == nil || out.Len() != 0 {
			t.Fatal("未知规则被保存")
		}
	}
	for _, kind := range []string{"source", "time", "evidence", "writer"} {
		source, ev, at := "synthetic", refdata.EvidencePlaceholder, stamp
		var w io.Writer = &out
		if kind == "source" {
			source = " "
		}
		if kind == "time" {
			at = time.Time{}
		}
		if kind == "evidence" {
			ev = "invented"
		}
		if kind == "writer" {
			w = nil
		}
		if err = rules.Save(w, ev, source, at, nil); err == nil || out.Len() != 0 {
			t.Fatalf("保存前错误仍输出内容 kind=%s", kind)
		}
	}
}
