package lifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

const (
	// PersistenceFormat 是完整生命周期规则的存储版本，不是内核StateFormat。
	PersistenceFormat = 1
	// MaxSnapshotBytes 是生命周期文件读取/写入的总预算。
	MaxSnapshotBytes = 16 << 20
)

type wireRules struct {
	Format   int              `json:"format"`
	Day      types.TradingDay `json:"day"`
	Version  int64            `json:"version"`
	Schedule Schedule         `json:"schedule"`
	Base     json.RawMessage  `json:"base"`
}

// Save 保存原始基准、完整声明和日绑定；证据等级由调用者显式声明，不认证外部资料真实性。
// 先在内存验证完整文件，再一次写入；writer部分写失败仍返回错误，调用方负责原子发布。
func (r *DailyRules) Save(w io.Writer, ev refdata.Evidence, source string, generatedAt time.Time, cal *refdata.Calendar) error {
	if r == nil || r.data == nil || r.base == nil {
		return fmt.Errorf("生命周期规则未知，不能保存")
	}
	if w == nil || strings.TrimSpace(source) == "" || generatedAt.IsZero() {
		return fmt.Errorf("生命周期保存缺writer/来源/生成时间")
	}
	var base bytes.Buffer
	if err := r.base.Save(&base, ev, source, generatedAt, cal); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(wireRules{Format: PersistenceFormat, Day: r.day, Version: r.Version(), Schedule: r.schedule, Base: base.Bytes()}, "", "  ")
	if err != nil {
		return err
	}
	if _, _, _, err = Load(bytes.NewReader(raw), refdata.LoadOptions{AllowPlaceholder: true}); err != nil {
		return fmt.Errorf("生命周期保存前复核: %w", err)
	}
	raw = append(raw, '\n')
	if len(raw) > MaxSnapshotBytes {
		return fmt.Errorf("生命周期快照超过字节上限")
	}
	n, err := w.Write(raw)
	if err == nil && n != len(raw) {
		err = io.ErrShortWrite
	}
	return err
}

// Load 严格恢复完整日规则；AllowPlaceholder只能由调用方给，输入文件不能自行开启。
func Load(r io.Reader, opt refdata.LoadOptions) (*DailyRules, *refdata.Calendar, refdata.Evidence, error) {
	if r == nil {
		return nil, nil, "", fmt.Errorf("生命周期输入reader未知")
	}
	raw, err := io.ReadAll(io.LimitReader(r, MaxSnapshotBytes+1))
	if err != nil {
		return nil, nil, "", err
	}
	value, err := parseJSON(raw)
	if err != nil {
		return nil, nil, "", err
	}
	var in wireRules
	if err = checkJSON(value, reflect.TypeOf(in)); err != nil {
		return nil, nil, "", err
	}
	if err = json.Unmarshal(raw, &in); err != nil {
		return nil, nil, "", err
	}
	if in.Format != PersistenceFormat {
		return nil, nil, "", fmt.Errorf("生命周期存储版本不支持")
	}
	baseValue, err := parseJSON(in.Base)
	if err != nil {
		return nil, nil, "", err
	}
	if err = checkBaseMoney(baseValue); err != nil {
		return nil, nil, "", err
	}
	base, cal, ev, err := refdata.Load(bytes.NewReader(in.Base), opt)
	if err != nil {
		return nil, nil, ev, err
	}
	metadata, ok := baseValue.(map[string]any)
	if !ok {
		return nil, nil, ev, fmt.Errorf("基准不是对象")
	}
	source, _ := metadata["source"].(string)
	stamp, _ := metadata["generated_at"].(string)
	generatedAt, err := time.Parse(time.RFC3339, stamp)
	if err != nil || generatedAt.IsZero() || strings.TrimSpace(source) == "" {
		return nil, nil, ev, fmt.Errorf("生命周期基准来源或生成时间未知")
	}
	var canonical bytes.Buffer
	if err = base.Save(&canonical, ev, source, generatedAt, cal); err != nil {
		return nil, nil, ev, err
	}
	canonicalValue, err := parseJSON(canonical.Bytes())
	if err != nil {
		return nil, nil, ev, err
	}
	if jsonShape(baseValue) != jsonShape(canonicalValue) {
		return nil, nil, ev, fmt.Errorf("基准结构不完整、大小写别名或null改变声明")
	}
	rules, err := NewDailyRules(base, in.Schedule, in.Day, in.Version)
	if err != nil {
		return nil, nil, ev, err
	}
	return rules, cal, ev, nil
}
