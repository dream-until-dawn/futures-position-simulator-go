// Package probe 跑六条判别实验，产出证据。
//
// 它的产物不是代码，是**原始数据**：每条实验把柜台的原话脱敏后存进 testdata/probes/，
// 后续所有版本的离线夹具都从这里来。
//
// 实验设计的共同点：**必须构造到能把候选分开的地方。**
// 「在最常见的样本上，错误答案常常等于正确答案」——单合约测不出大边的合并范围，
// 平满了测不出平仓消耗顺序，空仓测不出两个权益口径的差。
package probe

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// utf8BOM 用字节写，不用转义 —— 源码里放一个真的 BOM 字符会让 Go 编译器报
// 「invalid BOM in the middle of the file」，而那个报错指向的行号看着毫无道理。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Env 是 .env 里与本工具相关的配置。
type Env struct {
	KQUser     string
	KQPassword string

	CTPUserID   string
	CTPPassword string
	CTPBrokerID string
	CTPTdFront  string
	CTPMdFront  string
	CTPAppID    string
	CTPAuthCode string

	AllowOrder bool
	MaxVolume  int
	Symbols    []string
	DumpDir    string

	raw map[string]string
}

// Secrets 返回所有不得出现在夹具里的值，交给独立复查用。
//
// ⚠️ 调用方不得打印它。
func (e Env) Secrets() []string {
	return []string{e.KQUser, e.KQPassword, e.CTPPassword, e.CTPAuthCode, e.CTPUserID}
}

// LoadEnv 读 .env。缺文件是硬错误——探针连柜台，不该有「默认凭据」这种东西。
func LoadEnv(path string) (Env, error) {
	f, err := os.Open(path)
	if err != nil {
		return Env{}, fmt.Errorf("读不到凭据文件 %s：%w（先 cp .env.example .env 并填写）", path, err)
	}
	defer f.Close()

	raw := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// .env 可能带 UTF-8 BOM（Windows 上的编辑器常这样存），首行会因此多三个字节。
		// 不处理的话第一个键名会变成不可见前缀开头，取值恒为空——而且不会报错。
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), string(utf8BOM)))
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		raw[strings.TrimSpace(kv[0])] = strings.Trim(strings.TrimSpace(kv[1]), `"'`)
	}
	if err := sc.Err(); err != nil {
		return Env{}, err
	}

	e := Env{
		KQUser:      raw["KQ_USER"],
		KQPassword:  raw["KQ_PASSWORD"],
		CTPUserID:   raw["CTP_USER_ID"],
		CTPPassword: raw["CTP_PASSWORD"],
		CTPBrokerID: raw["CTP_BROKER_ID"],
		CTPTdFront:  raw["CTP_TD_FRONT"],
		CTPMdFront:  raw["CTP_MD_FRONT"],
		CTPAppID:    raw["CTP_APP_ID"],
		CTPAuthCode: raw["CTP_AUTH_CODE"],
		DumpDir:     raw["PROBE_DUMP_DIR"],
		raw:         raw,
	}
	e.AllowOrder = strings.EqualFold(raw["PROBE_ALLOW_ORDER"], "true")
	if v, err := strconv.Atoi(raw["PROBE_MAX_VOLUME"]); err == nil {
		e.MaxVolume = v
	}
	for _, s := range strings.Split(raw["PROBE_SYMBOLS"], ",") {
		if s = strings.TrimSpace(s); s != "" {
			e.Symbols = append(e.Symbols, s)
		}
	}
	if e.KQUser == "" || e.KQPassword == "" {
		return e, fmt.Errorf("%s 里 KQ_USER / KQ_PASSWORD 为空", path)
	}
	return e, nil
}
