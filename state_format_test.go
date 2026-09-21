package futsim

import (
	"crypto/sha256"
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// stateFields 列出 State 在 JSON 里的全部字段路径与叶子类型（State 没有 json tag，键就是 Go 字段名）。
// 实现了 json.Marshaler / encoding.TextMarshaler 的类型（decimal.Decimal 等）当叶子，不往里走。
func stateFields() []string {
	marshaler := reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	texter := reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	var out []string
	var walk func(prefix string, t reflect.Type)
	walk = func(prefix string, t reflect.Type) {
		switch {
		case t.Implements(marshaler) || reflect.PointerTo(t).Implements(marshaler),
			t.Implements(texter) || reflect.PointerTo(t).Implements(texter):
			out = append(out, prefix+" "+t.String())
			return
		}
		switch t.Kind() {
		case reflect.Struct:
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				if !f.IsExported() {
					continue
				}
				walk(prefix+"."+f.Name, f.Type)
			}
		case reflect.Slice, reflect.Array:
			walk(prefix+"[]", t.Elem())
		case reflect.Map:
			walk(prefix+"{}", t.Elem())
		case reflect.Pointer:
			walk(prefix, t.Elem())
		default:
			out = append(out, prefix+" "+t.String())
		}
	}
	walk("State", reflect.TypeOf(State{}))
	sort.Strings(out)
	return out
}

// stateFingerprints 是每个存档格式号对应的字段集合指纹。
//
// ⚠️ 加删改 State（含嵌套的 account.State、position.Lot、order.Request……）里任何一个导出字段，指纹就变：
// 那时要**抬 StateFormat**，并在这里为新格式号添一行 —— 不许只改指纹不抬号（旧存档会被当成新格式读进来，缺的字段静默为零）。
// 格式 1 是回填的：本表 20260918 才建（写于 F10 分支、随 F11 先落进 main），按 b11c600 的字段集合算（在 b11c600 上核过）。
// 格式 2 是 F11：State 加 Quotas。
// 格式 3 是 F10：account.State 加 SettleCommission / CommissionTrades（结算时按笔重算手续费）。
// ⚠️ 旧的 impl-f10 分支（按「逐笔截断」写的，已被 F10 改写取代）曾在它自己的分支上把 2 用给了 SettleCommission —— 那一版不落地。
// ⚠️ F5 到 b11c600 之间格式 1 的字段有没有变过而没抬号，本表回答不了 —— 这张表防的是今后。
var stateFingerprints = map[int]string{
	1: "a2b10ae2455cd8891e10313f00448bba",
	2: "e2605fab03a8811d588cb672f88ffb6e",
	3: "4a379c6808d44940cc3532d5753a94d8",
}

func fingerprint(fields []string) string {
	sum := sha256.Sum256([]byte(strings.Join(fields, "\n")))
	return fmt.Sprintf("%x", sum[:16])
}

// TestStateFormatPinsFieldSet：存档的字段集合变了而 StateFormat 没抬，本条红。
func TestStateFormatPinsFieldSet(t *testing.T) {
	fields := stateFields()
	if len(fields) < 30 {
		t.Fatalf("⚠️ 只走出 %d 个字段 —— 遍历写错了，本条在空转", len(fields))
	}
	if all := strings.Join(fields, "\n"); !strings.Contains(all, "State.Account.SettleCommission ") || !strings.Contains(all, "State.Quotas[].OpenedToday ") {
		t.Fatal("⚠️ 字段表里没有 Account.SettleCommission / Quotas[].OpenedToday —— 遍历没走进嵌套结构")
	}
	want, ok := stateFingerprints[StateFormat]
	if !ok {
		t.Fatalf("⚠️ StateFormat %d 没有登记指纹 —— 抬了号就要在 stateFingerprints 添一行", StateFormat)
	}
	if got := fingerprint(fields); got != want {
		t.Errorf("⚠️ 存档字段集合的指纹 %s ≠ 格式 %d 登记的 %s —— 改了存档里的字段就要抬 StateFormat（不迁移，旧存档报错），"+
			"再为新号登记指纹。当前字段：\n%s", got, StateFormat, want, strings.Join(fields, "\n"))
	}
}

// TestRestoreRefusesOlderFormat：格式 1 / 2 的存档（F11 / F10 之前）在版本号那一步报错，不迁移。
func TestRestoreRefusesOlderFormat(t *testing.T) {
	s := newSim(t)
	st, err := s.State()
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []int{1, 2} {
		st.Format = old
		_, err = Restore(Config{Day: simDay, PreBalance: dec("100000"), Rules: simRules(t), Choices: ctpChoices()}, st)
		if err == nil || !strings.Contains(err.Error(), "不迁移") {
			t.Errorf("⚠️ 格式 %d 的存档应在版本号那一步报错（不迁移），得到 %v", old, err)
		}
	}
}
