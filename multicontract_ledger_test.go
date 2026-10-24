package futsim

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestMultiContractIndependentLedger 的预期数只来自本测试注释中的手算，不调用生产盈亏/保证金函数。
// A: 乘数10，多2@3000；B: 乘数5，空3@100。两合约不启用大边，固定费率各不相同。
func TestMultiContractIndependentLedger(t *testing.T) {
	for _, alg := range []account.Algorithm{account.AlgorithmAll, account.AlgorithmOnlyLost} {
		t.Run(alg.String(), func(t *testing.T) {
			dec := decimal.NewFromInt
			a := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
			b := types.InstrumentID{Exchange: types.SHFE, Product: "cu", Year: 2027, Month: 1}
			builder := refdata.NewBuilder(1)
			for i, id := range []types.InstrumentID{a, b} {
				multiple, rate := int64(10), "0.1"
				if i == 1 {
					multiple, rate = 5, "0.2"
				}
				builder.AddInstrument(refdata.Instrument{ID: id, VolumeMultiple: dec(multiple), PriceTick: dec(1), PositionDateType: refdata.UseHistory, IsTrading: true}).
					AddCommissionRates(id, types.Speculation, refdata.CommissionRates{OpenByVolume: dec(int64(2 + 2*i)), CloseByVolume: dec(int64(3 + 3*i)), CloseTodayByVolume: dec(int64(5 + 3*i))}).
					AddMarginRates(id, types.Speculation, refdata.MarginRates{LongByMoney: decimal.RequireFromString(rate), ShortByMoney: decimal.RequireFromString(rate)})
			}
			rules, err := builder.Build()
			if err != nil {
				t.Fatal(err)
			}
			choices := ctpChoices()
			choices.Algorithm = alg
			cfg := Config{Day: simDay, PreBalance: dec(1000000), Rules: rules, Choices: choices}
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			mark := func(id types.InstrumentID, last, pre int64) {
				t.Helper()
				if err := s.Mark(cfg.Day, Quote{Instrument: id, Last: dec(last), HasLast: true, PreSettlement: dec(pre), HasPreSettlement: true}); err != nil {
					t.Fatal(err)
				}
			}
			trade := func(id types.InstrumentID, dir types.Direction, off types.Offset, price int64, volume int) {
				t.Helper()
				if err := s.ApplyTrade(cfg.Day, match.Trade{Instrument: id, Direction: dir, Offset: off, Hedge: types.Speculation, Price: dec(price), Volume: volume}); err != nil {
					t.Fatal(err)
				}
			}
			// 每个检查点都经过 JSON 往返，再独立核对账户字面金额；不以恢复相等代替算账。
			check := func(label string, balance, profit, close, fees, margin, frozen, freezeFee int64) {
				t.Helper()
				_, raw := offlineState(t, s)
				var decoded State
				if err := json.Unmarshal(raw, &decoded); err != nil {
					t.Fatal(err)
				}
				restored, err := Restore(cfg, decoded)
				if err != nil {
					t.Fatal(err)
				}
				_, again := offlineState(t, restored)
				if !bytes.Equal(raw, again) {
					t.Fatalf("多合约恢复不等价 %s", label)
				}
				got := s.Account()
				for _, v := range []struct {
					name string
					got  decimal.Decimal
					want int64
				}{
					{"balance", got.Balance, balance}, {"positionProfit", got.PositionProfit, profit}, {"closeProfit", got.CloseProfit, close}, {"commission", got.Commission, fees}, {"margin", got.CurrMargin, margin}, {"frozenMargin", got.FrozenMargin, frozen}, {"frozenFee", got.FrozenCommission, freezeFee},
				} {
					if !v.got.Equal(dec(v.want)) {
						t.Fatalf("独立多合约账本不符 %s %s got=%s want=%d", label, v.name, v.got, v.want)
					}
				}
				available := balance - margin - frozen - freezeFee
				if alg == account.AlgorithmOnlyLost && profit > 0 {
					available -= profit
				}
				if !got.Available.Equal(dec(available)) {
					t.Fatalf("独立多合约可用不符 %s got=%s want=%d", label, got.Available, available)
				}
				// Restore 必须复制输入；篡改原存档不可影响已恢复实例。
				if len(decoded.Positions) > 0 {
					if len(decoded.Positions[0].Long) > 0 {
						decoded.Positions[0].Long[0].Volume = 99
					}
					if len(decoded.Positions[0].Short) > 0 {
						decoded.Positions[0].Short[0].Volume = 99
					}
				}
				_, isolated := offlineState(t, restored)
				if !bytes.Equal(again, isolated) {
					t.Fatal("恢复实例引用输入持仓")
				}
				s = restored
			}
			mark(a, 3000, 3000)
			mark(b, 100, 100)
			if err := s.Deposit(cfg.Day, dec(1000)); err != nil {
				t.Fatal(err)
			}
			if err := s.Withdraw(cfg.Day, dec(200)); err != nil {
				t.Fatal(err)
			}
			trade(a, types.Buy, types.Open, 3000, 2)
			trade(b, types.Sell, types.Open, 100, 3)
			mark(a, 3010, 3000)
			mark(b, 98, 100)
			check("marked", 1001014, 230, 0, 16, 6300, 0, 0)
			request := order.Request{Instrument: a, Direction: types.Buy, Offset: types.Open, Hedge: types.Speculation, Price: dec(3020), Volume: 1}
			if _, err := s.PlaceAccepted(cfg.Day, "pending", request); err != nil {
				t.Fatal(err)
			}
			check("frozen", 1001014, 230, 0, 16, 6300, 3020, 2)
			// 已有挂单重复编号、负入金、不可用资金出金均须完整状态不变。
			for _, fail := range []func() error{
				func() error { _, err := s.PlaceAccepted(cfg.Day, "pending", request); return err },
				func() error { return s.Deposit(cfg.Day, dec(-1)) },
				func() error { return s.Withdraw(cfg.Day, dec(2000000)) },
				func() error { return s.Settle(cfg.Day, map[types.InstrumentID]decimal.Decimal{a: dec(3020)}, simNext) },
			} {
				_, before := offlineState(t, s)
				if fail() == nil {
					t.Fatal("独立账本非法操作被接受")
				}
				_, after := offlineState(t, s)
				if !bytes.Equal(before, after) {
					t.Fatal("独立账本失败改变状态")
				}
			}
			if err := s.Cancel(cfg.Day, "pending"); err != nil {
				t.Fatal(err)
			}
			trade(a, types.Sell, types.CloseToday, 3015, 1)
			trade(b, types.Buy, types.CloseToday, 97, 2)
			check("closed-today", 1001053, 110, 180, 37, 3100, 0, 0)
			if err := s.Settle(cfg.Day, map[types.InstrumentID]decimal.Decimal{a: dec(3020), b: dec(96)}, simNext); err != nil {
				t.Fatal(err)
			}
			cfg.Day = simNext
			check("settled", 1001163, 0, 0, 0, 3116, 0, 0)
			mark(a, 3010, 3020)
			mark(b, 99, 96)
			check("next-mark", 1001048, -115, 0, 0, 3116, 0, 0)
			trade(a, types.Sell, types.CloseYesterday, 3010, 1)
			trade(b, types.Buy, types.CloseYesterday, 99, 1)
			check("closed-history", 1001039, 0, -115, 9, 0, 0, 0)
		})
	}
}
