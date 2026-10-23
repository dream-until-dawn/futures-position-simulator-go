package order

import (
	"fmt"
	"math"
	"sort"
)

// BookEntry 保存一笔挂单及其本地接收序号，用于显式恢复，不推断柜台排队顺序。
type BookEntry struct {
	ID       string
	Request  Request
	Frozen   Frozen
	Sequence uint64
}

// Sequence 返回挂单的本地接收序号。
func (b *Book) Sequence(id string) (uint64, bool) { e, ok := b.live[id]; return e.sequence, ok }

// LastSequence 返回已分配序号的高水位，空簿也保留。
func (b *Book) LastSequence() uint64 { return b.lastSequence }

// OrderedIDs 按成功接收顺序返回挂单编号，不改变 Live 的字典序约定。
func (b *Book) OrderedIDs() []string {
	ids := b.Live()
	sort.Slice(ids, func(i, j int) bool { return b.live[ids[i]].sequence < b.live[ids[j]].sequence })
	return ids
}

// Reinsert 用原序号放回失败的成交；调用方负责提供真实历史，普通挂单必须用 Insert。
func (b *Book) Reinsert(id string, req Request, in FreezeInput, sequence uint64) error {
	if sequence == 0 || sequence > b.lastSequence {
		return fmt.Errorf("回滚序号 %d 超出高水位 %d", sequence, b.lastSequence)
	}
	for _, e := range b.live {
		if e.sequence == sequence {
			return fmt.Errorf("重复挂单序号 %d", sequence)
		}
	}
	return b.insertAt(id, req, in, sequence)
}

// RestoreBook 构造新簿并验证全部显式序号及冻结；失败不修改输入。
func RestoreBook(lastSequence uint64, entries []BookEntry) (*Book, error) {
	b := NewBook()
	b.lastSequence = lastSequence
	for _, e := range entries {
		in := FreezeInput{Margin: e.Frozen.Margin, Commission: e.Frozen.Commission, UndatedToday: e.Frozen.VolumeToday, UndatedHistory: e.Frozen.VolumeHistory}
		if err := b.Reinsert(e.ID, e.Request, in, e.Sequence); err != nil {
			return nil, err
		}
		_, got, _ := b.Get(e.ID)
		if !got.Margin.Equal(e.Frozen.Margin) || !got.Commission.Equal(e.Frozen.Commission) || got.VolumeToday != e.Frozen.VolumeToday || got.VolumeHistory != e.Frozen.VolumeHistory {
			return nil, fmt.Errorf("挂单 %s 冻结与按开平标志重算不一致", e.ID)
		}
	}
	return b, nil
}

// nextSequence 在写入之前检查耗尽，避免 uint64 回绕成零。
func (b *Book) nextSequence() (uint64, error) {
	if b.lastSequence == math.MaxUint64 {
		return 0, fmt.Errorf("挂单序号已耗尽")
	}
	return b.lastSequence + 1, nil
}
