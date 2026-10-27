package futsim_test

import (
	"crypto/sha256"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/conformance/fixture"
	"github.com/dream-until-dawn/futures-position-simulator-go/lifecycle"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/replay"
	"github.com/dream-until-dawn/futures-position-simulator-go/risk"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// 仅用外部包公开签名，不能通过同包访问私有状态掩盖接入破坏。
var (
	_ func(*refdata.Snapshot, lifecycle.Schedule, types.TradingDay, int64) (*lifecycle.DailyRules, error)                                                  = lifecycle.NewDailyRules
	_ func(io.Reader, refdata.LoadOptions) (*lifecycle.DailyRules, *refdata.Calendar, refdata.Evidence, error)                                             = lifecycle.Load
	_ func(account.State, risk.Policy) (risk.Report, error)                                                                                                = risk.Assess
	_ func(*fixture.Fixture, futsim.Config, futsim.State, []string) (*order.Book, bool, error)                                                             = fixture.FrozenBookWithState
	_ func(*futsim.Simulator) (lifecycle.Report, error)                                                                                                    = (*futsim.Simulator).AssessLifecycle
	_ func(*futsim.Simulator, types.TradingDay, map[types.InstrumentID]decimal.Decimal, types.TradingDay, *lifecycle.DailyRules) (lifecycle.Report, error) = (*futsim.Simulator).SettleWithRules
)

// TestOfflinePublicProtocolShape 记录本轮公开字段和JSON键，更改须显式说明兼容/迁移；不是宣称API已冻结。
func TestOfflinePublicProtocolShape(t *testing.T) {
	for _, c := range []struct {
		value any
		want  string
	}{
		{risk.Policy{}, "MaxRiskRatio MinEquity MinAvailable"},
		{risk.Report{}, "Account Policy RatioExceeded EquityBelowMinimum AvailableBelowMinimum NeedsMarginCall RequiredDeposit"},
		{lifecycle.MarginStep{}, "From Rates"},
		{lifecycle.Contract{}, "Instrument LastTradingDay LastHoldingDay MarginSteps"},
		{lifecycle.Schedule{}, "First Last Investor Source Contracts"},
		{lifecycle.Holding{}, "Instrument Long Short"},
		{lifecycle.Holdings{}, "Known Positions"},
		{lifecycle.Status{}, "Instrument Long Short MarginFrom LastTradingDay LastHoldingDay Expired CloseOnly ExitByClose HoldingViolation Tradable"},
		{lifecycle.Report{}, "Day Investor Source Contracts HasBlockingHoldings"},
		{replay.RuleBinding{}, "Lifecycle=lifecycle Evidence=evidence"},
		{replay.Payload{}, "Rules=rules,omitempty Lifecycle=lifecycle,omitempty Settings=settings Initial=initial Events=events"},
		{replay.Settlement{}, "NextDay=next_day Prices=prices NextRules=next_rules,omitempty"},
		{replay.Event{}, "ID=id Day=day Kind=kind Quote=quote,omitempty Trade=trade,omitempty Order=order,omitempty Ref=ref,omitempty Amount=amount,omitempty Settlement=settlement,omitempty RiskPolicy=risk_policy,omitempty"},
		{replay.Frame{}, "Index=index Event=event Account=account State=state Changes=changes Statement=statement,omitempty Rules=rules,omitempty Lifecycle=lifecycle,omitempty Risk=risk,omitempty"},
		{replay.Report{}, "Format=format PayloadSHA256=payload_sha256 Evidence=evidence Status=status FailedIndex=failed_index,omitempty Failure=failure,omitempty Initial=initial InitialAccount=initial_account InitialRules=initial_rules,omitempty Frames=frames Final=final"},
	} {
		typ := reflect.TypeOf(c.value)
		var fields []string
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			name := f.Name
			if tag := f.Tag.Get("json"); tag != "" {
				name += "=" + tag
			}
			fields = append(fields, name)
		}
		if got := strings.Join(fields, " "); got != c.want {
			t.Errorf("公开协议字段或标签改变 type=%s got=%q want=%q", typ, got, c.want)
		}
	}
	typ := reflect.TypeOf((*lifecycle.DailyRules)(nil))
	var methods []string
	for i := 0; i < typ.NumMethod(); i++ {
		methods = append(methods, typ.Method(i).Name)
	}
	if got := strings.Join(methods, " "); got != "Assess CheckOpening CommissionRates Instrument Investor LifecycleDay MarginRates ProductInstruments Save Version" {
		t.Fatalf("日规则公开方法集改变: %s", got)
	}
}

// TestOfflineWireCompatibility 数字枚举已用于文件，旧格式1演示包必须保持逐字节兼容。
func TestOfflineWireCompatibility(t *testing.T) {
	if lifecycle.InvestorUnknown != 0 || lifecycle.Individual != 1 || lifecycle.Institution != 2 || lifecycle.PersistenceFormat != 1 || replay.Format != 1 {
		t.Fatal("离线协议枚举或格式值改变，须明确迁移")
	}
	p, err := replay.Demo()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := replay.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(raw)); digest != "70a723b4ac9f91281c28a81fb268a7d5af931c97736a6536bd6df14c5a88f644" {
		t.Fatalf("旧格式1规范包发生非兼容变化: %s", digest)
	}
}
