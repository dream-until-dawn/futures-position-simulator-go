// Package replay 提供有界、可复现的纯离线业务重放、账单和差异定位。
// 仅接受显式成交与手动挂单事件；不连接柜台、不实现K线撮合，不提升输入的证据等级。
package replay

import (
	"encoding/json"
	futsim "github.com/dream-until-dawn/futures-position-simulator-go"
	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/dream-until-dawn/futures-position-simulator-go/match"
	"github.com/dream-until-dawn/futures-position-simulator-go/order"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

const (
	// Format 是回放信封和报告的版本，不改变内核存档格式。
	Format = 1
	// MaxInputBytes 是完整JSON输入的硬上限。
	MaxInputBytes = 16 << 20
	// MaxEvents 限制一次回放的事件数。
	MaxEvents = 4096
	// MaxReportBytes 限制含全部状态的报告大小。
	MaxReportBytes = 64 << 20
)

// Settings 是内核存档之外必须随包绑定的外部配置。
type Settings struct {
	Day            types.TradingDay `json:"day"`
	PreBalance     decimal.Decimal  `json:"pre_balance"`
	Choices        futsim.Choices   `json:"choices"`
	TickRounding   []TickRule       `json:"tick_rounding"`
	PositionLimits []Limit          `json:"position_limits"`
}

// TickRule 显式绑定一个交易所的涨跌停取整配置。
type TickRule struct {
	Exchange types.Exchange       `json:"exchange"`
	Rounding refdata.TickRounding `json:"rounding"`
}

// Limit 显式绑定一个合约的限仓配置；重复项拒绝。
type Limit struct {
	Instrument types.InstrumentID `json:"instrument"`
	Volume     int                `json:"volume"`
}

// Payload 是同一回放所需的完整输入；Initial为空时按Settings开户。
type Payload struct {
	Rules    json.RawMessage `json:"rules"`
	Settings Settings        `json:"settings"`
	Initial  *futsim.State   `json:"initial"`
	Events   []Event         `json:"events"`
}

// Accepted 表示调用方明确声明已接受的委托，不代表本工具执行了柜台报单校验。
type Accepted struct {
	Ref     string        `json:"ref"`
	Request order.Request `json:"request"`
}

// Price 是指定合约的本日结算价，不能用缺失值代替零。
type Price struct {
	Instrument types.InstrumentID `json:"instrument"`
	Value      decimal.Decimal    `json:"value"`
}

// Settlement 绑定本日价格与严格下一交易日。
type Settlement struct {
	NextDay types.TradingDay `json:"next_day"`
	Prices  []Price          `json:"prices"`
}

// Event 只允许kind所需的一个负载，避免忽略无关字段造成假重放。
type Event struct {
	ID         string           `json:"id"`
	Day        types.TradingDay `json:"day"`
	Kind       string           `json:"kind"`
	Quote      *futsim.Quote    `json:"quote,omitempty"`
	Trade      *match.Trade     `json:"trade,omitempty"`
	Order      *Accepted        `json:"order,omitempty"`
	Ref        string           `json:"ref,omitempty"`
	Amount     *decimal.Decimal `json:"amount,omitempty"`
	Settlement *Settlement      `json:"settlement,omitempty"`
}

// Options 的合成数据许可由调用方显式给，不能藏进回放包自行授权。
type Options struct{ AllowPlaceholder bool }

// Changes 是累计字段相邻快照的变化，结算清零不等于负手续费。
type Changes struct {
	Balance, Commission, CloseProfit, PositionProfit          decimal.Decimal
	Margin, FrozenMargin, FrozenCommission, Deposit, Withdraw decimal.Decimal
}

// Statement 是本库计算的日终账单，不是柜台原始结算单。
type Statement struct {
	Day, NextDay                                                                                types.TradingDay
	PreBalance, Deposit, Withdraw, CloseProfit, PositionProfit, Commission, Balance, NextMargin decimal.Decimal
}

// Frame 只保存已成功提交的事件及其完整结果。
type Frame struct {
	Index     int              `json:"index"`
	Event     Event            `json:"event"`
	Account   account.Snapshot `json:"account"`
	State     futsim.State     `json:"state"`
	Changes   Changes          `json:"changes"`
	Statement *Statement       `json:"statement,omitempty"`
}

// Report 的failed携带有效前缀；调用方必须同时检查返回error与status。
type Report struct {
	Format         int               `json:"format"`
	PayloadSHA256  string            `json:"payload_sha256"`
	Evidence       refdata.Evidence  `json:"evidence"`
	Status         string            `json:"status"`
	FailedIndex    *int              `json:"failed_index,omitempty"`
	Failure        string            `json:"failure,omitempty"`
	Initial        *futsim.State     `json:"initial"`
	InitialAccount *account.Snapshot `json:"initial_account"`
	Frames         []Frame           `json:"frames"`
	Final          *futsim.State     `json:"final"`
}
