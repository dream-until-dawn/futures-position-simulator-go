package refdata

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// trickySnapshot 造一份**专挑浮点会出错的数**的快照。
//
// ⚠️ 0.07 / 12.07125 / 0.00001 都是本项目实测里真实出现过的值。
// 用真实量到的数，而不是 0.1+0.2 那类经典例子 —— 后者证明的是浮点的一般性质，
// 前者证明的是**这个库会不会在它真正处理的数上出错**。
func trickySnapshot(t *testing.T) (*Snapshot, *Calendar) {
	t.Helper()
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
	b := NewBuilder(20260908)
	b.AddInstrument(Instrument{
		ID: id, VolumeMultiple: dec("10"), PriceTick: dec("1"),
		PositionDateType: UseHistory, MaxMarginSide: true,
		ExpireDate: 20270115, IsTrading: true,
		MinLimitOrderVolume: 1, MaxLimitOrderVolume: 500,
		PriceLimitRatio: dec("0.07"), HasPriceLimitRatio: true,
	})
	b.AddMarginRates(id, types.Speculation, MarginRates{
		LongByMoney: dec("0.07"), LongByVolume: dec("0"),
		ShortByMoney: dec("0.07"), ShortByVolume: dec("0"),
		CompanyAddOn: dec("0.02"),
	})
	b.AddCommissionRates(id, types.Speculation, CommissionRates{
		OpenByMoney: dec("0.00001"), OpenByVolume: dec("0"),
		CloseByMoney: dec("0.00001"), CloseByVolume: dec("0"),
		CloseTodayByMoney: dec("0"), CloseTodayByVolume: dec("12.07125"),
	})
	snap, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	cal, err := NewCalendar(
		[]types.TradingDay{20260907, 20260908, 20260909},
		[]SessionTable{{Exchange: types.SHFE, Product: "rb",
			Day:   []Session{{MustClockTime(9, 0, 0), MustClockTime(15, 0, 0)}},
			Night: []Session{{MustClockTime(21, 0, 0), MustClockTime(23, 0, 0)}}}},
		[]int32{20260907})
	if err != nil {
		t.Fatal(err)
	}
	return snap, cal
}

