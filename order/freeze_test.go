package order

import (
	"strings"
	"testing"

	"github.com/dream-until-dawn/futures-position-simulator-go/types"
	"github.com/shopspring/decimal"
)

func openIn() FreezeInput  { return FreezeInput{Margin: d("2214.1"), Commission: d("0.3163")} }
func closeIn() FreezeInput { return FreezeInput{Margin: decimal.Zero, Commission: d("0.3163")} }

// TestFreezeOfSeparatesTwoSides 断言**开仓冻金额、平仓冻手数**。
//
// ⚠️ 两侧合并的话，「开仓单冻了保证金」与「平仓单冻了手数」会落进同一个数，
// 而它们影响的是柜台截面上**完全不同的字段**
// （account.frozen_margin vs position.volume_*_frozen_*），
// 释放时机也不同。合并之后对拍时两边都填不对。
func TestFreezeOfSeparatesTwoSides(t *testing.T) {
	inst := rb2701(t)
	cases := []struct {
		name    string
		req     Request
		in      FreezeInput
		wantMar string
		wantT   int
		wantH   int
	}{
		{"开仓：冻保证金与手续费，不冻手数",
			Request{inst, types.Buy, types.Open, types.Speculation, d("3163"), 1},
			openIn(), "2214.1", 0, 0},
		{"平今：冻今仓手数，不冻保证金",
			Request{inst, types.Sell, types.CloseToday, types.Speculation, d("3163"), 2},
			closeIn(), "0", 2, 0},
		{"平昨：冻昨仓手数",
			Request{inst, types.Sell, types.CloseYesterday, types.Speculation, d("3163"), 3},
			closeIn(), "0", 0, 3},
	}
	for _, c := range cases {
		got, err := FreezeOf(c.req, c.in)
		if err != nil {
			t.Errorf("%s：%v", c.name, err)
			continue
		}
		if !got.Margin.Equal(d(c.wantMar)) {
			t.Errorf("%s：冻结保证金 %s，应为 %s", c.name, got.Margin, c.wantMar)
		}
		if got.VolumeToday != c.wantT || got.VolumeHistory != c.wantH {
			t.Errorf("%s：冻结手数 今%d/昨%d，应为 今%d/昨%d",
				c.name, got.VolumeToday, got.VolumeHistory, c.wantT, c.wantH)
		}
		// 手续费开平都冻 —— 平仓一样要收，且同样在成交前扣。
		if !got.Commission.Equal(d("0.3163")) {
			t.Errorf("%s：冻结手续费 %s，应为 0.3163", c.name, got.Commission)
		}
	}
	// ⚠️ 判别力：三条用例必须**真的不同**，否则「两侧分开」没被验到。
	o, _ := FreezeOf(cases[0].req, cases[0].in)
	ct, _ := FreezeOf(cases[1].req, cases[1].in)
	if o.Margin.Equal(ct.Margin) || o.VolumeToday == ct.VolumeToday {
		t.Fatal("⚠️ 开仓与平今的冻结结果相同 —— 两侧等于没分开")
	}
}

// TestFreezeRefusesSuspiciousZero 断言**开仓冻结额为零要报错**。
//
// ⚠️ 没有哪个合约开仓不占保证金。让 0 静默通过，账户的 Available
// 会多出一大截，而回测据此开出实际开不出的仓 —— 那个错的方向是
// 「看起来钱更多」，比看起来钱更少危险得多。
func TestFreezeRefusesSuspiciousZero(t *testing.T) {
	req := Request{rb2701(t), types.Buy, types.Open, types.Speculation, d("3163"), 1}
	_, err := FreezeOf(req, FreezeInput{Margin: decimal.Zero, Commission: d("0.3")})
	if err == nil {
		t.Fatal("⚠️ 开仓冻结保证金 0 竟然通过了 —— " +
			"账户的可用资金会多出一大截，而回测据此开出实际开不出的仓")
	}
	if !strings.Contains(err.Error(), "没算") {
		t.Errorf("报错了但没说清 0 是「没算」的伪装：%v", err)
	}
	// 平仓单的 Margin 为零是**正常**的 —— 不许被同一条规则拦下。
	cl := Request{rb2701(t), types.Sell, types.CloseToday, types.Speculation, d("3163"), 1}
	if _, err := FreezeOf(cl, closeIn()); err != nil {
		t.Errorf("⚠️ 平仓单的零保证金被拦下了：%v —— "+
			"那条规则只该管开仓，管到平仓就是误伤", err)
	}
}

