// Package ctp 是 SimNow / CTP 这一侧的取证客户端。
//
// ⚠️ 它与 `kq` **并列，不做统一抽象**。理由写在 docs/ctp-oracle.md 第 4 节：
// 两边的协议、字段名、回调模型、错误模型没有一处相同，硬抽一个 Client 接口
// 会得到一个两边都不像的中间层，而它会把「这两个口子本来就不一样」
// 这个**本项目最要紧的事实**藏起来。共用的是纪律（白名单脱敏、安全阀），不是代码。
//
// # ⚠️ 为什么不直接用 goctp 的高层封装
//
// `goctp` 只导出高层 `win.Trade`，而取证要用的 `ReqQryBrokerTradingParams`
// 之类挂在它**未导出**的底层类型上 —— 从包外够不着（probes.md §6.5）。
// 所以这里自己走 syscall：只用 goctp 的 `ctpdefine`（结构体定义，几千行手抄不现实）
// 与它带的 DLL。
//
// # ⚠️ 平台
//
// 本包**只支持 Windows**。CTP 在 Linux 侧是 .so + cgo，形状完全不同，
// 不在本期范围内 —— 见 docs/ctp-oracle.md 第 1 节。
package ctp

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GoctpModule 是 DLL 所在的模块目录名，**与 go.mod 里的版本号必须一致**。
//
// ⚠️ 它是一处**必然会漂**的重复：升级 goctp 时 go.mod 变了而这里不会自动变，
// 而漂开的表现是 DLL 找不到 —— 那反倒是好的（当场就知道）。
// 守卫 TestGoctpModuleMatchesGoMod 把这两处钉在一起。
const GoctpModule = "gitee.com/haifengat/goctp@v1.10.17"

// DLLDirEnv 是显式指定 DLL 目录的环境变量名。
const DLLDirEnv = "CTP_DLL_DIR"

// DLLDir 找出 CTP 的那几个 DLL 在哪。
//
// 按顺序试三条路，并且**报告用的是哪一条** ——
// ⚠️ 「找到了」与「从哪找到的」是两件事：一台机器上恰好能跑，
// 不等于换台机器也能跑，而只报「找到了」会让那个差别在换机那天才出现。
//
//	① CTP_DLL_DIR      显式指定，优先级最高
//	② GOMODCACHE       环境变量直给
//	③ go env GOMODCACHE 兜底（要有 go 工具链）
//
// ⚠️ 三条都不成就报错，**不猜**一个默认路径：一个猜出来的路径
// 在猜错时得到的是 `MustLoadDLL` 的 panic，而 panic 的信息里
// 不会有「我是猜的」这件事。
func DLLDir() (dir, how string, err error) {
	if d := os.Getenv(DLLDirEnv); d != "" {
		return d, DLLDirEnv, nil
	}
	if c := os.Getenv("GOMODCACHE"); c != "" {
		return modWin(c), "GOMODCACHE", nil
	}
	out, e := exec.Command("go", "env", "GOMODCACHE").Output()
	if e != nil {
		return "", "", fmt.Errorf(
			"找不到 CTP 的 DLL 目录：%s 未设、GOMODCACHE 未设、`go env GOMODCACHE` 也失败（%v）。"+
				"⚠️ 这里**不猜**默认路径 —— 猜错时只会得到一个 LoadDLL 的 panic，"+
				"而那条 panic 里不会有「我是猜的」这件事", DLLDirEnv, e)
	}
	c := strings.TrimSpace(string(out))
	if c == "" {
		return "", "", fmt.Errorf("`go env GOMODCACHE` 返回空 —— 找不到 CTP 的 DLL 目录")
	}
	return modWin(c), "go env GOMODCACHE", nil
}

// modWin 把模块缓存根拼成 goctp 的 win 子目录。
func modWin(cache string) string {
	return filepath.Join(cache, filepath.FromSlash(GoctpModule), "win")
}

// RequiredDLLs 是必须同时在场的几个文件。
//
// ⚠️ 逐个列出来而不是只查 ctp_trade.dll：后者会 LoadDLL 成功但在
// **第一次调用**时才因为缺 thosttraderapi_se.dll 而崩 ——
// 那时的错误信息与「参数写错了」长得一样。
var RequiredDLLs = []string{"ctp_trade.dll", "thosttraderapi_se.dll"}

// CheckDLLs 核对那几个文件真的在。
func CheckDLLs(dir string) error {
	var missing []string
	for _, n := range RequiredDLLs {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s 里缺 %v —— "+
			"⚠️ 这几个文件来自 goctp 模块（依赖裁决 A，见 docs/ctp-oracle.md）。"+
			"仓库里**刻意不放二进制**，所以它们只可能在模块缓存里；"+
			"`go mod download gitee.com/haifengat/goctp` 之后再试", dir, missing)
	}
	return nil
}
