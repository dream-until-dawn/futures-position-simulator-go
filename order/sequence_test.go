package order

import (
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"math"
	"reflect"
	"testing"
)

// TestSequenceLifecycle 覆盖字典序与接收序不同、失败不占号及回滚不移到队尾。
func TestSequenceLifecycle(t *testing.T) {
	b := NewBook()
	r := Request{rb2701(t), types.Buy, types.Open, types.Speculation, d("3163"), 1}
	for _, id := range []string{"z", "a", "m"} {
		if err := b.Insert(id, r, openIn()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(b.OrderedIDs(), []string{"z", "a", "m"}) || !reflect.DeepEqual(b.Live(), []string{"a", "m", "z"}) {
		t.Fatal("挂单顺序错误")
	}
	if b.Insert("a", r, openIn()) == nil || b.Insert("", r, openIn()) == nil || b.LastSequence() != 3 {
		t.Fatal("失败消耗序号")
	}
	seq, _ := b.Sequence("a")
	b.Remove("a")
	if err := b.Reinsert("a", r, openIn(), seq); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.OrderedIDs(), []string{"z", "a", "m"}) || b.LastSequence() != 3 {
		t.Fatal("回滚改变顺序")
	}
	for _, id := range b.Live() {
		b.Remove(id)
	}
	b.Insert("a", r, openIn())
	if seq, _ := b.Sequence("a"); seq != 4 {
		t.Fatal("复用编号重置序号")
	}
}

// TestSequenceRestoreRejects 验证存档高水位、重复和耗尽等边界。
func TestSequenceRestoreRejects(t *testing.T) {
	r := Request{rb2701(t), types.Buy, types.Open, types.Speculation, d("3163"), 1}
	f, _ := FreezeOf(r, openIn())
	e := BookEntry{ID: "a", Request: r, Frozen: f, Sequence: 2}
	for _, entries := range [][]BookEntry{{{ID: "a", Request: r, Frozen: f}}, {e, e}, {e, {ID: "b", Request: r, Frozen: f, Sequence: 2}}} {
		if _, err := RestoreBook(2, entries); err == nil {
			t.Fatal("非法序号存档被接受")
		}
	}
	if _, err := RestoreBook(1, []BookEntry{e}); err == nil {
		t.Fatal("接受越界序号")
	}
	b, err := RestoreBook(math.MaxUint64, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b.Insert("a", r, openIn()) == nil || b.LastSequence() != math.MaxUint64 {
		t.Fatal("序号回绕")
	}
	b, err = RestoreBook(8, []BookEntry{e})
	if err != nil {
		t.Fatal(err)
	}
	if err = b.Reinsert("b", r, openIn(), 2); err == nil {
		t.Fatal("回滚接受重复序号")
	}
	if err = b.Reinsert("b", r, openIn(), 9); err == nil {
		t.Fatal("回滚接受越界序号")
	}
	b.Remove("a")
	b.Insert("a", r, openIn())
	if seq, _ := b.Sequence("a"); seq != 9 {
		t.Fatal("恢复丢高水位")
	}
}
