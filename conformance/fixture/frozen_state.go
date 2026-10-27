package fixture

import (
	"fmt"
	"strings"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// FrozenBookWithState 从独立的挂单前完整状态按明确的接收顺序重建冻结。
// 不从待比的柜台持仓/资金输出反推seed；未知记录返回has=false，任一失败不返回半本簿。
// seed必须没有挂单或冻结，允许保留历史序号高水位；cfg/seed/夹具的交易日必须相同。
func FrozenBookWithState(f *Fixture, cfg futsim.Config, seed futsim.State, orderedIDs []string) (*order.Book, bool, error) {
	if f == nil {
		return nil, false, fmt.Errorf("冻结夹具为nil")
	}
	if !f.HasOrders {
		return nil, false, nil
	}
	if cfg.Day != f.TradingDay || seed.Day != f.TradingDay {
		return nil, false, fmt.Errorf("冻结夹具、配置与初始状态交易日不一致")
	}
	if len(seed.Orders) != 0 || !seed.Account.FrozenMargin.IsZero() || !seed.Account.FrozenCommission.IsZero() || !seed.Account.FrozenCash.IsZero() {
		return nil, false, fmt.Errorf("初始状态已有挂单或冻结，不能重复重建")
	}
	sim, err := futsim.Restore(cfg, seed)
	if err != nil {
		return nil, false, fmt.Errorf("冻结初始状态不可用：%w", err)
	}
	requests := map[string]order.Request{}
	for id, record := range f.Orders {
		v, ok := record["status"]
		if !ok || !v.IsText || v.Absent {
			return nil, false, fmt.Errorf("委托%s缺少明确状态", id)
		}
		switch strings.ToUpper(v.Text) {
		case "FINISHED":
			continue
		case "ALIVE":
		default:
			return nil, false, fmt.Errorf("委托%s状态未知", id)
		}
		left, present := record["volume_left"]
		if !present {
			return nil, false, fmt.Errorf("委托%s缺少剩余手数", id)
		}
		volume, err := frozenRemaining(left)
		if err != nil {
			return nil, false, fmt.Errorf("委托%s剩余手数：%w", id, err)
		}
		if volume == 0 {
			continue
		}
		if strings.TrimSpace(id) == "" {
			return nil, false, fmt.Errorf("活委托ID为空")
		}
		symbol, ok := textOf(record, "exchange_id", "instrument_id")
		if !ok {
			return nil, false, fmt.Errorf("委托%s缺少合约", id)
		}
		inst, err := types.ParseSymbol(symbol, f.TradingDay)
		if err != nil {
			return nil, false, fmt.Errorf("委托%s合约：%w", id, err)
		}
		dir, offset, err := dirOffsetOf(record)
		if err != nil {
			return nil, false, fmt.Errorf("委托%s：%w", id, err)
		}
		price, ok := numberOf(record, "limit_price")
		if !ok || !price.IsPositive() || price.Exponent() < -64 || price.Exponent() > 64 || price.Coefficient().BitLen() > 512 {
			return nil, false, fmt.Errorf("委托%s没有有限精度且为正的limit_price", id)
		}
		requests[id] = order.Request{Instrument: inst, Direction: dir, Offset: offset, Hedge: types.Speculation, Price: price, Volume: volume}
	}
	if len(orderedIDs) != len(requests) {
		return nil, false, fmt.Errorf("接收顺序必须完整覆盖全部活委托")
	}
	seen := map[string]bool{}
	for _, id := range orderedIDs {
		req, ok := requests[id]
		if !ok || seen[id] {
			return nil, false, fmt.Errorf("接收顺序有重复或未知委托：%s", id)
		}
		seen[id] = true
		if _, err = sim.PlaceAccepted(f.TradingDay, id, req); err != nil {
			return nil, false, fmt.Errorf("冻结委托%s：%w", id, err)
		}
	}
	state, err := sim.State()
	if err != nil {
		return nil, false, err
	}
	entries := make([]order.BookEntry, 0, len(state.Orders))
	for _, entry := range state.Orders {
		entries = append(entries, order.BookEntry{ID: entry.ID, Request: entry.Request, Frozen: entry.Frozen, Sequence: entry.Sequence})
	}
	book, err := order.RestoreBook(state.LastOrderSequence, entries)
	if err != nil {
		return nil, false, fmt.Errorf("重建冻结簿：%w", err)
	}
	return book, true, nil
}

// frozenRemaining 对手数使用有限精度整数域，避免截断小数、int溢出或巨大指数分配。
func frozenRemaining(v Value) (int, error) {
	d := v.Number
	if v.Absent || v.IsText || d.Exponent() < -64 || d.Exponent() > 64 || d.Coefficient().BitLen() > 512 {
		return 0, fmt.Errorf("手数未知或超出有限精度范围")
	}
	maxInt := int(^uint(0) >> 1)
	if d.IsNegative() || d.GreaterThan(decimal.NewFromInt(int64(maxInt))) || !d.Equal(d.Truncate(0)) {
		return 0, fmt.Errorf("手数必须为可表示的非负整数")
	}
	return int(d.IntPart()), nil
}
