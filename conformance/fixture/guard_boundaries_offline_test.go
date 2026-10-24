package fixture

import (
	"fmt"
	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"strings"
	"testing"
)

// evidenceReporter 是语料守卫已有的诊断边界；真实测试仍传入testing.T。
// 反例测试记录同一判据的失败，不启动会污染真实语料的子测试或网络调用。
type evidenceReporter interface {
	Helper()
	Error(...any)
	Errorf(string, ...any)
	Fatalf(string, ...any)
	Log(...any)
	Logf(string, ...any)
}
type evidenceFailure struct{}
type recordedEvidence struct{ errors []string }

func (*recordedEvidence) Helper()          {}
func (r *recordedEvidence) Error(a ...any) { r.errors = append(r.errors, fmt.Sprint(a...)) }
func (r *recordedEvidence) Errorf(f string, a ...any) {
	r.errors = append(r.errors, fmt.Sprintf(f, a...))
}
func (r *recordedEvidence) Fatalf(f string, a ...any) { r.Errorf(f, a...); panic(evidenceFailure{}) }
func (*recordedEvidence) Log(...any)                  {}
func (*recordedEvidence) Logf(string, ...any)         {}

// expectEvidence 只截获守卫自身的Fatal信号，其他panic仍直接失败。
func expectEvidence(t *testing.T, label, want string, run func(evidenceReporter)) {
	t.Helper()
	r := &recordedEvidence{}
	func() {
		defer func() {
			if p := recover(); p != nil {
				if _, ok := p.(evidenceFailure); !ok {
					panic(p)
				}
			}
		}()
		run(r)
	}()
	text := strings.Join(r.errors, "\n")
	if (want == "") != (len(r.errors) == 0) || (want != "" && !strings.Contains(text, want)) {
		t.Fatalf("离线守卫反例未检出或正例误拒：%s want=%q errors=%q", label, want, text)
	}
}

func TestOfflineCloseOrderEvidenceBoundaries(t *testing.T) {
	makeAll := func(n int, side string) []*Fixture {
		out := make([]*Fixture, n)
		for i := range out {
			p := map[string]Value{"volume_long_his": {Number: dd("0")}, "volume_short_his": {Number: dd("0")}}
			if i == 0 && side != "" {
				p["volume_"+side+"_his"] = Value{Number: dd("1")}
			}
			out[i] = &Fixture{Positions: map[string]map[string]Value{"DCE.m2701": p}}
		}
		return out
	}
	expectEvidence(t, "50条合法", "", func(r evidenceReporter) { checkCloseOrderEvidence(r, makeAll(50, "")) })
	expectEvidence(t, "49条不足", "只有", func(r evidenceReporter) { checkCloseOrderEvidence(r, makeAll(49, "")) })
	for _, side := range []string{"long", "short"} {
		expectEvidence(t, side+"昨仓反例", "大商所出现了", func(r evidenceReporter) { checkCloseOrderEvidence(r, makeAll(50, side)) })
	}
}

func TestOfflineComparisonCoverageBoundaries(t *testing.T) {
	expectEvidence(t, "部分跳过仍比对", "", func(r evidenceReporter) { checkReconstructionCoverage(r, 1, 2) })
	expectEvidence(t, "全部跳过", "每一个", func(r evidenceReporter) { checkReconstructionCoverage(r, 2, 2) })
	expectEvidence(t, "有非零冻结", "", func(r evidenceReporter) { checkFrozenDiscrimination(r, 3, 1) })
	expectEvidence(t, "全零不能证明冻结", "没有一个是非零", func(r evidenceReporter) { checkFrozenDiscrimination(r, 3, 0) })
	expectEvidence(t, "没有缺前提", "", func(r evidenceReporter) { checkReplayPrerequisites(r, nil) })
	expectEvidence(t, "缺前提不能静默", "取不到门面前提", func(r evidenceReporter) { checkReplayPrerequisites(r, []string{"synthetic:no-carry-source"}) })
}

func TestOfflineObservedClassBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, class string
		never       bool
		want        string
	}{
		{"未知保持未知", "跨日-?", true, ""}, {"已非零可分类", "跨日-X", false, ""}, {"零观测冒认机制", "跨日-X", true, "一次都没有非零"},
	} {
		expectEvidence(t, tc.name, tc.want, func(r evidenceReporter) {
			checkObservedClasses(r, map[string]bool{"x": tc.never}, map[string]string{"x": tc.class}, 1)
		})
	}
}

