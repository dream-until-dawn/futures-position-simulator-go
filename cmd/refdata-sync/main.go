// Command refdata-sync 从上游拉规则数据，生成可入库的数据文件。
//
// ⚠️ 它**产不出一份完整快照**，这不是缺陷，是上游数据的边界：
//
//	字典给得出   合约规格（乘数、最小变动价位、到期、限价手数）、**交易时段**
//	字典给不出   PositionDateType、MaxMarginSideAlgorithm、涨跌幅比例、六个费率
//	             ——那些是 CTP 合约表里的**规则数据**，不是行情商的字典
//	字典也没有   交易日列表
//
// 所以本工具目前只产出**时段表**：那一块它给得完整，而且它正是
// `refdata.Calendar` 里此前照文档填的猜测。
//
// ⚠️ 想让它产出完整快照，缺的东西必须从别处来，而不是靠这里填默认值——
// `refdata.Builder` 的零值报错会拦住任何这样的尝试，那正是它存在的意义。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dream-until-dawn/futures-position-simulator-go/refdata"
	"github.com/dream-until-dawn/futures-position-simulator-go/refdata/live"
)

func main() {
	url := flag.String("url", live.SymbolsURL, "合约字典地址")
	products := flag.String("products", "", "要的品种，形如 SHFE.rb,DCE.m；留空报错")
	out := flag.String("out", "", "时段表输出文件；留空只打印不落盘")
	timeout := flag.Duration("timeout", 20*time.Minute, "整体超时（那份文件 334 MiB）")
	// ⚠️ raw / from 让「拉取」与「转换」分开。
	//
	// 一次拉取实测 25 分钟（238978 条、334 MiB），
	// 而转换侧的每一处改动都不该再付那 25 分钟 ——
	// 那样的代价会让人倾向于「少改一点」，而少改的那一点通常正是该改的。
	raw := flag.String("raw", "", "把筛出来的原始条目落盘到这里，供离线重跑转换")
	from := flag.String("from", "", "从 -raw 落下的文件读，不联网")
	flag.Parse()

	if err := run(*url, *products, *out, *raw, *from, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "失败：%v\n", err)
		os.Exit(1)
	}
}

