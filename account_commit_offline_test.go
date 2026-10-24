package futsim

import (
	"github.com/dream-until-dawn/futures-position-simulator-go/account"
	"github.com/shopspring/decimal"
	"strings"
	"testing"
)

// TestOfflineCommitChecksAccount 以合法可恢复的极端笔数触发末端自查，不用unsafe污染私有字段。
func TestOfflineCommitChecksAccount(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		cfg, id := offlineFixture(t)
		s := offlineNew(t, cfg, id)
		st := s.acc.State()
		st.CommissionTrades = int(^uint(0)>>1) - 1
		if overflow {
			st.CommissionTrades++
		}
		var err error
		s.acc, err = account.Restore(st)
		if err != nil {
			t.Fatal(err)
		}
		err = s.commit(cfg.Day, valuation{}, decimal.NewFromInt(1), decimal.NewFromInt(1), decimal.Zero)
		if overflow {
			if err == nil || s.broken == nil || !strings.Contains(err.Error(), "账户写入失败") {
				t.Fatalf("账户末端自查未阻断：%v", err)
			}
			if s.Mark(cfg.Day, Quote{Instrument: id}) == nil {
				t.Fatal("失效模拟器继续运行")
			}
		} else if err != nil || s.broken != nil {
			t.Fatalf("合法边界被误拒：%v", err)
		}
	}
}