// TestFreezeRefusesNakedClose 断言裸 CLOSE 的冻结**算不出来**。
//
// ⚠️ 不知道该冻今仓还是昨仓时，猜一边冻会让另一边的可平量凭空多出来。
// 本库对裸 CLOSE 本来就报错（simnow_pending#1 未裁决），
// 走到这里说明校验被绕过了 —— 那时更要报错，不要接着猜。
func TestFreezeRefusesNakedClose(t *testing.T) {
	req := Request{rb2701(t), types.Sell, types.Close, types.Speculation, d("3163"), 1}
	_, err := FreezeOf(req, closeIn())
	if err == nil {
		t.Fatal("⚠️ 裸 CLOSE 竟然算出了冻结 —— 它必然猜了一边")
	}
	if !strings.Contains(err.Error(), "校验被绕过") {
		t.Errorf("报错了但没指出这是校验被绕过的信号：%v", err)
	}
}

// TestBookAggregates 断言委托簿的合计与逐合约逐方向的分组。
func TestBookAggregates(t *testing.T) {
	inst := rb2701(t)
	b := NewBook()
	if !b.Total().IsZero() {
		t.Fatal("空簿的合计不是零")
	}
	if err := b.Insert("o1",
		Request{inst, types.Buy, types.Open, types.Speculation, d("3163"), 1}, openIn()); err != nil {
		t.Fatal(err)
	}
	if err := b.Insert("o2",
		Request{inst, types.Sell, types.CloseToday, types.Speculation, d("3163"), 2}, closeIn()); err != nil {
		t.Fatal(err)
	}
	tot := b.Total()
	if !tot.Margin.Equal(d("2214.1")) {
		t.Errorf("合计保证金 %s，应为 2214.1（只有开仓那笔冻）", tot.Margin)
	}
	if !tot.Commission.Equal(d("0.6326")) {
		t.Errorf("合计手续费 %s，应为 0.6326（两笔都冻）", tot.Commission)
	}
	if tot.VolumeToday != 2 {
		t.Errorf("合计冻结今仓 %d 手，应为 2", tot.VolumeToday)
	}

	// ⚠️ 逐方向：o2 是 SELL/CLOSETODAY，平的是**多头**。
	long := b.TotalOf(inst, types.Buy)
	if long.VolumeToday != 2 {
		t.Errorf("⚠️ 多头方向冻结今仓 %d 手，应为 2 —— "+
			"SELL/CLOSETODAY 平的是多头，方向搞反会填错字段", long.VolumeToday)
	}
	short := b.TotalOf(inst, types.Sell)
	if short.VolumeToday != 0 {
		t.Errorf("⚠️ 空头方向不该有冻结，得到 %d 手", short.VolumeToday)
	}
	// ⚠️ 判别力：两个方向必须**不同**，否则「逐方向」没被验到。
	if long.VolumeToday == short.VolumeToday {
		t.Fatal("⚠️ 两个方向的冻结相同 —— 逐方向分组等于没分")
	}

	// —— 释放 ——
	rel, err := b.Remove("o2")
	if err != nil {
		t.Fatal(err)
	}
	if rel.VolumeToday != 2 {
		t.Errorf("释放的冻结今仓 %d 手，应为 2", rel.VolumeToday)
	}
	if got := b.Total().VolumeToday; got != 0 {
		t.Errorf("⚠️ 撤单后仍冻着 %d 手 —— 那些手数从此平不掉", got)
	}
	// ⚠️ 释放两次必须报错：第二次会凭空多出一份可用资金。
	if _, err := b.Remove("o2"); err == nil {
		t.Error("⚠️ 同一笔委托释放了两次却没报错 —— " +
			"第二次释放会凭空多出一份可用资金")
	}
}