func TestOfflineCostSideAndDirectionCounts(t *testing.T) {
	p := map[string]Value{"volume_long": {Number: dd("999")}, "volume_short": {Number: dd("999")}, "volume_long_today": {Number: dd("3")}, "volume_long_his": {Number: dd("5")}, "volume_short_today": {Number: dd("7")}, "volume_short_his": {Number: dd("11")}}
	for _, tc := range []struct {
		i     int
		today bool
		want  int64
	}{{0, true, 3}, {0, false, 5}, {1, true, 7}, {1, false, 11}} {
		if got := volumeOnSide(p, tc.i, tc.today); got != tc.want {
			t.Fatalf("成本判别侧别错误：got=%d want=%d", got, tc.want)
		}
	}
	for _, today := range []bool{false, true} {
		if n := countCostDiscrimination(p, today); n != 2 {
			t.Fatalf("成本判别方向丢失：got=%d want=2", n)
		}
	}
	for _, side := range []string{"today", "his"} {
		p["volume_short_"+side] = Value{Number: dd("0")}
	}
	if n := countCostDiscrimination(p, true); n != 1 {
		t.Fatalf("成本判别单方向错误：%d", n)
	}
	if n := volumeOnSide(nil, 0, true); n != 0 {
		t.Fatal("缺持仓被当成有量")
	}
}

func TestOfflineTickOutcomeBothDirections(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		off  bool
		want string
	}{{tickMsg, true, ""}, {limitMsg, false, ""}, {tickMsg, false, "整数倍拒因"}, {limitMsg, true, "涨停拒因"}} {
		expectEvidence(t, tc.msg, tc.want, func(r evidenceReporter) { checkTickOutcome(r, tc.msg, tc.off, "synthetic only") })
	}
}

func TestOfflineNotifyTableDisjoint(t *testing.T) {
	expectEvidence(t, "分离的两表", "", func(r evidenceReporter) {
		checkNotifyTables(r, map[int]string{1: "已知"}, map[int]string{2: "待认"})
	})
	expectEvidence(t, "同码进入两表", "通知已知与待认领表相交", func(r evidenceReporter) {
		checkNotifyTables(r, map[int]string{1: "已知"}, map[int]string{1: "待认"})
	})
}

func TestOfflineSkippedOrdersAndAnyOrders(t *testing.T) {
	empty := &Fixture{HasOrders: true, Orders: map[string]map[string]Value{}}
	held := &Fixture{HasOrders: true, Orders: map[string]map[string]Value{"o": {}}}
	n := 7
	noteSkippedOrders(empty, &n)
	if n != 7 || anyFixtureHasOrders([]*Fixture{empty}) {
		t.Fatal("空委托被误计")
	}
	noteSkippedOrders(held, &n)
	if n != 8 {
		t.Fatalf("跳过委托计数遗漏：%d", n)
	}
	if !anyFixtureHasOrders([]*Fixture{empty, held}) {
		t.Fatal("有委托的夹具被判为空")
	}
}

func TestOfflineCarrySourceSelection(t *testing.T) {
	day := mustDay(t, "20260908")
	today := &Fixture{TradingDay: mustDay(t, "20260909")}
	id, err := types.ParseSymbol("SHFE.rb2701", day)
	if err != nil {
		t.Fatal(err)
	}
	early := &Fixture{TradingDay: day, CapturedAt: "10:00", Trades: []Trade{{Instrument: id}}}
	lateEmpty := &Fixture{TradingDay: day, CapturedAt: "15:00"}
	expectEvidence(t, "最晚零成交不能挤掉来源", "", func(r evidenceReporter) {
		got, ok, why := selectCarryFixture(r, []*Fixture{early, lateEmpty}, today, "SHFE.rb2701")
		if !ok || got != early || why != "" {
			t.Fatalf("结转源筛选错误：%v %s", ok, why)
		}
	})
	if _, ok, _ := selectCarryFixture(t, []*Fixture{lateEmpty}, today, "SHFE.rb2701"); ok {
		t.Fatal("无成交也选出结转源")
	}
	lateOne := &Fixture{TradingDay: day, CapturedAt: "15:01", Trades: []Trade{{Instrument: id}}}
	early.Trades = append(early.Trades, Trade{Instrument: id})
	expectEvidence(t, "最晚快照丢成交", "越晚看到越多", func(r evidenceReporter) { selectCarryFixture(r, []*Fixture{early, lateOne}, today, "SHFE.rb2701") })
}