func saveTo(t *testing.T, s *Snapshot, cal *Calendar, ev Evidence) []byte {
	t.Helper()
	var buf bytes.Buffer
	at := time.Date(2026, 9, 8, 10, 0, 0, 0, CNZone())
	if err := s.Save(&buf, ev, "单元测试", at, cal); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestSnapshotRoundTripIsExact 断言往返**逐位精确**，不是「差不多」。
func TestSnapshotRoundTripIsExact(t *testing.T) {
	snap, cal := trickySnapshot(t)
	raw := saveTo(t, snap, cal, EvidenceMeasuredKQ)

	got, gotCal, ev, err := Load(bytes.NewReader(raw), LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if ev != EvidenceMeasuredKQ {
		t.Errorf("证据等级往返丢了：%q", ev)
	}
	id := types.InstrumentID{Exchange: types.SHFE, Product: "rb", Year: 2027, Month: 1}
	a, err := snap.Instrument(id)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := got.Instrument(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		x, y decimal.Decimal
	}{
		{"乘数", a.VolumeMultiple, bb.VolumeMultiple},
		{"最小变动价位", a.PriceTick, bb.PriceTick},
		{"涨跌幅比例", a.PriceLimitRatio, bb.PriceLimitRatio},
	} {
		if !c.x.Equal(c.y) {
			t.Errorf("%s 往返后变了：%s → %s", c.name, c.x, c.y)
		}
	}
	// ⚠️ 配对的布尔必须一起往返：漏掉它，「比例是零」与「不知道比例」就压成一个值。
	if a.HasPriceLimitRatio != bb.HasPriceLimitRatio {
		t.Error("⚠️ HasPriceLimitRatio 往返丢了 —— 「比例是零」与「不知道比例」被压成同一个值")
	}
	if a.PositionDateType != bb.PositionDateType || a.MaxMarginSide != bb.MaxMarginSide ||
		a.ExpireDate != bb.ExpireDate || a.IsTrading != bb.IsTrading ||
		a.MinLimitOrderVolume != bb.MinLimitOrderVolume ||
		a.MaxLimitOrderVolume != bb.MaxLimitOrderVolume {
		t.Errorf("合约规格往返后不一致：%+v vs %+v", a, bb)
	}

	m1, _ := snap.MarginRates(id, types.Speculation)
	m2, _ := got.MarginRates(id, types.Speculation)
	if !m1.LongByMoney.Equal(m2.LongByMoney) {
		t.Errorf("保证金率往返后变了：%s → %s", m1.LongByMoney, m2.LongByMoney)
	}
	// ⚠️ 漏掉 CompanyAddOn，往返之后加收变 0 —— 那个方向是**低估保证金占用**。
	if !m1.CompanyAddOn.Equal(m2.CompanyAddOn) {
		t.Errorf("⚠️ CompanyAddOn 往返后 %s → %s —— 低估保证金占用，"+
			"在回测里表现为「比真实账户能开更多仓」", m1.CompanyAddOn, m2.CompanyAddOn)
	}
	c1, _ := snap.CommissionRates(id, types.Speculation)
	c2, _ := got.CommissionRates(id, types.Speculation)
	if !c1.CloseTodayByVolume.Equal(c2.CloseTodayByVolume) {
		t.Errorf("平今每手费往返后变了：%s → %s", c1.CloseTodayByVolume, c2.CloseTodayByVolume)
	}

	if gotCal == nil {
		t.Fatal("日历没往返回来")
	}
	for _, when := range []string{"2026-09-08 10:00:00", "2026-09-08 21:30:00"} {
		tm, _ := time.ParseInLocation("2006-01-02 15:04:05", when, CNZone())
		want, err1 := cal.TradingDayAt(tm, types.SHFE, "rb")
		gotDay, err2 := gotCal.TradingDayAt(tm, types.SHFE, "rb")
		if (err1 == nil) != (err2 == nil) || want != gotDay {
			t.Errorf("%s 的交易日往返后不一致：%d/%v vs %d/%v", when, want, err1, gotDay, err2)
		}
	}
	// ⚠️ 停夜盘那天也要往返：它是**长假前夜盘不开**那条例外的唯一载体。
	tm, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-09-07 21:30:00", CNZone())
	if _, err := gotCal.TradingDayAt(tm, types.SHFE, "rb"); err == nil {
		t.Error("⚠️ nightSuspended 往返丢了 —— 停夜盘的那一晚又能算出交易日了")
	}
}

// TestDecimalsAreStringsInWire 断言线格式里小数是**字符串**，不是 JSON 数字。
//
// ⚠️ 这不是风格洁癖：JSON 数字过一次 float64 就不再是原来的数。
// 本项目已经量到过这个形状 —— 夹具里存着 commission: 126.95459999999999，
// 而资金恒等式因此只能写成「在 float64 表示误差内成立」。
// 持久化是本库自己能控制的那一段，没有理由在这里再引入一次。
func TestDecimalsAreStringsInWire(t *testing.T) {
	snap, cal := trickySnapshot(t)
	raw := string(saveTo(t, snap, cal, EvidenceDocumented))
	for _, want := range []string{"\"0.07\"", "\"12.07125\"", "\"0.00001\"", "\"0.02\""} {
		if !strings.Contains(raw, want) {
			t.Errorf("线格式里找不到字符串形式的 %s —— 它可能被写成了 JSON 数字", want)
		}
	}
	for _, bad := range []string{": 0.07,", ": 12.07125,", ": 0.00001,"} {
		if strings.Contains(raw, bad) {
			t.Errorf("⚠️ 线格式里出现了裸的 JSON 数字 %q —— 过一次 float64 就不再是原来的数", bad)
		}
	}
}

// TestSaveIsDeterministic 断言同一份数据两次生成产生同样的字节。
//
// ⚠️ 内置快照是要进 git 的。一个每次生成都变的文件，
// 会让「这次快照到底改了什么」无从查起 —— 而那是它进 git 的全部意义。
func TestSaveIsDeterministic(t *testing.T) {
	snap, cal := trickySnapshot(t)
	a := saveTo(t, snap, cal, EvidenceDocumented)
	b := saveTo(t, snap, cal, EvidenceDocumented)
	if !bytes.Equal(a, b) {
		t.Error("⚠️ 同一份数据两次生成的字节不同 —— 快照进 git 之后每次都会有 diff")
	}
	if len(a) < 200 {
		t.Fatalf("生成的快照只有 %d 字节 —— 太小，本条可能在比较两个空串", len(a))
	}
}

// TestLoadRefusesPlaceholder 断言占位快照默认不许加载。
func TestLoadRefusesPlaceholder(t *testing.T) {
	snap, cal := trickySnapshot(t)
	raw := saveTo(t, snap, cal, EvidencePlaceholder)
	if _, _, _, err := Load(bytes.NewReader(raw), LoadOptions{}); err == nil {
		t.Error("⚠️ 占位快照被默认加载了 —— 调用方拿到的会是「查不到这个合约」，" +
			"而不是「本库还没有参考数据」。两者在代码里长得一样，在排查时差得很远")
	}
	if _, _, ev, err := Load(bytes.NewReader(raw), LoadOptions{AllowPlaceholder: true}); err != nil {
		t.Errorf("显式允许之后仍被拒：%v", err)
	} else if ev != EvidencePlaceholder {
		t.Errorf("证据等级应为 placeholder，实为 %q", ev)
	}
}

// TestLoadGoesThroughBuilder 断言加载路径**走同一套构造期校验**。
//
// ⚠️ 快照文件可以手改。若 Load 直接把字段塞进 Snapshot，
// Builder 的全部校验在加载路径上就失效了 —— 这与「两个实现一起退化」同形，
// 只是两条路径一条是构造、一条是加载。
func TestLoadGoesThroughBuilder(t *testing.T) {
	cases := []struct {
		name   string
		from   string
		to     string
		msgHas string
	}{
		{"乘数为零", "\"volume_multiple\": \"10\"", "\"volume_multiple\": \"0\"", "构造期校验"},
		{"今昨仓类型是空串", "\"position_date_type\": \"use_history\"", "\"position_date_type\": \"\"", "不认识"},
		{"多出一个字段", "\"version\":", "\"未知字段\": 1, \"version\":", "解析失败"},
		{"没有版本号", "\"version\": 20260908", "\"version\": 0", "没有版本号"},
		{"证据等级自造", "\"evidence\": \"documented\"", "\"evidence\": \"我说它对\"", "不是已知的四档"},
	}
	if len(cases) != 5 {
		t.Fatalf("用例 %d 条，应为 5 —— 增删了就同步改这个数", len(cases))
	}
	snap, cal := trickySnapshot(t)
	base := string(saveTo(t, snap, cal, EvidenceDocumented))
	// 对照组：未改动的必须能加载。这一条不通过，下面几条什么都不说明。
	if _, _, _, err := Load(strings.NewReader(base), LoadOptions{}); err != nil {
		t.Fatalf("前提变了：未改动的快照加载失败：%v", err)
	}
	for _, c := range cases {
		mutated := strings.Replace(base, c.from, c.to, 1)
		// ⚠️ 零层：先确认破坏落到了目标上。绿也可能绿错理由。
		if mutated == base {
			t.Errorf("⚠️ %s：改动没生效（找不到 %q）—— 破坏没落到目标上", c.name, c.from)
			continue
		}
		_, _, _, err := Load(strings.NewReader(mutated), LoadOptions{})
		if err == nil {
			t.Errorf("⚠️ %s：手改过的快照被接受了", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.msgHas) {
			t.Errorf("%s：错误信息里没有 %q：%v", c.name, c.msgHas, err)
		}
	}
}
