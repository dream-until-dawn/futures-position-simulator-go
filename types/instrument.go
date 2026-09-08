// Package types 是本库的枚举与值类型。
//
// # 取值规范化，不绑定线格式
//
// 本库对接两个口子，而它们的线格式并不相同：
//
//	         买卖方向          开平标志
//	CTP      '0' / '1'         '0' '1' '3' '4' …
//	DIFF     BUY / SELL        OPEN / CLOSE / CLOSETODAY
//
// ⚠️ 挑其中一套做内部取值，等于默默偏袒一个口子，另一个口子的转换就会散落到
// 调用处——而散落的转换是本项目一直在防的那类：它们不在一个地方，
// 所以没有任何一处能被完整地检查。
//
// 因此取值规范化，两套映射在本包内显式成对给出，并配往返测试。
//
// # 解析失败必须报错
//
// ⚠️ 所有 FromXxx 解析函数在遇到未知取值时**返回错误**，不返回零值。
// 零值会让「没见过的开平标志」悄悄变成「开仓」——本项目的静默风险清单里，
// 「零值不是安全的默认」排在方法论第一条。
//
// # 依赖
//
// 本包不依赖任何东西，连 decimal 都不依赖。
package types

import (
	"fmt"
	"strings"
)

// Exchange 是交易所标识，取值与 CTP / DIFF 两侧一致（这一项两边恰好相同）。
type Exchange string

const (
	SHFE  Exchange = "SHFE"  // 上海期货交易所
	INE   Exchange = "INE"   // 上海国际能源交易中心
	DCE   Exchange = "DCE"   // 大连商品交易所
	CZCE  Exchange = "CZCE"  // 郑州商品交易所
	CFFEX Exchange = "CFFEX" // 中国金融期货交易所
	GFEX  Exchange = "GFEX"  // 广州期货交易所
)

// AllExchanges 是本库覆盖的全部交易所。
//
// ⚠️ 它只是**身份清单**。「区分今昨仓」「单向大边」这类规则**不在这里**，
// 它们是逐合约的规则数据（`PositionDateType` / `MaxMarginSideAlgorithm`），
// 必须从 refdata 读。按交易所硬编码在绝大多数合约上都对，
// 于是错的那几个不会被任何测试抓到。
var AllExchanges = []Exchange{SHFE, INE, DCE, CZCE, CFFEX, GFEX}

// Valid 报告 e 是否为本库覆盖的交易所。
func (e Exchange) Valid() bool {
	for _, x := range AllExchanges {
		if x == e {
			return true
		}
	}
	return false
}

func (e Exchange) String() string { return string(e) }

// ⚠️ 郑商所的合约代码只带**三位**年月（`TA701`），其余交易所带四位（`rb2701`）。
// 三位形式十年一轮回：`TA701` 既是 2027 年 1 月，也是 2037 年 1 月。
//
// 本库因此区分两种形式：
//
//	规范形式  CZCE.TA2701   四位年月，无歧义，内部与落盘用
//	线格式    CZCE.TA701    交易所与柜台实际收发的形式
//
// 键的选择见 docs/design.md §6.5：持仓/委托/成交按线格式做键是安全的
// （相隔十年的两个合约不可能同时持有），而**规则数据必须按规范形式做键**
// ——一份覆盖十年以上的快照里，两者会撞。
//
// ⚠️ 这条只在跨十年的历史回测上才显形。十年以内的样本上，
// 两种做法给出同一个数——又一个「在最常见的样本上，错误答案等于正确答案」。

// InstrumentID 是一个合约的规范形式：品种 + 四位年月。
type InstrumentID struct {
	Exchange Exchange
	Product  string // 品种代码，保留原始大小写（郑商所大写，其余小写）
	Year     int    // 四位年份，如 2027
	Month    int    // 1..12
}

// Canonical 返回规范形式，如 CZCE.TA2701 / SHFE.rb2701。
func (i InstrumentID) Canonical() string {
	return fmt.Sprintf("%s.%s%04d", i.Exchange, i.Product, i.Year%100*100+i.Month)
}

// Native 返回交易所线格式：郑商所三位年月，其余四位。
func (i InstrumentID) Native() string {
	return string(i.Exchange) + "." + i.NativeInstrument()
}

// NativeInstrument 返回不带交易所前缀的线格式合约代码。
func (i InstrumentID) NativeInstrument() string {
	if i.Exchange == CZCE {
		return fmt.Sprintf("%s%d%02d", i.Product, i.Year%10, i.Month)
	}
	return fmt.Sprintf("%s%02d%02d", i.Product, i.Year%100, i.Month)
}

func (i InstrumentID) String() string { return i.Canonical() }

// YearMonth 返回 yyyymm，便于比较与排序。
func (i InstrumentID) YearMonth() int { return i.Year*100 + i.Month }

