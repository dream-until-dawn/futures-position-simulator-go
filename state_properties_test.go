package futsim

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func offlineState(tb testing.TB, s *Simulator) (State, []byte) {
	tb.Helper()
	st, err := s.State()
	if err != nil {
		tb.Fatal(err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		tb.Fatal(err)
	}
	return st, raw
}

// exerciseStateSequence 同时检查恢复等价与独立账本，避免两个实例以相同方式算错而自洽。
func exerciseStateSequence(t *testing.T, input []byte) [2][8]int {
	var outcomes [2][8]int
	t.Helper()
	if len(input) > 128 {
		input = input[:128]
	}
	cfg, inst := offlineFixture(t)
	a := offlineNew(t, cfg, inst)
	b := offlineNew(t, cfg, inst)
	live := map[string]bool{}
	var high uint64
	fees, held := int64(0), 0
	day := cfg.Day
	for step, token := range input {
		before, rawBefore := offlineState(t, a)
		cfg.Day = day
		var decoded State
		if err := json.Unmarshal(rawBefore, &decoded); err != nil {
			t.Fatal(err)
		}
		var err error
		b, err = Restore(cfg, decoded)
		if err != nil {
			t.Fatalf("序列恢复失败 step=%d: %v", step, err)
		}
		id := fmt.Sprintf("slot-%d", token%7)
		request := order.Request{Instrument: inst, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: decimal.NewFromInt(3000), Volume: 1}
		run := func(s *Simulator) error {
			switch token % 8 {
			case 0, 1:
				_, e := s.PlaceAccepted(day, id, request)
				return e
			case 2:
				return s.Cancel(day, id)
			case 3:
				_, e := s.Fill(day, id)
				return e
			case 4:
				request.Direction, request.Offset = types.Sell, types.CloseToday
				_, e := s.PlaceAccepted(day, id, request)
				return e
			case 5:
				request.Direction = types.Direction(255)
				_, e := s.PlaceAccepted(day, id, request)
				return e
			case 6:
				now, _ := time.Parse("20060102", fmt.Sprint(day))
				nextDate := now.AddDate(0, 0, 1)
				next := types.TradingDay(nextDate.Year()*10000 + int(nextDate.Month())*100 + nextDate.Day())
				return s.Settle(day, map[types.InstrumentID]decimal.Decimal{inst: decimal.NewFromInt(3000)}, next)
			default:
				if token&128 != 0 {
					return s.Mark(day, Quote{Instrument: inst, Last: decimal.NewFromInt(3000), HasLast: true, PreSettlement: decimal.NewFromInt(3000), HasPreSettlement: true})
				}
				return s.Mark(day, Quote{Instrument: inst, Last: decimal.NewFromInt(-1), HasLast: true})
			}
		}
		errA, errB := run(a), run(b)
		after, rawAfter := offlineState(t, a)
		_, rawB := offlineState(t, b)
		if fmt.Sprint(errA) != fmt.Sprint(errB) || !bytes.Equal(rawAfter, rawB) {
			t.Fatalf("恢复后继续执行不等价 step=%d token=%d", step, token)
		}
		if errA != nil {
			outcomes[1][token%8]++
			if !bytes.Equal(rawBefore, rawAfter) {
				t.Fatalf("失败操作改变完整状态 step=%d token=%d err=%v", step, token, errA)
			}
		} else {
			outcomes[0][token%8]++
			switch token % 8 {
			case 0, 1, 4:
				high++
				live[id] = true
			case 2:
				delete(live, id)
			case 3:
				for _, o := range before.Orders {
					if o.ID == id {
						if o.Request.Offset == types.Open {
							held++
						} else {
							held--
						}
					}
				}
				fees++
				delete(live, id)
			case 6:
				day = after.Day
			}
		}
		if after.LastOrderSequence != high || len(after.Orders) != len(live) {
			t.Fatalf("独立序号/挂单账不符 step=%d high=%d state=%d", step, high, after.LastOrderSequence)
		}
		for _, o := range after.Orders {
			if !live[o.ID] {
				t.Fatalf("独立挂单集合不符：%s", o.ID)
			}
		}
		if !a.Account().Balance.Equal(cfg.PreBalance.Sub(decimal.NewFromInt(fees))) {
			t.Fatalf("独立费用账不符 step=%d fees=%d", step, fees)
		}
		actualHeld := 0
		if p, ok := a.Position(inst, types.Speculation); ok {
			actualHeld = p.VolumeToday(types.Buy) + p.VolumeHistory(types.Buy)
		}
		if actualHeld != held {
			t.Fatalf("独立持仓账不符 step=%d want=%d got=%d", step, held, actualHeld)
		}
	}
	return outcomes
}

// TestStateSequenceProperties 固定种子在普通回归中执行，包含失败、跨日及序号空洞。
func TestStateSequenceProperties(t *testing.T) {
	for seed := int64(0); seed < 40; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			data := make([]byte, 128)
			_, _ = r.Read(data)
			exerciseStateSequence(t, data)
		})
	}
	counts := exerciseStateSequence(t, []byte{0, 35, 28, 35, 0, 42, 6, 135, 0, 35, 6, 28, 5, 7})
	for _, op := range []int{0, 2, 3, 4, 6, 7} {
		if counts[0][op] == 0 {
			t.Fatalf("固定正对照未触发操作%d", op)
		}
	}
	for _, op := range []int{4, 5, 7} {
		if counts[1][op] == 0 {
			t.Fatalf("固定反对照未拒绝操作%d", op)
		}
	}
}

// FuzzStateSequence 在固定上限内探索组合，不把合成账本提升为柜台证据。
func FuzzStateSequence(f *testing.F) {
	f.Add([]byte{0, 3, 4, 3, 2, 6, 0, 3, 6, 5, 7})
	f.Add([]byte{1, 1, 2, 3, 6, 6, 5, 7})
	f.Fuzz(func(t *testing.T, data []byte) { exerciseStateSequence(t, data) })
}

// TestStatePropertiesRejectTampering 正常存档可恢复；破坏冻结、序号和格式必须拒绝。
func TestStatePropertiesRejectTampering(t *testing.T) {
	cfg, inst := offlineFixture(t)
	s := offlineNew(t, cfg, inst)
	r := order.Request{Instrument: inst, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: decimal.NewFromInt(3000), Volume: 1}
	if _, err := s.PlaceAccepted(cfg.Day, "own", r); err != nil {
		t.Fatal(err)
	}
	_, raw := offlineState(t, s)
	for _, change := range []func(*State){func(st *State) { st.Format-- }, func(st *State) { st.Orders[0].Sequence = 0 }, func(st *State) { st.LastOrderSequence = 0 }, func(st *State) { st.Orders[0].Frozen.Margin = st.Orders[0].Frozen.Margin.Add(decimal.NewFromInt(1)) }} {
		var st State
		_ = json.Unmarshal(raw, &st)
		change(&st)
		if _, err := Restore(cfg, st); err == nil {
			t.Fatal("篡改存档仍被接受")
		}
	}
}
