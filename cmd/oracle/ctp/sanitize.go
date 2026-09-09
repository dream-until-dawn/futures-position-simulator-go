package ctp

import (
	"path/filepath"
	"os"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Source 是 CTP 夹具的来源标记。
//
// ⚠️ 它存在是为了**不被 DIFF 那侧的加载器吃掉**。
// `conformance/fixture` 的 loadAll 扫的是 `testdata/probes/*.json`，
// 而它把持仓字段当成一个泛型 map 读 —— 一份 CTP 夹具丢进那个目录，
// 它会**若无其事地解析成功**，然后带着一堆 CTP 字段名流进棘轮与对拍。
//
// 于是两道防线：CTP 夹具落在 `testdata/ctp/`，且带这个标记；
// 主模块那侧有守卫查 `testdata/probes/` 里没有带这个标记的文件。
const Source = "ctp-simnow"

// Fixture 是一份 CTP 侧的实测截面。
//
// ⚠️ 它与 `kq.Fixture` **形状相近但不是同一个类型**，
// 理由与两个客户端不做统一抽象相同（docs/ctp-oracle.md 第 4 节）：
// 字段名一个都不一样，硬共用一个类型只会让「哪些字段来自哪个口子」变模糊。
type Fixture struct {
	Source     string `json:"source"`
	TradingDay string `json:"trading_day"`
	CapturedAt string `json:"captured_at"`
	Note       string `json:"note,omitempty"`

	// BrokerParams 是经纪商交易参数。⚠️ 它是**声明**不是行为，见 ctp-oracle.md 第 3 节。
	BrokerParams map[string]any `json:"broker_params,omitempty"`
	// Account 是资金账户截面。
	Account map[string]any `json:"account,omitempty"`
	// Positions 按 "SHFE.rb2701" 键。
	Positions map[string]map[string]any `json:"positions,omitempty"`

	// Dropped 逐个记下**被白名单去掉的键名**（不记值）。
	//
	// ⚠️ 记名字是为了让脱敏**可审计**：评审能看出哪些键被拿掉了，
	// 而不必对着原始快照猜。⚠️ 只记名字不记值 —— 记了值这一段本身就是泄漏。
	Dropped []string `json:"dropped"`
}

// sanitizeStruct 按决定表把一个 CTP 结构体变成可落盘的 map。
//
// ⚠️ 它对**没有决定**的字段的处理是**报错**，不是默默丢掉：
// 默默丢掉会让「新加的字段」表现成「柜台没给这个字段」，
// 而那两件事要人做的完全不同。
// （编译期那一层由 TestFieldDecisionsAreComplete 兜着，这里是运行期的第二道。）
func sanitizeStruct(v any, decisions map[string]decision) (map[string]any, []string, error) {
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	out := map[string]any{}
	var dropped []string
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		d, ok := decisions[f.Name]
		if !ok {
			return nil, nil, fmt.Errorf("字段 %s.%s **没有决定** —— 不落盘。"+
				"⚠️ 这里不默默丢掉它：那会让「新加的字段」表现成「柜台没给这个字段」，"+
				"而两件事要人做的完全不同", rt.Name(), f.Name)
		}
		if !d.Keep {
			dropped = append(dropped, rt.Name()+"."+f.Name)
			continue
		}
		if !f.IsExported() {
			// ⚠️ 未导出字段（如 reserve1）读不到值。它已经被判成 drop，
			// 走不到这里；走到了说明决定表与结构体对不上。
			return nil, nil, fmt.Errorf("字段 %s.%s 被判为 keep，但它是**未导出**的，读不到值",
				rt.Name(), f.Name)
		}
		out[f.Name] = normalize(rv.Field(i))
	}
	sort.Strings(dropped)
	return out, dropped, nil
}

// text 把 CTP 的定长字节数组（尾部填 0）截成字符串。
//
// ⚠️ 它放在**平台无关**的文件里：脱敏要用它，而脱敏本身与平台无关。
// 放在 client_windows.go 里会让 GOOS=linux 的构建报 undefined —— 踩过一次。
// Text 是 text 的导出版本，供 cmd/oracle 主包用。
func Text(b []byte) string { return text(b) }

