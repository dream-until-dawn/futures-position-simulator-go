package ctp

import "sync"

// ⚠️ 本文件**不带 build tag**：这本簿子只是一个 map，没有任何平台依赖，
// 而留在 `_windows.go` 里意味着它的守卫只在一种机器上跑。

// orderBook 记本次运行发出去的委托。
type orderBook struct {
	mu sync.Mutex
	m  map[string]*OrderState
}

func (b *orderBook) put(ref string, f func(*OrderState)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]*OrderState{}
	}
	s, ok := b.m[ref]
	if !ok {
		s = &OrderState{OrderRef: ref}
		b.m[ref] = s
	}
	f(s)
}

// reset 把 ref 这一格清成「刚发出去、还没有任何回报」。
func (b *orderBook) reset(ref string, volume int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]*OrderState{}
	}
	b.m[ref] = &OrderState{OrderRef: ref, VolumeTotal: volume}
}

func (b *orderBook) get(ref string) (OrderState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.m[ref]
	if !ok {
		return OrderState{}, false
	}
	return *s, true
}
