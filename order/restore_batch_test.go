package order

import (
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"reflect"
	"testing"
)

// TestBatchRestoreSequenceChecks 验证无序/有空洞的序号不被重排，重复、越界与冻结篡改不能绕过批量路径。
func TestBatchRestoreSequenceChecks(t *testing.T) {
	req := Request{rb2701(t), types.Buy, types.Open, types.Speculation, d("3163"), 1}
	fr, err := FreezeOf(req, openIn())
	if err != nil {
		t.Fatal(err)
	}
	entries := []BookEntry{{ID: "z", Request: req, Frozen: fr, Sequence: 7}, {ID: "a", Request: req, Frozen: fr, Sequence: 2}}
	b, err := RestoreBook(12, entries)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.OrderedIDs(), []string{"a", "z"}) || b.LastSequence() != 12 {
		t.Fatal("批量恢复改变序号")
	}
	for _, tc := range []struct {
		name   string
		change func([]BookEntry)
	}{
		{"zero", func(e []BookEntry) { e[1].Sequence = 0 }},
		{"overflow", func(e []BookEntry) { e[1].Sequence = 13 }},
		{"duplicate-sequence", func(e []BookEntry) { e[1].Sequence = 7 }},
		{"duplicate-id", func(e []BookEntry) { e[1].ID = "z" }},
		{"invalid-freeze", func(e []BookEntry) { e[1].Frozen.VolumeToday = 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := append([]BookEntry(nil), entries...)
			tc.change(bad)
			if _, err := RestoreBook(12, bad); err == nil {
				t.Fatal("批量恢复接受非法存档")
			}
			if !reflect.DeepEqual(b.OrderedIDs(), []string{"a", "z"}) {
				t.Fatal("失败污染其他实例")
			}
		})
	}
	entries[0].ID = "mutated"
	entries[0].Sequence = 1
	if _, _, ok := b.Get("z"); !ok {
		t.Fatal("批量恢复引用输入切片")
	}
	if err := b.Insert("next", req, openIn()); err != nil {
		t.Fatal(err)
	}
	if seq, _ := b.Sequence("next"); seq != 13 {
		t.Fatal("批量恢复丢高水位")
	}
}
