package ctp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	def "gitee.com/haifengat/goctp/ctpdefine"
)

// TestSanitizeDropsIdentifiersAndKeepsMoney 是脱敏的正面 + 反面用例。
//
// ⚠️ 两侧都要断言：只查「标识性字段没了」的话，一个**把所有字段都丢掉**的
// 实现同样通过 —— 而那样的夹具在下游是「跳过」，不是「报错」。
func TestSanitizeDropsIdentifiersAndKeepsMoney(t *testing.T) {
	var acc def.CThostFtdcTradingAccountField
	copy(acc.BrokerID[:], "9999")
	copy(acc.AccountID[:], "SECRET-ACCOUNT-ID")
	copy(acc.CurrencyID[:], "CNY")
	acc.Balance = 20000000
	acc.Available = 19999999.5

	out, dropped, err := sanitizeStruct(acc, accountFields)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"BrokerID", "AccountID"} {
		if _, ok := out[k]; ok {
			t.Errorf("⚠️ 标识性字段 %s **留在了**落盘内容里", k)
		}
	}
	// ⚠️ 不能直接跟无类型常量比：CTP 的金额是 TThostFtdcMoneyType（**具名** float64），
	// 装进 any 之后动态类型不是 float64，`got != 20000000.0` 永远为真。
	// 第一版就是这么写的，红了才发现 —— 而它红得对：**比的是类型不是值**。
	got, ok := out["Balance"]
	if !ok {
		t.Errorf("⚠️ Balance 没留下 —— 一个把所有字段都丢掉的实现" +
			"也能通过上面那半，所以这一半不能省")
	} else if f, isNum := toFloat(got); !isNum || f != 20000000 {
		t.Errorf("⚠️ Balance 值错了：%#v", got)
	}
	if got := out["CurrencyID"]; got != "CNY" {
		t.Errorf("⚠️ CurrencyID 应当被规范成字符串 \"CNY\"，得到 %#v —— "+
			"CTP 的字符串字段是定长字节数组，直接落盘会变成一串数字，"+
			"**看起来像数据而不像 bug**", got)
	}
	if len(dropped) != 2 {
		t.Errorf("⚠️ dropped 记了 %v，应当恰好是那两个标识性字段 —— "+
			"逐个记名是为了让脱敏可审计", dropped)
	}
}

// TestSanitizeRefusesUndecidedField 钉住运行期那一道。
//
// ⚠️ 编译期已经有 TestFieldDecisionsAreComplete 穷举了，这一条是**第二道**：
// 两者会一起用到，理由是它们**在不同时刻失效**——
// 前者在改代码时说话，后者在换了 goctp 版本、结构体多出字段时说话。
func TestSanitizeRefusesUndecidedField(t *testing.T) {
	partial := map[string]decision{"BrokerID": drop("测试用")}
	_, _, err := sanitizeStruct(def.CThostFtdcTradingAccountField{}, partial)
	if err == nil {
		t.Fatal("⚠️ 决定表里只有一个字段，sanitizeStruct 却成功了 —— " +
			"没有决定的字段被**默默丢掉**了，而那会让「新加的字段」" +
			"表现成「柜台没给这个字段」")
	}
	if !strings.Contains(err.Error(), "没有决定") {
		t.Errorf("⚠️ 报错了，但不是因为「没有决定」：%v", err)
	}
}

// TestScrubbedIsIndependentOfTheWhitelist 钉住第二道防线**真的是独立的**。
//
// ⚠️ 构造方式很关键：这里**故意**把凭据放进一个被 keep 的字段里，
// 模拟「白名单误判」。白名单这时一个字都不会说，而 Scrubbed 必须说。
func TestScrubbedIsIndependentOfTheWhitelist(t *testing.T) {
	var acc def.CThostFtdcTradingAccountField
	copy(acc.CurrencyID[:], "CNY")
	// CurrencyID 是 keep 的；把凭据塞进另一个 keep 字段里。
	copy(acc.BrokerID[:], "9999")
	out, _, err := sanitizeStruct(acc, accountFields)
	if err != nil {
		t.Fatal(err)
	}
	out["Reserve"] = "APPID-1234567890" // ⚠️ 模拟：一个被 keep 的字段里混进了凭据
	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{"CTP_APP_ID": "APPID-1234567890"}
	if err := Scrubbed(string(blob), secrets); err == nil {
		t.Fatal("⚠️ 凭据混在一个**被 keep 的字段**里，Scrubbed 却放行了 —— " +
			"白名单这时一个字都不会说，第二道防线因此必须独立于它")
	}
	// 反面：没有凭据时不许误报。
	if err := Scrubbed(`{"Balance":1}`, secrets); err != nil {
		t.Errorf("⚠️ 干净内容被误报：%v —— 一条老是误报的检查最后一定会被关掉", err)
	}
}

// TestBlindSpotsSaysWhatItCannotCheck 钉住「明说查不了什么」。
//
// ⚠️ 免得「没报错」被读成「都查过了」。
func TestBlindSpotsSaysWhatItCannotCheck(t *testing.T) {
	got := BlindSpots(map[string]string{"CTP_BROKER_ID": "9999", "CTP_APP_ID": "APPID-1234567890"})
	if len(got) != 1 || got[0] != "CTP_BROKER_ID" {
		t.Errorf("⚠️ 盲区报告是 %v —— 应当**只**点名 CTP_BROKER_ID（太短查不了）。"+
			"在一份满是数字的夹具里搜一个四位串只会撞上价格，没有判别力", got)
	}
}

// toFloat 把任意数值型（含具名类型）转成 float64。
func toFloat(v any) (float64, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	}
	return 0, false
}
