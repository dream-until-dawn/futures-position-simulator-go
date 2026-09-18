package fixture

import (
	"testing"
)

// TestKQSettleDoesNotTruncateFees 钉住 kq_facts 53：快期结算时**不**重新取整手续费 —— 当日带零头的手续费原样进次日 pre_balance。
//
// ⚠️ 这是一条**已知口径差**的声明，不是本库的对拍：CTP 结算逐笔截断到分（cn-futures-rules §13 #5），
// 两个口子实测相反，按长期规则本库跟 CTP（design.md 门面形状 §14，F10）。
// 快期跨日对拍都从夹具的 pre_balance 起步，没有一条拿本库结算出的次日结存与快期比 —— 口径差不会自己冒出来，
// 所以在这里把快期那一侧的事实钉住：快期哪天改成截断了，本条会红，那时这条口径差就该撤掉。
func TestKQSettleDoesNotTruncateFees(t *testing.T) {
	end := loadOne(t, "watch-start-20260908-10.json") // 交易日 20260908 收盘后（15:54）
	next := loadOne(t, "status-20260909.json")        // 交易日 20260909 结算之后第一份
	if end.TradingDay != 20260908 || next.TradingDay != 20260909 {
		t.Fatalf("交易日 %d / %d，期望 20260908 / 20260909", end.TradingDay, next.TradingDay)
	}
	fee := end.Account["commission"].Number
	// 判别力：当日手续费带不到一分的零头，否则截断与不截断同值
	if fee.Equal(fee.Truncate(2)) {
		t.Fatalf("⚠️ 当日手续费 %s 是整分 —— 这对截面分不开截不截断，本条在空转", fee)
	}
	balance, pre := end.Account["balance"].Number, next.Account["pre_balance"].Number
	if !pre.Equal(balance) {
		t.Errorf("⚠️ 快期次日 pre_balance %s ≠ 收盘结存 %s（差 %s）—— 快期的结算口径变了？"+
			"若差额等于手续费的零头，它改成了截断：kq_facts 53 与这条口径差要撤掉", pre, balance, pre.Sub(balance))
	}
}
