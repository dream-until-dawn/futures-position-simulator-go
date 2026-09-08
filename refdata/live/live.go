// Package live 从上游拉取规则数据。
//
// ⚠️ 它独立成子包，是因为它引入 `net/http` —— 主模块的其余部分不应当因为
// 「有个地方要联网」而整体背上一个网络栈的依赖面。
//
// 目前唯一的上游是天勤的公开合约字典：
//
//	https://openmd.shinnytech.com/t/md/symbols/latest.json
//
// ⚠️ **那份文件是 334 MiB**（实测 350,177,909 字节，见 probes.md §4）。
// 所以本包**流式解析**，不整块读进内存；
// 并且**不做断点续传**——本项目记过一次教训：JSON 在记录之间被切断时
// 解析器**会**报错，真正的静默丢失只来自**拼装**。
// 于是这里的取舍是：一次请求，中途失败就整个失败，不留半份。
package live

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SymbolsURL 是天勤公开合约字典的地址。免鉴权。
const SymbolsURL = "https://openmd.shinnytech.com/t/md/symbols/latest.json"

// TradingTime 是字典里的时段表。
type TradingTime struct {
	Day   [][]string `json:"day"`
	Night [][]string `json:"night"`
}

// Symbol 是字典里一个合约条目中**本库用得上**的部分。
//
// ⚠️ 刻意不做 DisallowUnknownFields：这是**上游的**格式，不是本库的。
// 对自己的格式，多出来的字段是漂移信号；对别人的格式，多出来的字段是常态。
// 真正要卡的是**本库需要的字段缺失**，那在 Validate 里查。
type Symbol struct {
	Class          string      `json:"class"`
	InstrumentID   string      `json:"instrument_id"`
	ExchangeID     string      `json:"exchange_id"`
	InsID          string      `json:"ins_id"`
	ProductID      string      `json:"product_id"`
	VolumeMultiple float64     `json:"volume_multiple"`
	PriceTick      float64     `json:"price_tick"`
	PriceDecs      int         `json:"price_decs"`
	Expired        bool        `json:"expired"`
	ExpireDatetime float64     `json:"expire_datetime"`
	MaxLimitVolume int         `json:"max_limit_order_volume"`
	MinLimitVolume int         `json:"min_limit_order_volume"`
	TradingTime    TradingTime `json:"trading_time"`

	// ⚠️ 这两个是**每手**的单一数值，不是费率。
	//
	// 字典里它们长这样：`"commission": 31.477800000000002`、
	// `"margin": 164231.99999999997` —— **源数据本身就是 float64 往返过的**。
	// 用它们填 refdata 的费率会：① 丢掉平今这个维度（六个率压成一个数）；
	// ② 把上游的浮点噪声带进本库。所以本包**只取规格与时段**，
	// 这两个字段留在这里是为了让「我们知道它们存在且知道为什么不用」被写下来。
	Commission float64 `json:"commission"`
	Margin     float64 `json:"margin"`
}

// Validate 检查本库需要的字段齐不齐。
func (s Symbol) Validate() error {
	var missing []string
	if s.InstrumentID == "" {
		missing = append(missing, "instrument_id")
	}
	if s.ExchangeID == "" {
		missing = append(missing, "exchange_id")
	}
	if s.ProductID == "" {
		missing = append(missing, "product_id")
	}
	if s.VolumeMultiple <= 0 {
		// ⚠️ 乘数为 0 会让所有金额变成 0，而 0 看起来完全合理。
		missing = append(missing, "volume_multiple（为零或缺失）")
	}
	if s.PriceTick <= 0 {
		missing = append(missing, "price_tick（为零或缺失）")
	}
	if len(s.TradingTime.Day) == 0 && len(s.TradingTime.Night) == 0 {
		missing = append(missing, "trading_time（日盘夜盘都空）")
	}
	if len(missing) > 0 {
		return fmt.Errorf("合约 %q 缺本库需要的字段：%s",
			s.InstrumentID, strings.Join(missing, "、"))
	}
	return nil
}

// Progress 是流式拉取的进度回调。传 nil 表示不需要。
//
// ⚠️ 它不是装饰。一次 334 MiB 的下载若全程无输出，
// 「还在跑」与「卡住了」在终端上长得一模一样。
type Progress func(scanned, kept int, bytes int64)

// FetchSymbols 流式拉取合约字典，只保留 want 判为真的条目。
//
// ⚠️ 三条刻意：
//
//	单次请求，不用 Range，不续传 —— 拼装才是静默丢失的来源
//	流式解析，不整块读进内存 —— 那是 334 MiB
//	want 为 nil 时**报错**，不默认「全都要」 —— 全都要意味着约三万个条目进内存
func FetchSymbols(ctx context.Context, url string, want func(id string) bool, p Progress) (map[string]Symbol, error) {
	if want == nil {
		return nil, fmt.Errorf("必须给出筛选函数 —— " +
			"字典有约三万个条目，「全都要」不是一个合理的默认值，要就显式写出来")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("拉取 %s 失败：%w", url, err)
	}
	defer resp.Body.Close()
	// ⚠️ 只接受 200。206（部分内容）在这里是**错误**而不是可用结果：
	// 半份字典解析出来的东西看起来完全正常，只是少了一些合约。
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取 %s 得到状态码 %d —— "+
			"只接受 200；206 这类部分内容在这里是错误而不是可用结果，"+
			"半份字典解析出来的东西看起来完全正常，只是少了一些合约",
			url, resp.StatusCode)
	}

	counter := &countingReader{r: resp.Body}
	dec := json.NewDecoder(counter)

	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("读第一个 token 失败：%w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("字典的顶层不是对象，读到 %v", tok)
	}

	out := map[string]Symbol{}
	scanned := 0
	last := time.Now()
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("扫到第 %d 个条目时读键失败：%w（**不续传**，本次整体失败）",
				scanned+1, err)
		}
		key, _ := keyTok.(string)
		scanned++

		if !want(key) {
			// ⚠️ 不要的条目也必须**读掉**，否则解码器的位置就错了。
			// 解到 RawMessage 再丢，内存占用是单个条目的量级，不是整份文件。
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("跳过条目 %q 时失败：%w", key, err)
			}
		} else {
			var s Symbol
			if err := dec.Decode(&s); err != nil {
				return nil, fmt.Errorf("解析条目 %q 失败：%w", key, err)
			}
			out[key] = s
		}
		if p != nil && time.Since(last) > time.Second {
			p(scanned, len(out), counter.n)
			last = time.Now()
		}
	}
	if _, err := dec.Token(); err != nil { // 收尾的 '}'
		return nil, fmt.Errorf("字典没有正常收尾：%w —— "+
			"⚠️ 这说明流在中途断了。**本次整体失败，不返回半份结果**", err)
	}
	if p != nil {
		p(scanned, len(out), counter.n)
	}
	if scanned == 0 {
		return nil, fmt.Errorf("字典里一个条目都没有 —— 是真的空，还是拿到了别的东西？")
	}
	return out, nil
}

// countingReader 数读了多少字节，供进度回调用。
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
