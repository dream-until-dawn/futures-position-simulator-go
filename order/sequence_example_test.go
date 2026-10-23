package order_test

import (
	"fmt"

	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// exampleOpen 提供调用方已算好的冻结输入；Book 自身不负责完整报单校验。
func exampleOpen() (order.Request, order.FreezeInput) {
	return order.Request{
		Instrument: types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1},
		Direction:  types.Buy, Offset: types.Open, Hedge: types.Speculation,
		Price: decimal.NewFromInt(3000), Volume: 1,
	}, order.FreezeInput{Margin: decimal.NewFromInt(3000), Commission: decimal.NewFromInt(2)}
}

// ExampleBook_OrderedIDs 区分显示用的字典序与本地成功接收顺序。
func ExampleBook_OrderedIDs() {
	b := order.NewBook()
	r, in := exampleOpen()
	for _, id := range []string{"z-first", "a-second"} {
		if err := b.Insert(id, r, in); err != nil {
			panic(err)
		}
	}
	fmt.Println("显示顺序:", b.Live())
	fmt.Println("接收顺序:", b.OrderedIDs())
	fmt.Println("拒绝重复:", b.Insert("z-first", r, in) != nil)
	fmt.Println("高水位:", b.LastSequence())
	// 撤单后重新接收是新委托，会取得新序号。
	if _, err := b.Remove("z-first"); err != nil {
		panic(err)
	}
	if err := b.Insert("z-first", r, in); err != nil {
		panic(err)
	}
	fmt.Println("重新接收:", b.OrderedIDs())
	fmt.Println("高水位:", b.LastSequence())
	// Output:
	// 显示顺序: [a-second z-first]
	// 接收顺序: [z-first a-second]
	// 拒绝重复: true
	// 高水位: 2
	// 重新接收: [a-second z-first]
	// 高水位: 3
}

// ExampleRestoreBook 展示数组顺序不决定接收顺序，空簿也必须保存高水位。
func ExampleRestoreBook() {
	r, in := exampleOpen()
	frozen, err := order.FreezeOf(r, in)
	if err != nil {
		panic(err)
	}
	entries := []order.BookEntry{
		{ID: "a-second", Request: r, Frozen: frozen, Sequence: 2},
		{ID: "z-first", Request: r, Frozen: frozen, Sequence: 1},
	}
	b, err := order.RestoreBook(7, entries)
	if err != nil {
		panic(err)
	}
	fmt.Println("恢复顺序:", b.OrderedIDs())
	// 真实应用保存全部条目与 LastSequence；已无挂单时条目为空。
	empty, err := order.RestoreBook(b.LastSequence(), nil)
	if err != nil {
		panic(err)
	}
	if err = empty.Insert("next", r, in); err != nil {
		panic(err)
	}
	sequence, _ := empty.Sequence("next")
	fmt.Println("空簿恢复后:", sequence)
	entries[1].Sequence = 2
	_, err = order.RestoreBook(7, entries)
	fmt.Println("拒绝重复序号:", err != nil)
	// Output:
	// 恢复顺序: [z-first a-second]
	// 空簿恢复后: 8
	// 拒绝重复序号: true
}