func run(url, products, out, raw, from string, timeout time.Duration) error {
	// ⚠️ 必须显式给品种。字典约三万条，"全都要" 不是一个合理的默认值，
	// 而一个默认全要的工具会在第一次被跑起来时把内存吃光。
	var want []string
	for _, p := range strings.Split(products, ",") {
		if p = strings.TrimSpace(p); p != "" {
			want = append(want, p)
		}
	}
	if len(want) == 0 {
		return fmt.Errorf("必须用 -products 指定品种，形如 SHFE.rb,DCE.m —— " +
			"字典约三万条，「全都要」不是一个合理的默认值")
	}
	fmt.Printf("要的品种 %d 个：%s\n", len(want), strings.Join(want, " "))
	var syms map[string]live.Symbol
	if from != "" {
		// ⚠️ 离线路径**只跳过网络**，不跳过任何校验：
		// 转换与汇总走的仍是同一套函数。一条绕过校验的「快路径」，
		// 会成为唯一没被检查过的入口 —— 而它恰好是被用得最多的那条。
		b, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(b, &syms); err != nil {
			return fmt.Errorf("读 %s 失败：%w", from, err)
		}
		fmt.Printf("从 %s 离线读入 %d 条（未联网）", from, len(syms))
		fmt.Println()
	} else {
		fmt.Printf("拉取 %s", url)
		fmt.Println()
		fmt.Println("⚠️ 那份文件约 334 MiB，流式解析、单次请求、**不续传**")

		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		start := time.Now()
		var err error
		syms, err = live.FetchSymbols(ctx, url, matcher(want), func(scanned, kept int, bytes int64) {
			fmt.Printf("\r  扫过 %d 条，留下 %d 条，已读 %.1f MiB（%.0fs）",
				scanned, kept, float64(bytes)/1024/1024, time.Since(start).Seconds())
		})
		fmt.Println()
		if err != nil {
			return err
		}
		fmt.Printf("拉完：%d 条命中，耗时 %.0fs", len(syms), time.Since(start).Seconds())
		fmt.Println()
		if raw != "" {
			if err := dumpRaw(raw, syms); err != nil {
				return err
			}
			fmt.Printf("原始条目已落盘 %s —— 之后用 -from 离线重跑转换", raw)
			fmt.Println()
		}
	}
	if len(syms) == 0 {
		return fmt.Errorf("一条都没命中 —— 品种写法可能不对（要交易所前缀，如 SHFE.rb）")
	}

	tabs, err := live.SessionTablesOf(syms)
	if err != nil {
		return err
	}
	sort.Slice(tabs, func(i, j int) bool {
		return string(tabs[i].Exchange)+tabs[i].Product < string(tabs[j].Exchange)+tabs[j].Product
	})

	fmt.Println()
	fmt.Printf("时段表 %d 个品种：\n", len(tabs))
	for _, t := range tabs {
		fmt.Printf("  %s.%-4s 日盘 %s\n", t.Exchange, t.Product, fmtSessions(t.Day))
		if len(t.Night) == 0 {
			fmt.Printf("  %s.%-4s 夜盘 —— 无\n", t.Exchange, t.Product)
		} else {
			fmt.Printf("  %s.%-4s 夜盘 %s\n", t.Exchange, t.Product, fmtSessions(t.Night))
		}
	}

	// ⚠️ 明说产不出什么，而不是让调用方以为拿到了一份能用的快照。
	fmt.Println()
	fmt.Println("⚠️ 本工具**产不出完整快照**，缺的必须从别处来：")
	fmt.Println("   交易日列表            字典里没有")
	fmt.Println("   PositionDateType      CTP 合约表里的规则数据，决定要不要声明平今平昨")
	fmt.Println("   MaxMarginSideAlgorithm 同上，决定单向大边")
	fmt.Println("   涨跌幅比例 / 六个费率  同上")
	fmt.Println("   ⚠️ 这些**不许在这里填默认值** —— refdata.Builder 的零值报错会拦住，")
	fmt.Println("      而那正是它存在的意义：一个「差不多能用」的规格会在平今平昨和大边上静默算错。")

	if out == "" {
		return nil
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	type wireSession struct{ Start, End string }
	type wireTable struct {
		Exchange string        `json:"exchange"`
		Product  string        `json:"product"`
		Day      []wireSession `json:"day"`
		Night    []wireSession `json:"night"`
	}
	payload := struct {
		Source      string      `json:"source"`
		GeneratedAt string      `json:"generated_at"`
		Note        string      `json:"note"`
		Tables      []wireTable `json:"session_tables"`
	}{
		Source:      url,
		GeneratedAt: time.Now().In(refdata.CNZone()).Format(time.RFC3339),
		Note: "只含交易时段。⚠️ 交易日列表、PositionDateType、MaxMarginSideAlgorithm、" +
			"涨跌幅比例与六个费率均**不在**本文件内，字典给不出。",
	}
	for _, t := range tabs {
		wt := wireTable{Exchange: string(t.Exchange), Product: t.Product}
		for _, s := range t.Day {
			wt.Day = append(wt.Day, wireSession{s.Start.String(), s.End.String()})
		}
		for _, s := range t.Night {
			wt.Night = append(wt.Night, wireSession{s.Start.String(), s.End.String()})
		}
		payload.Tables = append(payload.Tables, wt)
	}
	if err := enc.Encode(payload); err != nil {
		return err
	}
	fmt.Printf("\n时段表已写入 %s\n", out)
	return nil
}

// dumpRaw 把筛出来的原始条目落盘，供 -from 离线重跑。
func dumpRaw(path string, syms map[string]live.Symbol) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", " ")
	return enc.Encode(syms)
}

func fmtSessions(ss []refdata.Session) string {
	parts := make([]string, 0, len(ss))
	for _, s := range ss {
		parts = append(parts, s.Start.String()+"–"+s.End.String())
	}
	return strings.Join(parts, "  ")
}

// matcher 生成品种筛选函数。
//
// ⚠️ 品种前缀之后**必须紧跟数字**，光靠 HasPrefix 会串品种，而且这不是假想：
// 大商所同时有 `c`（玉米）与 `cs`（淀粉），`DCE.c` 会匹配上 `DCE.cs2701`；
// 郑商所同时有 `SR`（白糖）与 `SR` 之外的多个双字母品种。
//
// 串了之后的表现是「多拉了一个品种」——它不会报错，
// 而多出来的那个品种会带着自己的时段表进汇总，
// 于是 `SessionTablesOf` 的「同品种内一致」检查也查不出它，因为它是**另一个**品种。
func matcher(want []string) func(string) bool {
	return func(id string) bool {
		for _, w := range want {
			if !strings.HasPrefix(id, w) || len(id) <= len(w) {
				continue
			}
			if c := id[len(w)]; c >= '0' && c <= '9' {
				return true
			}
		}
		return false
	}
}
