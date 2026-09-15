package fee

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

// TestBasisPriceReproducesBothCounters 用两个口子各自的一笔观测钉住两个取值。
//
// ⚠️ 每个观测都**同时**算另一个取值，要求它对不上 —— 否则样本分不开两者，本条只是在复述。
func TestBasisPriceReproducesBothCounters(t *testing.T) {
	d := decimal.RequireFromString
	// CTP：SHFE.rb2701 交易日 20260911 平昨 @3137，昨结算 3147，柜台收 3.142（ctp-status-20260911-2.json）。
	// 行为费率「按额 0.0001 + 每手 0.005」—— 那个 0.005 声明里没有（§13 #19）。
	rb := Rates{CloseByMoney: d("0.0001"), CloseByVolume: d("0.005")}
	at := func(r Rates, off types.Offset, b PriceBasis, trade, pre string) decimal.Decimal {
		t.Helper()
		px, err := BasisPrice(b, d(trade), d(pre), true)
		if err != nil {
			t.Fatal(err)
		}
		c, err := Compute(r, off, px, d("10"), 1, NoRounding)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if got := at(rb, types.CloseYesterday, TradePrice, "3137", "3147"); !got.Equal(d("3.142")) {
		t.Errorf("CTP 按成交价应得 3.142，得 %s", got)
	}
	if got := at(rb, types.CloseYesterday, PreSettlement, "3137", "3147"); got.Equal(d("3.142")) {
		t.Error("⚠️ 按昨结算价也得 3.142 —— 这笔样本分不开两个取值")
	}
	// 快期：rb2610 开仓成交 3092、昨结算 3100、乘数 10、按额 0.00001，柜台 0.3100（probes.md §7）。
	kq := Rates{OpenByMoney: d("0.00001")}
	if got := at(kq, types.Open, PreSettlement, "3092", "3100"); !got.Equal(d("0.31")) {
		t.Errorf("快期按昨结算价应得 0.31，得 %s", got)
	}
	if got := at(kq, types.Open, TradePrice, "3092", "3100"); got.Equal(d("0.31")) {
		t.Error("⚠️ 按成交价也得 0.31 —— 这笔样本分不开两个取值")
	}
}

// TestBasisPriceRefusesToSubstitute 钉住零值报错、缺昨结算价时不拿成交价顶。
func TestBasisPriceRefusesToSubstitute(t *testing.T) {
	px := decimal.RequireFromString("3137")
	if _, err := BasisPrice(PriceBasisUnmeasured, px, px, true); err == nil || !strings.Contains(err.Error(), "未指定") {
		t.Errorf("零值要报「未指定」：%v", err)
	}
	if got, err := BasisPrice(PreSettlement, px, decimal.Zero, false); err == nil {
		t.Errorf("⚠️ 没有昨结算价却返回了 %s —— 拿成交价顶了", got)
	}
	// 反向：按成交价时缺昨结算价不碍事，否则一个「一律要昨结算价」的实现也能过上面。
	if got, err := BasisPrice(TradePrice, px, decimal.Zero, false); err != nil || !got.Equal(px) {
		t.Errorf("按成交价、缺昨结算价时应返回成交价：%s %v", got, err)
	}
}
