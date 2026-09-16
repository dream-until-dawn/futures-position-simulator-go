package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CTP 夹具只有一个家：**仓库根**下的 testdata/ctp。
//
// ⚠️ 这条在此之前只写在 flag 帮助文字里，而帮助文字拦不住任何东西 ——
// `main.go` 里那句「混进去**不会报错**」是它自己承认的。两种落错都不报错：
//
//	testdata/probes          天勤 DIFF 的语料混进 CTP 夹具（帮助文字警告过的那种）
//	cmd/oracle/testdata/ctp  照抄文档里的相对路径，而 oracle 是嵌套模块、只能在 cmd/oracle 下跑
//	                         ⇒ `-dump testdata/ctp` 解析到的是 cmd/oracle 下面那个，Write 会把它 MkdirAll 出来
//
// 第二种尤其静默：路径**字面上**就是 testdata/ctp，肉眼复核也看不出不对。
const ctpFixtureHome = "testdata/ctp"

// checkCTPDumpDir 是判定本身，不碰文件系统：abs 与 root 都已是绝对路径。
//
// ⚠️ 大小写按 Windows 的口径比（本项目就跑在 Windows 上）；
// 多一层子目录也拒 —— 夹具名里带交易日，分子目录只会让「同一天的证据」散开。
func checkCTPDumpDir(abs, root string) error {
	want := filepath.Join(root, filepath.FromSlash(ctpFixtureHome))
	got := filepath.Clean(abs)
	if strings.EqualFold(got, want) {
		return nil
	}
	return fmt.Errorf("⚠️ -dump 指到 %s，**不落盘**：%s。CTP 夹具只有一个家：%s",
		got, dumpDirWhy(got, root), want)
}

// dumpDirWhy 只给**理由**，不带路径。
//
// ⚠️ 它之所以是独立的一支：理由退化（两种落错说同一句话）要能被测出来，
// 而整句错误里带着各自的 got —— 路径不同，整句就不同，
// 理由塌成一句也照样「两条消息不一样」。破坏 632 起初就是这样安静地绿着的。
func dumpDirWhy(got, root string) string {
	switch {
	case strings.EqualFold(got, filepath.Join(root, "testdata", "probes")):
		return "那是天勤 DIFF 的语料目录，CTP 夹具混进去在别处**不会报错**"
	case strings.EqualFold(filepath.Base(got), "ctp") &&
		strings.EqualFold(filepath.Base(filepath.Dir(got)), "testdata"):
		return "路径**字面上**就是 " + ctpFixtureHome + "，但它在仓库根之外 —— " +
			"oracle 是嵌套模块、只能在 cmd/oracle 下跑，照抄文档里的相对路径就会落到这里"
	}
	return "它不是仓库根下的 " + ctpFixtureHome
}

// ctpDumpDir 校验并返回 -dump 的绝对路径。空目录由调用方各自处理（有的命令允许不落盘）。
func ctpDumpDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	if err := checkCTPDumpDir(abs, root); err != nil {
		return "", err
	}
	return abs, nil
}

// repoRoot 从当前目录往上找 .git（工作树里它是文件而不是目录，所以只看存在与否）。
func repoRoot() (string, error) {
	d, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", fmt.Errorf("⚠️ 从 %s 往上找不到 .git —— 认不出仓库根，也就判不了 -dump 落得对不对", d)
		}
		d = parent
	}
}