// SameProduct 报告两个合约是否同一交易所的同一品种。
//
// ⚠️ 实验 3（单向大边按品种还是按合约合并）要用它，而**它的存在不代表实验 3
// 有了结论**：建这个判据是因为它很便宜——解析郑商所的三位年月本来就要拆出品种
// ——不是因为已经验出「大边按品种合并」。见 docs/roadmap.md。
func (i InstrumentID) SameProduct(o InstrumentID) bool {
	return i.Exchange == o.Exchange && strings.EqualFold(i.Product, o.Product)
}

// ParseNative 把线格式解析成规范形式。
//
// asOf 是**该数据所属的交易日**，用于给郑商所的三位年月定年代。
//
// ⚠️ **不能以「现在」为锚。** 用当前时间解析历史数据，同一段数据在不同年份跑出
// 不同结果——回测就此不可复现，而且不会有任何报错。asOf 必须来自数据本身。
func ParseNative(exchange Exchange, instrument string, asOf TradingDay) (InstrumentID, error) {
	if !exchange.Valid() {
		return InstrumentID{}, fmt.Errorf("未知交易所 %q", exchange)
	}
	product, digits := splitProduct(instrument)
	if product == "" {
		return InstrumentID{}, fmt.Errorf("合约 %q 切不出品种", instrument)
	}
	switch len(digits) {
	case 4: // 四位年月：yymm
		yy := atoi(digits[:2])
		mm := atoi(digits[2:])
		if err := checkMonth(mm, instrument); err != nil {
			return InstrumentID{}, err
		}
		// 四位年月同样只给两位年份，仍需定世纪；但十进制百年一轮回，
		// 用 asOf 的世纪就足够，且本库覆盖范围内不会跨世纪。
		century := asOf.Year() / 100 * 100
		return InstrumentID{exchange, product, century + yy, mm}, nil

	case 3: // 三位年月：ymm，郑商所
		y := atoi(digits[:1])
		mm := atoi(digits[1:])
		if err := checkMonth(mm, instrument); err != nil {
			return InstrumentID{}, err
		}
		year, err := resolveDecade(y, mm, asOf)
		if err != nil {
			return InstrumentID{}, fmt.Errorf("合约 %q: %w", instrument, err)
		}
		return InstrumentID{exchange, product, year, mm}, nil

	default:
		return InstrumentID{}, fmt.Errorf("合约 %q 的年月位数是 %d，只支持 3 或 4",
			instrument, len(digits))
	}
}

// resolveDecade 给三位年月定年代：取**离 asOf 最近**的那个同尾数年份。
//
// 郑商所挂牌的合约通常在两年以内，而同尾数的年份相隔十年，
// 所以「最近」是无歧义的判据，不需要任何人为的宽限窗口。
//
// ⚠️ 恰好等距（距今 60 个月）时无法判定。那意味着一个五年后的合约，
// 现实中不存在；本函数**报错**而不是随便挑一个——
// 「测不出」和「答案是某某」必须分开。
func resolveDecade(lastDigit, month int, asOf TradingDay) (int, error) {
	base := asOf.Year()/10*10 + lastDigit
	anchor := asOf.Year()*12 + int(asOf.Month())

	best, bestDist := 0, 1<<30
	tie := false
	for _, y := range []int{base - 10, base, base + 10} {
		d := y*12 + month - anchor
		if d < 0 {
			d = -d
		}
		switch {
		case d < bestDist:
			best, bestDist, tie = y, d, false
		case d == bestDist:
			tie = true
		}
	}
	if tie {
		return 0, fmt.Errorf("三位年月距 %d 恰好等距（%d 个月），无法定年代", asOf, bestDist)
	}
	return best, nil
}

func splitProduct(instrument string) (product, digits string) {
	i := 0
	for i < len(instrument) && !isDigit(instrument[i]) {
		i++
	}
	if i == 0 || i == len(instrument) {
		return "", ""
	}
	for j := i; j < len(instrument); j++ {
		if !isDigit(instrument[j]) {
			return "", "" // 数字后面又出现字母，不是本库支持的形态
		}
	}
	return instrument[:i], instrument[i:]
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		n = n*10 + int(s[i]-'0')
	}
	return n
}

func checkMonth(m int, instrument string) error {
	if m < 1 || m > 12 {
		return fmt.Errorf("合约 %q 的月份是 %d，应在 1..12", instrument, m)
	}
	return nil
}

// ParseSymbol 解析 "EXCHANGE.instrument" 形式的线格式键。
func ParseSymbol(symbol string, asOf TradingDay) (InstrumentID, error) {
	dot := strings.IndexByte(symbol, '.')
	if dot <= 0 || dot == len(symbol)-1 {
		return InstrumentID{}, fmt.Errorf("合约键 %q 不是 EXCHANGE.instrument 形式", symbol)
	}
	return ParseNative(Exchange(symbol[:dot]), symbol[dot+1:], asOf)
}