func text(b []byte) string {
	for i, ch := range b {
		if ch == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// normalize 把 CTP 的定长字节数组变成字符串，其余原样。
//
// ⚠️ CTP 的字符串字段是 `[N]byte`，尾部填 0。直接落盘会得到一个
// 数字数组 —— 看得懂但没法比，而且**看起来像数据而不是像 bug**。
func normalize(v reflect.Value) any {
	if v.Kind() == reflect.Array && v.Type().Elem().Kind() == reflect.Uint8 {
		b := make([]byte, v.Len())
		reflect.Copy(reflect.ValueOf(b), v)
		return text(b)
	}
	if v.Kind() == reflect.Uint8 {
		// 单字节枚举（如 PosiDirection）：落成字符，别落成数字。
		//
		// ⚠️ 0 值单独处理：CTP 用**零字节**表示「没有设」，而它落进 JSON 会变成
		// 一个**字符串里的 NUL 控制字符**。那是合法 JSON，
		// 但不少工具会噎住，而噎住的表现是「夹具读不了」，与「夹具错了」长得一样。
		// ⚠️ 映射到 "" 不会有歧义：CTP 的枚举取值是可见字符（'0' 是 0x30），
		// 与 0x00 不是一回事。
		if v.Uint() == 0 {
			return ""
		}
		return string(rune(v.Uint()))
	}
	return v.Interface()
}

// Scrubbed 是**独立于白名单的第二道检查**：在落盘内容里搜凭据本身。
//
// ⚠️ 与白名单是两套不同原理的机制，因此不会一起失效：
// 白名单管「哪些键能留」，这一条管「留下的值里有没有混进凭据」——
// 一个字段被误判成 keep 时，白名单不会说话，而这一条会。
func Scrubbed(blob string, secrets map[string]string) error {
	var hit []string
	for name, v := range secrets {
		// ⚠️ 太短的值不查：在一份满是数字的夹具里搜一个两三位的串，
		// 只会撞上价格，没有判别力。查不了就**说出来**，见 BlindSpots。
		if len(v) < 8 {
			continue
		}
		if strings.Contains(blob, v) {
			hit = append(hit, name)
		}
	}
	sort.Strings(hit)
	if len(hit) > 0 {
		return fmt.Errorf("⚠️ 落盘内容里出现了凭据 %v —— **不落盘**。"+
			"⚠️ 白名单漏了一个键，而这一条是独立的第二道防线", hit)
	}
	return nil
}

// BlindSpots 报告哪些凭据**短到查不了**。
//
// ⚠️ 明说查不了什么，免得「没报错」被读成「都查过了」——
// 这与 kq 那侧同一条纪律。
func BlindSpots(secrets map[string]string) []string {
	var short []string
	for name, v := range secrets {
		if len(v) < 8 {
			short = append(short, name)
		}
	}
	sort.Strings(short)
	return short
}

// ⚠️ 落盘与脱敏都**与平台无关**，所以放在这里而不是 dump_windows.go：
// 放在那边会让非 Windows 的构建缺一个方法，而缺的是「写夹具」这种
// 与 CTP 协议毫无关系的能力。
// Write 把一份夹具落到 dir 下，并在落盘**之前**做独立的凭据复查。
//
// ⚠️ 复查放在这里而不是调用方：一个「记得先查一下」的约定，
// 与没有这道检查在出事那天是一样的。
func (fx *Fixture) Write(dir, name string, secrets map[string]string,
	logf func(string, ...any)) (string, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	b, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		return "", err
	}
	if bs := BlindSpots(secrets); len(bs) > 0 {
		// ⚠️ 明说查不了什么，免得「没报错」被读成「都查过了」。
		logf("  ⓘ 独立复查的盲区：%v —— 这几个值太短，在夹具里搜它们只会撞上数字", bs)
	}
	if err := Scrubbed(string(b), secrets); err != nil {
		return "", fmt.Errorf("脱敏自检失败，**不落盘**：%w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.json", name, fx.TradingDay))
	if _, err := os.Stat(path); err == nil {
		// ⚠️ 同名不覆盖：两份都是证据，谁也不该把谁擦掉。
		for i := 2; ; i++ {
			alt := filepath.Join(dir, fmt.Sprintf("%s-%s-%d.json", name, fx.TradingDay, i))
			if _, err := os.Stat(alt); err != nil {
				path = alt
				break
			}
		}
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	abs, _ := filepath.Abs(path)
	logf("CTP 夹具落盘 %s（%d 字节，去掉 %d 个键）", abs, len(b), len(fx.Dropped))
	return path, nil
}
