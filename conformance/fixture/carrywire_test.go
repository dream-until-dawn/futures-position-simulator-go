package fixture

import (
	"sort"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/position"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
)

// carryStartFor 为一份**带昨仓**的截面找出它的起始持仓：
// 取上一交易日的收盘态夹具，按交易所结算价结转过来。
//
// # ⚠️ 挑哪一份上一日夹具，是这段代码里最容易出错的一步
//
// 同一个交易日有几十份夹具，它们是**盘中不同时刻**的快照。
// 拿早上那一份去结转，得到的起始持仓少几手 —— 而少几手之后的对拍
// **看起来完全正常**，只是一堆字段「差一点」。
//
// 判据两条，缺一不可：
//
//	① 必须**有那个合约的成交**    Carry 靠重放成交重建持仓，
//	                              零成交的夹具会安静地结转出一个空仓
//	② 采集时刻**最晚**的那一份    最接近收盘态
//
// ⚠️ 而 ② 只是个代理。真正要的是「它看全了那一天的成交」，所以这里
// **再核一次**：选中那份的成交笔数必须是当天同合约的**最大值**。
// 不是的话就报错而不是将就 —— 那说明「越晚看到越多」这个前提不成立，
// 而前提不成立时 ② 挑出来的那份是错的。
func carryStartFor(t *testing.T, all []*Fixture, f *Fixture, sym string) (*position.Position, bool, string) {
	t.Helper()
	today := f.TradingDay.String()

	type cand struct {
		f      *Fixture
		trades int
	}
	var sameDay []cand // 上一交易日、含该合约成交的全部夹具
	prevDay := ""
	for _, o := range all {
		d := o.TradingDay.String()
		if d >= today {
			continue
		}
		if n := len(o.TradesOf(sym)); n > 0 {
			if d > prevDay {
				prevDay = d
			}
		}
	}
	if prevDay == "" {
		return nil, false, "上一交易日没有含该合约成交的夹具"
	}
	for _, o := range all {
		if o.TradingDay.String() != prevDay {
			continue
		}
		if n := len(o.TradesOf(sym)); n > 0 {
			sameDay = append(sameDay, cand{o, n})
		}
	}
	sort.Slice(sameDay, func(i, j int) bool {
		return sameDay[i].f.CapturedAt < sameDay[j].f.CapturedAt
	})
	pick := sameDay[len(sameDay)-1]

	// ⚠️ 代理变量的当场核对，见函数注释。
	maxTrades := 0
	for _, c := range sameDay {
		if c.trades > maxTrades {
			maxTrades = c.trades
		}
	}
	if pick.trades != maxTrades {
		t.Fatalf("⚠️ %s 上采集最晚的那份夹具（%s，%s）只有 %d 笔 %s 的成交，"+
			"而当天最多的一份有 %d 笔 —— **「越晚看到越多」这个前提不成立**，"+
			"于是「取最晚那份」挑出来的起始持仓是错的。"+
			"⚠️ 别放宽这条：少几手的起始持仓会让下游比出一堆看起来像真差异的差异",
			prevDay, pick.f.Path, pick.f.CapturedAt, pick.trades, sym, maxTrades)
	}

	settle, ok := settlementOf(t, sym)
	if !ok {
		// ⚠️ 缺席就缺席，**不拿柜台的 quotes.settlement 顶替** ——
		// 那会把独立来源悄悄换成同源，而同源核对是同义反复。
		return nil, false, "交易所日行情里没有该合约的结算价（大商所 412 未打通）"
	}
	p, err := Carry(pick.f, sym, types.Speculation, positionDateOf(t, sym), settle, f.TradingDay)
	if err != nil {
		return nil, false, "结转失败：" + err.Error()
	}
	return p, true, ""
}
