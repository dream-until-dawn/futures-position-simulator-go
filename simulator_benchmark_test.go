package futsim

import (
	"fmt"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// offlineBenchFixture 生成逐笔持仓和可选挂单，setup始终不计入被测操作时间。
func offlineBenchFixture(b *testing.B, n int, withOrders bool) (Config, types.InstrumentID, *Simulator, State) {
	b.Helper()
	cfg, inst := offlineFixture(b)
	s := offlineNew(b, cfg, inst)
	for i := 0; i < n; i++ {
		if err := s.ApplyTrade(cfg.Day, match.Trade{Instrument: inst, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: decimal.NewFromInt(3000 + int64(i%13)), Volume: 1}); err != nil {
			b.Fatal(err)
		}
		if withOrders {
			_, err := s.PlaceAccepted(cfg.Day, fmt.Sprintf("order-%06d", i), order.Request{Instrument: inst, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: decimal.NewFromInt(3000), Volume: 1})
			if err != nil {
				b.Fatal(err)
			}
		}
	}
	st, err := s.State()
	if err != nil {
		b.Fatal(err)
	}
	return cfg, inst, s, st
}

func BenchmarkOfflineState(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			_, _, s, _ := offlineBenchFixture(b, n, true)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.State(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkOfflineRestore(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			cfg, _, _, st := offlineBenchFixture(b, n, true)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Restore(cfg, st); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkOfflineFill 每次先暂停计时恢复相同起点，只计一笔Fill；用固定次数避免慢setup导致长时间校准。
func BenchmarkOfflineFill(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			cfg, _, _, st := offlineBenchFixture(b, n, true)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s, err := Restore(cfg, st)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if _, err = s.Fill(cfg.Day, "order-000000"); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkOfflineSettle 不含挂单，测量逐笔持仓跨日结算；恢复起点不计时。
func BenchmarkOfflineSettle(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			cfg, inst, _, st := offlineBenchFixture(b, n, false)
			prices := map[types.InstrumentID]decimal.Decimal{inst: decimal.NewFromInt(3000)}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s, err := Restore(cfg, st)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err = s.Settle(cfg.Day, prices, simNext); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