// TestBookRefusesDuplicateID 断言同 id 插两次报错。
//
// ⚠️ 覆盖会让前一笔的冻结凭空消失，而账面上只表现为「可用资金多了一点」——
// 那个方向是「看起来钱更多」。
func TestBookRefusesDuplicateID(t *testing.T) {
	inst := rb2701(t)
	b := NewBook()
	req := Request{inst, types.Buy, types.Open, types.Speculation, d("3163"), 1}
	if err := b.Insert("x", req, openIn()); err != nil {
		t.Fatal(err)
	}
	if err := b.Insert("x", req, openIn()); err == nil {
		t.Fatal("⚠️ 同一个委托编号插了两次却没报错")
	}
	if err := b.Insert("", req, openIn()); err == nil {
		t.Error("⚠️ 空委托编号被接受了 —— 空 id 会让两笔单互相覆盖")
	}
	if n := len(b.Live()); n != 1 {
		t.Errorf("簿上应当只有 1 笔，得到 %d", n)
	}
}

// TestFrozenAddIsAdditive 断言累加不丢项。
//
// ⚠️ 四个字段里漏加一个，合计会**偏小**，而偏小的方向是
// 「看起来钱更多 / 可平量更多」—— 那类错不会爆仓，只会让回测
// 开出或平掉实际做不到的量，且全程没有报错。
func TestFrozenAddIsAdditive(t *testing.T) {
	a := Frozen{Margin: d("1"), Commission: d("2"), VolumeToday: 3, VolumeHistory: 4}
	sum := a.Add(a)
	if !sum.Margin.Equal(d("2")) || !sum.Commission.Equal(d("4")) ||
		sum.VolumeToday != 6 || sum.VolumeHistory != 8 {
		t.Errorf("⚠️ 累加漏了字段：%+v —— 合计偏小的方向是「看起来钱更多」", sum)
	}
	if a.IsZero() {
		t.Error("非零的冻结被判成零")
	}
	if !(Frozen{Margin: decimal.Zero, Commission: decimal.Zero}).IsZero() {
		t.Error("零冻结没被判成零")
	}
}

// TestCloseWithMarginIsCallerError 断言**平仓单带着非零保证金要报错**。
//
// ⚠️ 这一条是破坏验证逼出来的。原来的用例给平仓单传的 Margin 本来就是零，
// 于是「平仓不冻保证金」与「冻了但恰好是零」**给出同一个结果** ——
// 把 `f.Margin = in.Margin` 加进平仓分支，测试照样绿。
//
// 判据必须落在**非零**输入上：那才是唯一能把两者分开的场合。
// ⚠️ 而选择「报错」而不是「忽略」，是因为忽略在结果上与冻结相同，
// 于是这条性质会再一次变得测不出来。
func TestCloseWithMarginIsCallerError(t *testing.T) {
	inst := rb2701(t)
	for _, off := range []types.Offset{types.CloseToday, types.CloseYesterday} {
		req := Request{inst, types.Sell, off, types.Speculation, d("3163"), 1}
		_, err := FreezeOf(req, FreezeInput{Margin: d("2214.1"), Commission: d("0.3")})
		if err == nil {
			t.Errorf("⚠️ %v 带着非零保证金却通过了 —— "+
				"那意味着「平仓不冻保证金」这条性质根本没被验证", off)
			continue
		}
		if !strings.Contains(err.Error(), "平仓不额外冻") {
			t.Errorf("%v：报错了但没说清理由：%v", off, err)
		}
	}
	// 零的那一支必须仍然通过 —— 否则上面那条只是「什么都拦」。
	ok := Request{inst, types.Sell, types.CloseToday, types.Speculation, d("3163"), 1}
	if _, err := FreezeOf(ok, closeIn()); err != nil {
		t.Errorf("⚠️ 正常的平仓单被拦下了：%v —— 那说明上面那条只是「什么都拦」", err)
	}
}
