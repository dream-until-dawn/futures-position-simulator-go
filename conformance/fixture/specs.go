package fixture

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/shopspring/decimal"
)

// ContractSpec 是从上游合约字典取来的合约规格。
//
// ⚠️ 只含**字典真的给了**的那几项。六个费率、四个保证金率、
// PositionDateType、MaxMarginSideAlgorithm 字典里没有 ——
// 它们仍然只能来自实测或 CTP 合约表。
type ContractSpec struct {
	Instrument     string          `json:"instrument"`
	Exchange       string          `json:"exchange"`
	Product        string          `json:"product"`
	VolumeMultiple decimal.Decimal `json:"-"`
	PriceTick      decimal.Decimal `json:"-"`

	// ⚠️ 价格与乘数用 RawMessage 原样取字节，不经 float64 ——
	// 与 refdata/exchange 同一条理由：JSON 数值经 float64 往返会丢位，
	// 而金额上的每一次这种损失都会被乘以手数与乘数放大。
	RawMultiple json.RawMessage `json:"volume_multiple"`
	RawTick     json.RawMessage `json:"price_tick"`
	PriceDecs   int             `json:"price_decs"`
	MaxLimit    int             `json:"max_limit_order_volume"`
	MinLimit    int             `json:"min_limit_order_volume"`
}

// LoadSpecs 读 refdata-sync -specs 产出的规格文件。
//
// ⚠️ 它替掉的是测试里一张**手抄的**乘数表。手抄的那张当初写着
// 「这是一处已知的手抄，哪天有了快照就删掉换成读快照」——
// 而没有任何机制逼它被删掉，那正是「暂缓变永久」的形状。
func LoadSpecs(r io.Reader) (map[string]ContractSpec, error) {
	var raw struct {
		Source      string         `json:"source"`
		GeneratedAt string         `json:"generated_at"`
		Note        string         `json:"note"`
		Specs       []ContractSpec `json:"specs"`
	}
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("读合约规格失败：%w", err)
	}
	out := make(map[string]ContractSpec, len(raw.Specs))
	for _, s := range raw.Specs {
		m, err := decimal.NewFromString(string(s.RawMultiple))
		if err != nil || !m.IsPositive() {
			// ⚠️ 乘数为零会让所有金额变成 0，而 0 看起来完全合理。
			return nil, fmt.Errorf("合约 %s 的乘数 %q 不是正数：%v",
				s.Instrument, s.RawMultiple, err)
		}
		t, err := decimal.NewFromString(string(s.RawTick))
		if err != nil || !t.IsPositive() {
			return nil, fmt.Errorf("合约 %s 的最小变动价位 %q 不是正数：%v",
				s.Instrument, s.RawTick, err)
		}
		s.VolumeMultiple, s.PriceTick = m, t
		if _, dup := out[s.Instrument]; dup {
			return nil, fmt.Errorf("合约 %s 在规格文件里出现两次 —— "+
				"后一条会覆盖前一条，而覆盖是静默的", s.Instrument)
		}
		out[s.Instrument] = s
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("规格文件里一个合约都没有")
	}
	return out, nil
}
